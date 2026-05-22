package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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

	// Convert all SVGs in parallel, bounded to NumCPU workers
	var (
		warnings []string
		mu       sync.Mutex
		wg       sync.WaitGroup
	)
	sem := make(chan struct{}, runtime.NumCPU())

	for _, svgPath := range svgFiles {
		wg.Add(1)
		go func(path string) {
			sem <- struct{}{}
			defer func() {
				<-sem
				wg.Done()
			}()
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
	var texFiles []string

	filepath.Walk(workDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(workDir, path)
		existing[strings.ToLower(rel)] = true
		existing[strings.ToLower(filepath.Base(path))] = true
		if strings.HasSuffix(strings.ToLower(path), ".tex") {
			texFiles = append(texFiles, path)
		}
		return nil
	})

	reInclude := regexp.MustCompile(`\\includegraphics(?:\[.*?\])?\{([^}]+)\}`)

	for _, texPath := range texFiles {
		content, err := os.ReadFile(texPath)
		if err != nil {
			continue
		}
		for _, match := range reInclude.FindAllStringSubmatch(string(content), -1) {
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
				// Mark as existing so sibling tex files don't re-seed the same placeholder
				rel, _ := filepath.Rel(workDir, target)
				existing[strings.ToLower(rel)] = true
				existing[strings.ToLower(filepath.Base(target))] = true
			}
		}
	}
	return nil
}
