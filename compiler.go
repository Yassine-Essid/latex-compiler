package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
)

func compile(ctx context.Context, workDir string) ([]byte, error) {
	inputPath := filepath.Join(workDir, "main.tex")

	output, err := runPdfLatex(ctx, workDir, inputPath)

	if err != nil {
		outputStr := string(output)
		if strings.Contains(outputStr, "Rerun to get") ||
			strings.Contains(outputStr, "Label(s) may have changed") ||
			strings.Contains(outputStr, "rerunfilecheck") {
			output, err = runPdfLatex(ctx, workDir, inputPath)
		}
	}

	if err != nil && !strings.Contains(string(output), "==> Fatal error") {
		output, err = runLatexMk(ctx, workDir, inputPath)
	}

	return output, err
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
