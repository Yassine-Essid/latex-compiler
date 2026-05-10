package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// convertSVGFiles converts all SVG files to PDF in parallel
func convertSVGFiles(workDir string) []string {
	// First, collect all SVG files
	var svgFiles []string
	filepath.Walk(workDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if strings.EqualFold(filepath.Ext(path), ".svg") {
			svgFiles = append(svgFiles, path)
		}
		return nil
	})

	if len(svgFiles) == 0 {
		return nil
	}

	// Convert all SVGs in parallel
	var (
		warnings []string
		mu       sync.Mutex
		wg       sync.WaitGroup
	)

	for _, svgPath := range svgFiles {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			pdfPath := path[:len(path)-len(filepath.Ext(path))] + ".pdf"
			cmd := exec.Command("rsvg-convert", "--format=pdf", "--output="+pdfPath, path)
			if out, err := cmd.CombinedOutput(); err != nil {
				mu.Lock()
				warnings = append(warnings,
					fmt.Sprintf("SVG->PDF failed for %s: %s", filepath.Base(path), strings.TrimSpace(string(out))),
				)
				mu.Unlock()
			}
		}(svgPath)
	}

	wg.Wait()
	return warnings
}

func patchIncludeSVG(workDir string) error {
	reSVG := regexp.MustCompile(`\\includesvg(\s*(?:\[[^\]]*\])?\s*)\{`)
	reIncludeSVG := regexp.MustCompile(`\\includegraphics(\s*(?:\[[^\]]*\])?\s*)\{([^}]+)\.svg\}`)

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

		patched := reIncludeSVG.ReplaceAll(content, []byte(`\includegraphics$1{$2.pdf}`))
		patched = reSVG.ReplaceAll(patched, []byte(`\includegraphics$1{`))
		if !bytes.Equal(content, patched) {
			return os.WriteFile(path, patched, info.Mode())
		}
		return nil
	})
}

func preSeedMissingAssets(workDir string) error {
	existing := make(map[string]bool)
	filepath.Walk(workDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(workDir, path)
		existing[strings.ToLower(rel)] = true
		existing[strings.ToLower(filepath.Base(path))] = true
		return nil
	})

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
