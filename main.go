package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

var placeholderImg []byte

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
	ws, err := newWorkspace()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create workspace"})
		return
	}
	defer ws.cleanup()

	if err := saveUploadedFiles(c, ws); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	convertSVGFiles(ws.dir)
	patchIncludeSVG(ws.dir)
	preSeedMissingAssets(ws.dir)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	output, compileErr := compile(ctx, ws.dir)

	if ctx.Err() == context.DeadlineExceeded {
		c.JSON(http.StatusRequestTimeout, gin.H{"error": "Compilation timed out after 60s"})
		return
	}

	if compileErr != nil {
		errs := extractErrors(string(output), ws.dir)
		c.JSON(http.StatusBadRequest, gin.H{
			"error":        "Compilation failed",
			"latex_errors": errs,
			"logs":         string(output),
		})
		return
	}

	zipPath, err := createZip(ws.dir, []string{"main.pdf", "main.log", "main.toc", "main.aux"})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create zip"})
		return
	}

	c.FileAttachment(zipPath, "artifacts.zip")
}
