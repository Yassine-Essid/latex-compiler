package main

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var placeholderImg []byte

func init() {
	// 1x1 pixel transparent PNG
	const base64Img = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="
	var err error
	placeholderImg, err = base64.StdEncoding.DecodeString(base64Img)
	if err != nil {
		panic(err)
	}
}

func main() {
	r := gin.Default()

	// Set max upload size (e.g., 64MB)
	r.MaxMultipartMemory = 64 << 20

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "healthy"})
	})

	r.POST("/compile", compileHandler)

	// Run on port 8000
	r.Run(":8000")
}

func compileHandler(c *gin.Context) {
	// 1. Parse Multipart Form
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

	// 2. Create a unique workspace
	jobID := uuid.New().String()
	workDir := filepath.Join("/tmp", "latex", jobID)

	// Ensure cleanup happens after request finishes
	defer os.RemoveAll(workDir)

	if err := os.MkdirAll(workDir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create workspace"})
		return
	}

	// 3. Save all files
	mainFileFound := false
	for _, file := range files {
		filename := filepath.Base(file.Filename)
		if filename == "main.tex" {
			mainFileFound = true
		}
		if err := c.SaveUploadedFile(file, filepath.Join(workDir, filename)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save file: " + filename})
			return
		}
	}

	if !mainFileFound {
		c.JSON(http.StatusBadRequest, gin.H{"error": "main.tex is required"})
		return
	}

	// 4. Prepare compilation command (latexmk)
	inputPath := filepath.Join(workDir, "main.tex")
	// We use a context with timeout to prevent hanging processes
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var output []byte

	// Regex to find missing files
	// Matches:
	// ! LaTeX Error: File 'foo.tex' not found.
	// ! Package pdftex.def Error: File `foo.jpg' not found.
	// LaTeX Warning: File `foo.jpg' not found
	reMissingFile := regexp.MustCompile(`(?:File|file) ['` + "`" + `](.+?)['] not found`)

	maxRetries := 3
	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Command: latexmk -pdf -interaction=nonstopmode -output-directory=DIR file
		cmd := exec.CommandContext(ctx, "latexmk",
			"-pdf",                     // Output PDF
			"-interaction=nonstopmode", // Don't ask for user input on error
			"-no-shell-escape",         // SECURITY: Disable \write18 system commands
			"-output-directory="+workDir,
			inputPath,
		)

		output, err = cmd.CombinedOutput()

		// If success, break
		if err == nil {
			break
		}

		// If context timed out, stop
		if ctx.Err() == context.DeadlineExceeded {
			break
		}

		// Check for missing files in output
		matches := reMissingFile.FindAllStringSubmatch(string(output), -1)
		if len(matches) == 0 {
			// No missing files found, some other error
			break
		}

		filesCreated := 0
		for _, match := range matches {
			missingFile := match[1]

			// Ignore .tex files or other source files to prevent infinite loops or bad overwrites
			ext := strings.ToLower(filepath.Ext(missingFile))
			if ext == ".tex" || ext == ".sty" || ext == ".cls" || ext == ".bib" {
				continue
			}

			// If no extension, assume it's an image and append .png (since our placeholder is png)
			// LaTeX often reports "File 'foo' not found" for \includegraphics{foo}
			targetPath := filepath.Join(workDir, missingFile)
			if ext == "" {
				targetPath += ".png"
			}

			// Create the placeholder file
			if err := os.WriteFile(targetPath, placeholderImg, 0644); err == nil {
				filesCreated++
				log.Printf("Created placeholder for missing file: %s", missingFile)
			}
		}

		if filesCreated == 0 {
			// We found missing file errors but couldn't/shouldn't fix them (e.g. missing .tex)
			break
		}

		// If we created files, loop again to retry compilation
	}

	// 5. Handle Errors
	if ctx.Err() == context.DeadlineExceeded {
		log.Printf("Job %s timed out", jobID)
		c.JSON(http.StatusRequestTimeout, gin.H{"error": "Compilation timed out"})
		return
	}

	if err != nil {
		log.Printf("Job %s failed", jobID)

		// Extract easy-to-read errors from the latexmk output
		latexErrors := extractLatexErrors(string(output))

		// Return the parsed errors along with the full compiler log
		c.JSON(http.StatusBadRequest, gin.H{
			"error":        "Compilation failed",
			"latex_errors": latexErrors,
			"logs":         string(output),
		})
		return
	}

	// 6. Return the PDF
	pdfPath := filepath.Join(workDir, "main.pdf")
	if _, err := os.Stat(pdfPath); os.IsNotExist(err) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "PDF was not generated despite successful exit code"})
		return
	}

	// 7. Create ZIP with artifacts
	zipPath := filepath.Join(workDir, "artifacts.zip")
	artifacts := []string{"main.pdf", "main.log", "main.toc", "main.aux"}

	if err := zipFiles(zipPath, workDir, artifacts); err != nil {
		log.Printf("Failed to zip artifacts: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create zip archive"})
		return
	}

	c.FileAttachment(zipPath, "artifacts.zip")
}

// extractLatexErrors scans the compiler output for lines starting with "!"
// to provide a cleaner array of error messages for the user.
func extractLatexErrors(output string) []string {
	var errors []string
	lines := strings.Split(output, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		// LaTeX errors typically start with "!"
		if strings.HasPrefix(line, "!") {
			errStr := line
			// Grabbing the next line often provides useful context (e.g. file/line number)
			if i+1 < len(lines) {
				contextLine := strings.TrimSpace(lines[i+1])
				if contextLine != "" && !strings.HasPrefix(contextLine, "!") {
					errStr += " " + contextLine
				}
			}
			errors = append(errors, errStr)
		}
	}
	if len(errors) == 0 {
		errors = append(errors, "Could not extract specific LaTeX errors. Please check the full logs.")
	}
	return errors
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

		// Check if file exists
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			continue // Skip missing files
		}

		fileToZip, err := os.Open(filePath)
		if err != nil {
			return err
		}
		defer fileToZip.Close()

		info, err := fileToZip.Stat()
		if err != nil {
			return err
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filename
		header.Method = zip.Deflate

		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			return err
		}
		_, err = io.Copy(writer, fileToZip)
		if err != nil {
			return err
		}
	}
	return nil
}