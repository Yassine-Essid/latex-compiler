package main

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var placeholderImg []byte

// Regex patterns for error extraction
var (
	reBoxError      = regexp.MustCompile(`(Overfull|Underfull)\\s*\\hbox\s*\([^)]*\)\s*in\s*paragraph\s*at\s*lines\s*(\d+)(?:--(\d+))?`)
	reErrorLine     = regexp.MustCompile(`^l\.(\d+)`)
	reFileInParen   = regexp.MustCompile(`\(([^)]+\.(?:tex|sty|cls|bib|pdf|png|jpg|jpeg|eps))`)
	reLineInFile    = regexp.MustCompile(`l\.(\d+)\s+in\s+([^\s]+)`)
	reMissingChar   = regexp.MustCompile(`Missing character: There is no (.+?) in font`)
	rePackageError  = regexp.MustCompile(`Package\s+(\w+)\s+Error:\s*(.+)`)
	reLatexError    = regexp.MustCompile(`LaTeX Error:\s*(.+)`)
	reMissingFile   = regexp.MustCompile(`(?:File|file) ['` + "`" + `](.+?)['] not found`)
)

func init() {
	const base64Img = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="
	var err error
	placeholderImg, err = base64.StdEncoding.DecodeString(base64Img)
	if err != nil {
		panic(err)
	}
}

func main() {
	r := gin.Default()
	r.MaxMultipartMemory = 64 << 20

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "healthy"})
	})

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

	mainFileFound := false
	for _, file := range files {
		filename := filepath.Base(file.Filename)
		if filename == "main.tex" {
			mainFileFound = true
		}
		if err := c.SaveUploadedFile(file, filepath.Join(workDir, filename)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save file"})
			return
		}
	}

	if !mainFileFound {
		c.JSON(http.StatusBadRequest, gin.H{"error": "main.tex is required"})
		return
	}

	inputPath := filepath.Join(workDir, "main.tex")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var output []byte
	maxRetries := 3
	for attempt := 0; attempt <= maxRetries; attempt++ {
		cmd := exec.CommandContext(ctx, "latexmk",
			"-pdf",
			"-interaction=nonstopmode",
			"-no-shell-escape",
			"-output-directory="+workDir,
			inputPath,
		)

		output, err = cmd.CombinedOutput()
		if err == nil || ctx.Err() == context.DeadlineExceeded {
			break
		}

		matches := reMissingFile.FindAllStringSubmatch(string(output), -1)
		if len(matches) == 0 {
			break
		}

		filesCreated := 0
		for _, match := range matches {
			missingFile := match[1]
			ext := strings.ToLower(filepath.Ext(missingFile))
			if ext == ".tex" || ext == ".sty" || ext == ".cls" || ext == ".bib" {
				continue
			}
			targetPath := filepath.Join(workDir, missingFile)
			if ext == "" {
				targetPath += ".png"
			}
			if err := os.WriteFile(targetPath, placeholderImg, 0644); err == nil {
				filesCreated++
			}
		}
		if filesCreated == 0 {
			break
		}
	}

	if ctx.Err() == context.DeadlineExceeded {
		c.JSON(http.StatusRequestTimeout, gin.H{"error": "Compilation timed out"})
		return
	}

	if err != nil {
		latexErrors := extractLatexErrors(string(output))
		c.JSON(http.StatusBadRequest, gin.H{
			"error":        "Compilation failed",
			"latex_errors": latexErrors,
			"logs":         string(output),
		})
		return
	}

	zipPath := filepath.Join(workDir, "artifacts.zip")
	artifacts := []string{"main.pdf", "main.log", "main.toc", "main.aux"}

	if err := zipFiles(zipPath, workDir, artifacts); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create zip"})
		return
	}

	c.FileAttachment(zipPath, "artifacts.zip")
}

type LaTeXError struct {
	Type    string `json:"type"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"`
	Context string `json:"context"`
}

func extractLatexErrors(output string) []LaTeXError {
	var errors []LaTeXError
	lines := strings.Split(output, "\n")
	totalLines := len(lines)

	currentParsingFile := "main.tex" // Default

	for i := 0; i < totalLines; i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		// Track current file context
		if strings.HasPrefix(line, "(") {
			if matches := reFileInParen.FindStringSubmatch(line); len(matches) > 1 {
				currentParsingFile = extractFileName(matches[1])
			}
		}

		// Detect standard LaTeX Error block
		if strings.HasPrefix(line, "!") {
			// Skip generic fatal messages that aren't specific errors
			if strings.Contains(line, "==> Fatal error") || strings.Contains(line, "Emergency stop") {
				continue
			}

			newErr := LaTeXError{
				Type:    "error",
				Line:    -1,
				File:    currentParsingFile,
				Message: strings.TrimSpace(strings.TrimPrefix(line, "!")),
				Context: line,
			}

			// Look ahead for the line number (extended to handle errors with help text)
			for j := i + 1; j < i+16 && j < totalLines; j++ {
				next := strings.TrimSpace(lines[j])
				
				// Case 1: Standard l.XX
				if matches := reErrorLine.FindStringSubmatch(next); len(matches) > 1 {
					if ln, err := strconv.Atoi(matches[1]); err == nil {
						newErr.Line = ln
						newErr.Context += "\n" + next
						break 
					}
				}
				// Case 2: Package specific l.XX in file
				if matches := reLineInFile.FindStringSubmatch(next); len(matches) > 2 {
					ln, _ := strconv.Atoi(matches[1])
					newErr.Line = ln
					newErr.File = extractFileName(matches[2])
					newErr.Context += "\n" + next
					break
				}
				
				if next != "" {
					newErr.Context += "\n" + next
				}
			}

			// Specific cleanups for messages
			if strings.Contains(newErr.Message, "LaTeX Error:") {
				if m := reLatexError.FindStringSubmatch(newErr.Message); len(m) > 1 {
					newErr.Message = m[1]
				}
			}

			errors = append(errors, newErr)
			continue
		}

		// Handle Overfull/Underfull boxes
		if matches := reBoxError.FindStringSubmatch(line); len(matches) > 0 {
			startLine, _ := strconv.Atoi(matches[2])
			errors = append(errors, LaTeXError{
				Type:    "warning",
				Line:    startLine,
				File:    currentParsingFile,
				Message: line,
				Context: line,
			})
		}
	}

	// Fallback if compilation failed but no '!' errors were caught
	if len(errors) == 0 {
		errors = append(errors, LaTeXError{
			Type:    "error",
			Line:    -1,
			Message: "Compilation failed. Check logs for details.",
		})
	}

	return deduplicateErrors(errors)
}

func extractFileName(path string) string {
	path = strings.Trim(path, "() \t\n\r")
	return filepath.Base(path)
}

func deduplicateErrors(errs []LaTeXError) []LaTeXError {
	unique := []LaTeXError{}
	seen := make(map[string]bool)
	for _, e := range errs {
		key := fmt.Sprintf("%s:%d:%s", e.File, e.Line, e.Message)
		if !seen[key] {
			seen[key] = true
			unique = append(unique, e)
		}
	}
	return unique
}

func zipFiles(zipPath, baseDir string, files []string) error {
	newZipFile, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer newZipFile.Close()

	zipWriter := zip.NewWriter(newZipFile)
	defer zipWriter.Close()

	for _, filename := range files {
		filePath := filepath.Join(baseDir, filename)
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			continue
		}

		fileToZip, err := os.Open(filePath)
		if err != nil {
			return err
		}
		
		info, _ := fileToZip.Stat()
		header, _ := zip.FileInfoHeader(info)
		header.Name = filename
		header.Method = zip.Deflate

		writer, _ := zipWriter.CreateHeader(header)
		io.Copy(writer, fileToZip)
		fileToZip.Close()
	}
	return nil
}