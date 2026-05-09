package main

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func createZip(workDir string, files []string) (string, error) {
	zipPath := filepath.Join(workDir, "artifacts.zip")

	f, err := os.Create(zipPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	w := zip.NewWriter(f)
	defer w.Close()

	for _, name := range files {
		path := filepath.Join(workDir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}

		src, err := os.Open(path)
		if err != nil {
			return "", err
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
			return "", err
		}
		io.Copy(writer, src)
		src.Close()
	}
	return zipPath, nil
}
