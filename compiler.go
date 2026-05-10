package main

import (
	"context"
	"os/exec"
	"path/filepath"
)

func compile(ctx context.Context, workDir string) ([]byte, error) {
	inputPath := filepath.Join(workDir, "main.tex")

	// Run 3 passes to ensure auxiliary files (.toc, .aux, .lof, .lot) are stable.
	// Pass 1: Initial compilation - generates auxiliary files
	// Pass 2: Incorporates auxiliary file content into the document
	// Pass 3: Stabilizes page numbers and cross-references
	for i := 0; i < 2; i++ {
		runXeLatex(ctx, workDir, inputPath)
	}

	// Final pass - return its output and error
	return runXeLatex(ctx, workDir, inputPath)
}

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
