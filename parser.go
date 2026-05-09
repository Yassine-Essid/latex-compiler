package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	reBoxError = regexp.MustCompile(
		`(Overfull|Underfull)\s*\\hbox\s*\([^)]*\)\s*in\s*paragraph\s*at\s*lines?\s*(\d+)(?:--(\d+))?`,
	)
	reErrorLine = regexp.MustCompile(`^l\.(\d+)\s*(.*)`)

	reFileOpen = regexp.MustCompile(
		`^\((\./[^\s)]+\.(?:tex|sty|cls|bib|fd|def))|^\(([^\s()]+\.(?:tex|sty|cls|bib|fd|def))`,
	)

	reMissingFile = regexp.MustCompile("['`]([^'`]+?\\.(?:tex|bib|cls|sty|pdf|png|jpg))[`']")
	rePackageErr  = regexp.MustCompile(`^(?:Package|Class)\s+(\S+)\s+(?:Error|Warning):\s*(.+)`)
	reLatexError  = regexp.MustCompile(`^LaTeX Error:\s*(.+)`)
	reUndefinedCS = regexp.MustCompile(`\\([a-zA-Z@*]+)`)
)

type LaTeXError struct {
	Type    string `json:"type"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"`
	Source  string `json:"source,omitempty"`
}

func extractErrors(rawOutput, workDir string) []LaTeXError {
	output := unwrapLogLines(rawOutput)
	lines := strings.Split(output, "\n")

	var errors []LaTeXError

	fileStack := []string{"main.tex"}
	currentFile := func() string { return fileStack[len(fileStack)-1] }

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "(") {
			if m := reFileOpen.FindStringSubmatch(trimmed); m != nil {
				raw := m[1]
				if raw == "" {
					raw = m[2]
				}
				fileStack = append(fileStack, extractRelativePath(raw, workDir))
			}
		}
		if strings.HasSuffix(trimmed, ")") && !strings.Contains(trimmed, "(") && len(fileStack) > 1 {
			fileStack = fileStack[:len(fileStack)-1]
		}

		if m := reBoxError.FindStringSubmatch(trimmed); m != nil {
			startLine, _ := strconv.Atoi(m[2])
			errors = append(errors, LaTeXError{
				Type:    "warning",
				Line:    startLine,
				File:    currentFile(),
				Message: trimmed,
			})
			continue
		}

		if !strings.HasPrefix(trimmed, "!") {
			continue
		}
		if strings.Contains(trimmed, "==> Fatal error") {
			continue
		}

		e := LaTeXError{
			Type:    "error",
			Line:    -1,
			File:    currentFile(),
			Message: strings.TrimRight(strings.TrimSpace(strings.TrimPrefix(trimmed, "!")), "."),
		}

		if m := reLatexError.FindStringSubmatch(e.Message); m != nil {
			e.Message = strings.TrimRight(strings.TrimSpace(m[1]), ".")
		}

		if strings.Contains(e.Message, "Emergency stop") {
			e.Message = "LaTeX aborted - likely an unclosed environment or fatal syntax error"
		}

		for j := i + 1; j < i+20 && j < len(lines); j++ {
			next := strings.TrimSpace(lines[j])

			if m := reErrorLine.FindStringSubmatch(next); m != nil {
				ln, _ := strconv.Atoi(m[1])
				e.Line = ln

				snippet := strings.TrimSpace(m[2])
				if snippet != "" {
					e.Source = snippet
					if strings.Contains(e.Message, "Undefined control sequence") {
						if cs := reUndefinedCS.FindString(snippet); cs != "" {
							e.Message = "Undefined control sequence: " + cs
						}
					}
				}
				break
			}

			if strings.HasPrefix(next, "<*>") {
				path := strings.TrimSpace(strings.TrimPrefix(next, "<*>"))
				rel := extractRelativePath(path, workDir)
				if strings.HasSuffix(rel, ".tex") {
					e.File = rel
				}
				continue
			}

			if m := reMissingFile.FindStringSubmatch(next); m != nil {
				e.Source = "missing: " + m[1]
			}

			if m := rePackageErr.FindStringSubmatch(next); m != nil {
				e.Message = fmt.Sprintf("[%s] %s", m[1], strings.TrimSpace(m[2]))
			}

			if strings.HasPrefix(next, "!") {
				break
			}
		}

		errors = append(errors, e)
	}

	if len(errors) == 0 {
		errors = append(errors, LaTeXError{
			Type:    "error",
			Line:    -1,
			File:    "main.tex",
			Message: "Compilation failed - no specific error was identified; check the full logs",
		})
	}

	return deduplicateErrors(errors)
}

func unwrapLogLines(raw string) string {
	const maxWidth = 79

	isNewEntry := func(s string) bool {
		return len(s) == 0 ||
			strings.HasPrefix(s, "!") ||
			strings.HasPrefix(s, "l.") ||
			strings.HasPrefix(s, "Package") ||
			strings.HasPrefix(s, "LaTeX") ||
			strings.HasPrefix(s, "Class") ||
			strings.HasPrefix(s, "Overfull") ||
			strings.HasPrefix(s, "Underfull") ||
			strings.HasPrefix(s, " [") ||
			strings.HasPrefix(s, "Output written")
	}

	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	i := 0

	for i < len(lines) {
		line := lines[i]
		for len(line) == maxWidth && i+1 < len(lines) {
			next := lines[i+1]
			if isNewEntry(next) {
				break
			}
			line = line + next
			i++
		}
		out = append(out, line)
		i++
	}

	return strings.Join(out, "\n")
}

func deduplicateErrors(errs []LaTeXError) []LaTeXError {
	seen := make(map[string]bool)
	out := make([]LaTeXError, 0, len(errs))
	for _, e := range errs {
		key := fmt.Sprintf("%s:%d:%s", e.File, e.Line, e.Message)
		if !seen[key] {
			seen[key] = true
			out = append(out, e)
		}
	}
	return out
}
