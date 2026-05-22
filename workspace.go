package main

import (
	"fmt"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type workspace struct {
	dir string
}

func newWorkspace() (*workspace, error) {
	jobID := uuid.New().String()
	dir := filepath.Join("/tmp", "latex", jobID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return &workspace{dir: dir}, nil
}

func (ws *workspace) cleanup() {
	os.RemoveAll(ws.dir)
}

func saveUploadedFiles(c *gin.Context, ws *workspace) error {
	form, err := c.MultipartForm()
	if err != nil {
		return fmt.Errorf("failed to parse multipart form: %w", err)
	}

	files := form.File["files"]
	if len(files) == 0 {
		return fmt.Errorf("no files uploaded. Key must be 'files'")
	}

	var (
		saveErr       error
		mu            sync.Mutex
		wg            sync.WaitGroup
		mainFileFound bool
	)
	sem := make(chan struct{}, runtime.NumCPU())

	for _, file := range files {
		wg.Add(1)
		go func(f *multipart.FileHeader) {
			sem <- struct{}{}
			defer func() {
				<-sem
				wg.Done()
			}()

			// Go's multipart package natively calls filepath.Base() on f.Filename for security.
			// To support nested folders, we must extract the original "filename" from the header.
			_, params, err := mime.ParseMediaType(f.Header.Get("Content-Disposition"))
			originalFilename := f.Filename
			if err == nil && params["filename"] != "" {
				originalFilename = params["filename"]
			}

			destPath, err := sanitizePath(ws.dir, originalFilename)
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
		return fmt.Errorf("failed to save file: %w", saveErr)
	}
	if !mainFileFound {
		return fmt.Errorf("main.tex is required")
	}
	return nil
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
