package lockfile

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// FindDepAffectedProjects parses a pnpm-lock.yaml and its diff to find
// which projects had direct dependency version changes.
// Returns a set of project folders (resolved from importer paths).
// The subspace parameter is used to resolve importer paths (they're relative to common/temp/{subspace}/).
// TODO: handle transitive dep changes (a dep-of-a-dep changing without the direct dep version changing).
// TODO: instead of tainting all exports of a dep-affected library, find imports of the changed dep,
// taint all usages of those imports, then trace up to the library's exports.
func FindDepAffectedProjects(lockfilePath string, subspace string, diffText string) map[string]bool {
	if diffText == "" {
		return nil
	}

	// Read the current lockfile to build a line→importer map
	content, err := os.ReadFile(lockfilePath)
	if err != nil {
		return nil
	}

	// Importer paths in pnpm-lock.yaml are relative to common/temp/{subspace}/
	importerBase := filepath.Join("common", "temp", subspace)
	importerAtLine := buildImporterLineMap(string(content), importerBase)

	// Parse diff hunks to find changed line numbers (new-file side)
	changedLines := parseDiffChangedLines(diffText)

	// Map changed lines to importers
	result := make(map[string]bool)
	for _, line := range changedLines {
		if importer, ok := importerAtLine[line]; ok && importer != "" {
			result[importer] = true
		}
	}
	return result
}

// buildImporterLineMap reads a pnpm-lock.yaml and returns a map of
// line number (1-based) → resolved project folder for lines in the importers section.
func buildImporterLineMap(content string, importerBase string) map[int]string {
	result := make(map[int]string)
	lines := strings.Split(content, "\n")

	inImporters := false
	currentImporter := ""

	for i, line := range lines {
		lineNum := i + 1

		// Top-level section detection (no indent)
		if len(line) > 0 && line[0] != ' ' && line[0] != '#' {
			if strings.HasPrefix(line, "importers:") {
				inImporters = true
				continue
			} else if inImporters {
				inImporters = false
				currentImporter = ""
			}
			continue
		}

		if !inImporters {
			continue
		}

		// Importer path: exactly 2 spaces indent, ends with ':'
		// e.g. "  ../../../sdk/libs/sdk-ui-kit:"
		if len(line) > 2 && line[0] == ' ' && line[1] == ' ' && line[2] != ' ' && strings.HasSuffix(strings.TrimSpace(line), ":") {
			rawPath := strings.TrimSpace(line)
			rawPath = strings.TrimSuffix(rawPath, ":")
			if rawPath == "." {
				currentImporter = ""
				continue
			}
			// Resolve relative to the pnpm install base (common/temp/{subspace}/)
			resolved := filepath.Clean(filepath.Join(importerBase, rawPath))
			if rel, err := filepath.Rel(".", resolved); err == nil {
				currentImporter = rel
			} else {
				currentImporter = resolved
			}
			continue
		}

		// Lines deeper than importer path belong to current importer
		if currentImporter != "" && len(line) > 2 && line[0] == ' ' {
			result[lineNum] = currentImporter
		}
	}
	return result
}

// parseDiffChangedLines extracts the new-file line numbers of changed lines from a unified diff.
func parseDiffChangedLines(diffText string) []int {
	var result []int
	lines := strings.Split(diffText, "\n")

	newLineNum := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			plusIdx := strings.Index(line, "+")
			if plusIdx < 0 {
				continue
			}
			rest := line[plusIdx+1:]
			spaceIdx := strings.Index(rest, " ")
			if spaceIdx < 0 {
				continue
			}
			rangeStr := rest[:spaceIdx]
			parts := strings.SplitN(rangeStr, ",", 2)
			start, err := strconv.Atoi(parts[0])
			if err != nil {
				continue
			}
			newLineNum = start - 1
			continue
		}

		if newLineNum == 0 {
			continue
		}

		if strings.HasPrefix(line, "-") {
			continue
		}

		newLineNum++

		if strings.HasPrefix(line, "+") {
			result = append(result, newLineNum)
		}
	}
	return result
}
