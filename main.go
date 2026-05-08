package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var placeholderImg []byte

var (
	reBoxError = regexp.MustCompile(
		`(Overfull|Underfull)\s*\\hbox\s*\([^)]*\)\s*in\s*paragraph\s*at\s*lines?\s*(\d+)(?:--(\d+))?`,
	)
	reErrorLine = regexp.MustCompile(`^l\.(\d+)\s*(.*)`)

	reFileOpen = regexp.MustCompile(
		`^\((\./[^\s)]+\.(?:tex|sty|cls|bib|fd|def))|^\(([^\s()]+\.(?:tex|sty|cls|bib|fd|def))`,
	)

	reMissingFile = regexp.MustCompile("['`]([^'`]+?\\.(?:tex|bib|cls|sty|pdf|png|jpg))[`']")
	rePackageErr  = regexp.MustCompile(`^(?:Package|Class)\s+(\S+)\s+(?:Error|Warning):\s*(.+)`)
	reLatexError  = regexp.MustCompile(`^LaTeX Error:\s*(.+)`)
	reUndefinedCS = regexp.MustCompile(`\\([a-zA-Z@*]+)`)
)

func init() {
	const b64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="
	var err error
	placeholderImg, err = base64.StdEncoding.DecodeString(b64)
	if err != nil {
		panic(err)
	}
}

func main() {
	r := gin.Default()
	r.MaxMultipartMemory = 64 << 20
	r.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"status": "healthy"}) })
	r.POST("/compile", compileHandler)
	r.Run(":8000")
}

func compileHandler(c *gin.Context) {
	form, err := c.MultipartForm()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to parse multipart form"})
		return
	}

	files := form.File["files"]
	if len(files) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No files uploaded. Key must be 'files'"})
		return
	}

	jobID := uuid.New().String()
	workDir := filepath.Join("/tmp", "latex", jobID)
	defer os.RemoveAll(workDir)

	if err := os.MkdirAll(workDir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create workspace"})
		return
	}

	var (
		saveErr       error
		mu            sync.Mutex
		wg            sync.WaitGroup
		mainFileFound bool
	)

	for _, file := range files {
		wg.Add(1)
		go func(f *multipart.FileHeader) {
			defer wg.Done()

			destPath, err := sanitizePath(workDir, f.Filename)
			if err != nil {
				mu.Lock()
				if saveErr == nil {
					saveErr = fmt.Errorf("rejected path %q: %w", f.Filename, err)
				}
				mu.Unlock()
				return
			}

			if filepath.Base(destPath) == "main.tex" {
				mu.Lock()
				mainFileFound = true
				mu.Unlock()
			}

			if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
				mu.Lock()
				if saveErr == nil {
					saveErr = err
				}
				mu.Unlock()
				return
			}

			if err := c.SaveUploadedFile(f, destPath); err != nil {
				mu.Lock()
				if saveErr == nil {
					saveErr = err
				}
				mu.Unlock()
			}
		}(file)
	}
	wg.Wait()

	if saveErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save file: " + saveErr.Error()})
		return
	}
	if !mainFileFound {
		c.JSON(http.StatusBadRequest, gin.H{"error": "main.tex is required"})
		return
	}

	if warns := convertSVGFiles(workDir); len(warns) > 0 {
		fmt.Printf("[SVG] warnings: %v\n", warns)
	}
	if err := patchIncludeSVG(workDir); err != nil {
		fmt.Printf("[SVG] patch warning: %v\n", err)
	}

	if err := preSeedMissingAssets(workDir, files); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to prepare assets: " + err.Error()})
		return
	}

	inputPath := filepath.Join(workDir, "main.tex")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	output, compileErr := runPdfLatex(ctx, workDir, inputPath)

	if compileErr != nil {
		outputStr := string(output)
		if strings.Contains(outputStr, "Rerun to get") ||
			strings.Contains(outputStr, "Label(s) may have changed") ||
			strings.Contains(outputStr, "rerunfilecheck") {
			output, compileErr = runPdfLatex(ctx, workDir, inputPath)
		}
	}

	if compileErr != nil && !strings.Contains(string(output), "==> Fatal error") {
		output, compileErr = runLatexMk(ctx, workDir, inputPath)
	}

	if ctx.Err() == context.DeadlineExceeded {
		c.JSON(http.StatusRequestTimeout, gin.H{"error": "Compilation timed out after 60s"})
		return
	}

	if compileErr != nil {
		errs := extractLatexErrors(string(output), workDir)
		c.JSON(http.StatusBadRequest, gin.H{
			"error":        "Compilation failed",
			"latex_errors": errs,
			"logs":         string(output),
		})
		return
	}

	zipPath := filepath.Join(workDir, "artifacts.zip")
	if err := zipFiles(zipPath, workDir, []string{"main.pdf", "main.log", "main.toc", "main.aux"}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create zip"})
		return
	}

	c.FileAttachment(zipPath, "artifacts.zip")
}

func sanitizePath(baseDir, relativePath string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relativePath))
	clean = strings.TrimPrefix(clean, string(filepath.Separator))

	if strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("path traversal rejected")
	}

	joined := filepath.Join(baseDir, clean)

	if !strings.HasPrefix(joined+string(filepath.Separator), baseDir+string(filepath.Separator)) {
		return "", fmt.Errorf("computed path escapes workspace")
	}

	return joined, nil
}

func extractRelativePath(absPath, workDir string) string {
	clean := filepath.Clean(absPath)
	prefix := filepath.Clean(workDir) + string(filepath.Separator)

	if strings.HasPrefix(clean, prefix) {
		return clean[len(prefix):]
	}

	if rel := strings.TrimPrefix(clean, "./"); rel != clean {
		return rel
	}

	return filepath.Base(clean)
}

func convertSVGFiles(workDir string) []string {
	var warnings []string

	err := filepath.Walk(workDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.EqualFold(filepath.Ext(path), ".svg") {
			return nil
		}

		pdfPath := path[:len(path)-len(filepath.Ext(path))] + ".pdf"
		cmd := exec.Command("rsvg-convert", "--format=pdf", "--output="+pdfPath, path)
		if out, err := cmd.CombinedOutput(); err != nil {
			warnings = append(warnings,
				fmt.Sprintf("SVG->PDF failed for %s: %s", filepath.Base(path), strings.TrimSpace(string(out))),
			)
		}
		return nil
	})

	if err != nil {
		warnings = append(warnings, fmt.Sprintf("SVG walk error: %v", err))
	}
	return warnings
}

func patchIncludeSVG(workDir string) error {
	reSVG := regexp.MustCompile(`\\includesvg(\s*(?:\[[^\]]*\])?\s*)\{`)

	return filepath.Walk(workDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".tex") {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		patched := reSVG.ReplaceAll(content, []byte(`\includegraphics$1{`))
		if !bytes.Equal(content, patched) {
			return os.WriteFile(path, patched, info.Mode())
		}
		return nil
	})
}

func runPdfLatex(ctx context.Context, workDir, inputPath string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "pdflatex",
		"-interaction=nonstopmode",
		"-no-shell-escape",
		"-output-directory="+workDir,
		inputPath,
	)
	cmd.Dir = workDir
	return cmd.CombinedOutput()
}

func runLatexMk(ctx context.Context, workDir, inputPath string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "latexmk",
		"-pdf",
		"-interaction=nonstopmode",
		"-no-shell-escape",
		"-output-directory="+workDir,
		inputPath,
	)
	cmd.Dir = workDir
	return cmd.CombinedOutput()
}

func preSeedMissingAssets(workDir string, files []*multipart.FileHeader) error {
	existing := make(map[string]bool)
	for _, f := range files {
		existing[strings.ToLower(filepath.Base(f.Filename))] = true
		existing[strings.ToLower(filepath.ToSlash(filepath.Clean(f.Filename)))] = true
	}

	mainTex, err := os.ReadFile(filepath.Join(workDir, "main.tex"))
	if err != nil {
		return err
	}

	reInclude := regexp.MustCompile(`\\includegraphics(?:\[.*?\])?\{([^}]+)\}`)
	for _, match := range reInclude.FindAllStringSubmatch(string(mainTex), -1) {
		ref := match[1]
		found := false
		for _, ext := range []string{"", ".png", ".jpg", ".jpeg", ".pdf", ".eps", ".svg"} {
			if existing[strings.ToLower(ref+ext)] {
				found = true
				break
			}
		}
		if !found {
			target := filepath.Join(workDir, ref)
			if filepath.Ext(ref) == "" {
				target += ".png"
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			if err := os.WriteFile(target, placeholderImg, 0644); err != nil {
				return err
			}
		}
	}
	return nil
}

func unwrapLogLines(raw string) string {
	const maxWidth = 79

	isNewEntry := func(s string) bool {
		return len(s) == 0 ||
			strings.HasPrefix(s, "!") ||
			strings.HasPrefix(s, "l.") ||
			strings.HasPrefix(s, "Package") ||
			strings.HasPrefix(s, "LaTeX") ||
			strings.HasPrefix(s, "Class") ||
			strings.HasPrefix(s, "Overfull") ||
			strings.HasPrefix(s, "Underfull") ||
			strings.HasPrefix(s, " [") ||
			strings.HasPrefix(s, "Output written")
	}

	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	i := 0

	for i < len(lines) {
		line := lines[i]
		for len(line) == maxWidth && i+1 < len(lines) {
			next := lines[i+1]
			if isNewEntry(next) {
				break
			}
			line = line + next
			i++
		}
		out = append(out, line)
		i++
	}

	return strings.Join(out, "\n")
}

type LaTeXError struct {
	Type    string `json:"type"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"`
	Source  string `json:"source,omitempty"`
}

func extractLatexErrors(rawOutput, workDir string) []LaTeXError {
	output := unwrapLogLines(rawOutput)
	lines := strings.Split(output, "\n")

	var errors []LaTeXError

	fileStack := []string{"main.tex"}
	currentFile := func() string { return fileStack[len(fileStack)-1] }

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "(") {
			if m := reFileOpen.FindStringSubmatch(trimmed); m != nil {
				raw := m[1]
				if raw == "" {
					raw = m[2]
				}
				fileStack = append(fileStack, extractRelativePath(raw, workDir))
			}
		}
		if strings.HasSuffix(trimmed, ")") && !strings.Contains(trimmed, "(") && len(fileStack) > 1 {
			fileStack = fileStack[:len(fileStack)-1]
		}

		if m := reBoxError.FindStringSubmatch(trimmed); m != nil {
			startLine, _ := strconv.Atoi(m[2])
			errors = append(errors, LaTeXError{
				Type:    "warning",
				Line:    startLine,
				File:    currentFile(),
				Message: trimmed,
			})
			continue
		}

		if !strings.HasPrefix(trimmed, "!") {
			continue
		}
		if strings.Contains(trimmed, "==> Fatal error") {
			continue
		}

		e := LaTeXError{
			Type:    "error",
			Line:    -1,
			File:    currentFile(),
			Message: strings.TrimRight(strings.TrimSpace(strings.TrimPrefix(trimmed, "!")), "."),
		}

		if m := reLatexError.FindStringSubmatch(e.Message); m != nil {
			e.Message = strings.TrimRight(strings.TrimSpace(m[1]), ".")
		}

		if strings.Contains(e.Message, "Emergency stop") {
			e.Message = "LaTeX aborted - likely an unclosed environment or fatal syntax error"
		}

		for j := i + 1; j < i+20 && j < len(lines); j++ {
			next := strings.TrimSpace(lines[j])

			if m := reErrorLine.FindStringSubmatch(next); m != nil {
				ln, _ := strconv.Atoi(m[1])
				e.Line = ln

				snippet := strings.TrimSpace(m[2])
				if snippet != "" {
					e.Source = snippet
					if strings.Contains(e.Message, "Undefined control sequence") {
						if cs := reUndefinedCS.FindString(snippet); cs != "" {
							e.Message = "Undefined control sequence: " + cs
						}
					}
				}
				break
			}

			if strings.HasPrefix(next, "<*>") {
				path := strings.TrimSpace(strings.TrimPrefix(next, "<*>"))
				rel := extractRelativePath(path, workDir)
				if strings.HasSuffix(rel, ".tex") {
					e.File = rel
				}
				continue
			}

			if m := reMissingFile.FindStringSubmatch(next); m != nil {
				e.Source = "missing: " + m[1]
			}

			if m := rePackageErr.FindStringSubmatch(next); m != nil {
				e.Message = fmt.Sprintf("[%s] %s", m[1], strings.TrimSpace(m[2]))
			}

			if strings.HasPrefix(next, "!") {
				break
			}
		}

		errors = append(errors, e)
	}

	if len(errors) == 0 {
		errors = append(errors, LaTeXError{
			Type:    "error",
			Line:    -1,
			File:    "main.tex",
			Message: "Compilation failed - no specific error was identified; check the full logs",
		})
	}

	return deduplicateErrors(errors)
}

func deduplicateErrors(errs []LaTeXError) []LaTeXError {
	seen := make(map[string]bool)
	out := make([]LaTeXError, 0, len(errs))
	for _, e := range errs {
		key := fmt.Sprintf("%s:%d:%s", e.File, e.Line, e.Message)
		if !seen[key] {
			seen[key] = true
			out = append(out, e)
		}
	}
	return out
}

func zipFiles(zipPath, baseDir string, files []string) error {
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer f.Close()

	w := zip.NewWriter(f)
	defer w.Close()

	for _, name := range files {
		path := filepath.Join(baseDir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}

		src, err := os.Open(path)
		if err != nil {
			return err
		}

		info, _ := src.Stat()
		hdr, _ := zip.FileInfoHeader(info)
		hdr.Name = name
		if strings.HasSuffix(strings.ToLower(name), ".pdf") {
			hdr.Method = zip.Store
		} else {
			hdr.Method = zip.Deflate
		}

		writer, err := w.CreateHeader(hdr)
		if err != nil {
			src.Close()
			return err
		}
		io.Copy(writer, src)
		src.Close()
	}
	return nil
}
