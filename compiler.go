package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// multiPassMarkers are LaTeX commands that require multiple compilation passes
var multiPassMarkers = []string{
	`\tableofcontents`,
	`\listoffigures`,
	`\listoftables`,
	`\ref{`,
	`\pageref{`,
	`\nameref{`,
	`\cite{`,
	`\citep{`,
	`\citet{`,
	`\autocite{`,
	`\bibliography{`,
	`\printbibliography`,
	`\makeindex`,
	`\printindex`,
}

// needsMultiplePasses checks if the document requires multiple compilation passes
// by scanning for cross-reference commands, TOC, citations, etc.
func needsMultiplePasses(workDir string) bool {
	content, err := os.ReadFile(filepath.Join(workDir, "main.tex"))
	if err != nil {
		return true // Default to multiple passes if we can't read the file
	}

	text := string(content)
	for _, marker := range multiPassMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func compile(ctx context.Context, workDir string) ([]byte, error) {
	inputPath := filepath.Join(workDir, "main.tex")

	if needsMultiplePasses(workDir) {
		// Intermediate pass WITHOUT PDF generation - much faster
		// This generates .aux, .toc, .lof, .lot files needed for cross-references
		runXeLatexNoPDF(ctx, workDir, inputPath)
	}

	// Final pass WITH PDF generation
	return runXeLatex(ctx, workDir, inputPath)
}

// runXeLatexNoPDF runs xelatex without generating PDF (produces .xdv instead)
// This is significantly faster and sufficient for generating auxiliary files
func runXeLatexNoPDF(ctx context.Context, workDir, inputPath string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "xelatex",
		"-interaction=nonstopmode",
		"-no-shell-escape",
		"-no-pdf",
		"-output-directory="+workDir,
		inputPath,
	)
	cmd.Dir = workDir
	return cmd.CombinedOutput()
}

// runXeLatex runs xelatex with full PDF generation
func runXeLatex(ctx context.Context, workDir, inputPath string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "xelatex",
		"-interaction=nonstopmode",
		"-no-shell-escape",
		"-output-directory="+workDir,
		inputPath,
	)
	cmd.Dir = workDir
	return cmd.CombinedOutput()
}
