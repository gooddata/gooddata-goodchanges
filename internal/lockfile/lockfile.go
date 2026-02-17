package lockfile

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type depLineInfo struct {
	projectFolder string
	depName       string
}

// FindDepAffectedProjects parses a pnpm-lock.yaml and its diff to find
// which projects had direct dependency version changes.
// Returns a map of project folder → set of changed dependency package names.
// Workspace deps (version: link:...) are excluded as they're handled by the Rush dep graph.
// The subspace parameter is used to resolve importer paths (they're relative to common/temp/{subspace}/).
// TODO: handle transitive dep changes (a dep-of-a-dep changing without the direct dep version changing).
func FindDepAffectedProjects(lockfilePath string, subspace string, diffText string) map[string]map[string]bool {
	if diffText == "" {
		return nil
	}

	content, err := os.ReadFile(lockfilePath)
	if err != nil {
		return nil
	}

	importerBase := filepath.Join("common", "temp", subspace)
	lineMap, workspaceDeps := buildImporterDepLineMap(string(content), importerBase)

	changedLines := parseDiffChangedLines(diffText)

	result := make(map[string]map[string]bool)
	for _, line := range changedLines {
		info, ok := lineMap[line]
		if !ok || info.projectFolder == "" || info.depName == "" {
			continue
		}
		if workspaceDeps[info.projectFolder] != nil && workspaceDeps[info.projectFolder][info.depName] {
			continue
		}
		if result[info.projectFolder] == nil {
			result[info.projectFolder] = make(map[string]bool)
		}
		result[info.projectFolder][info.depName] = true
	}
	return result
}

// buildImporterDepLineMap reads a pnpm-lock.yaml and returns:
// - lineMap: line number (1-based) → project folder + dep name for lines in the importers section
// - workspaceDeps: project folder → dep name → true (for workspace deps identified by version: link:...)
func buildImporterDepLineMap(content string, importerBase string) (map[int]depLineInfo, map[string]map[string]bool) {
	lineMap := make(map[int]depLineInfo)
	workspaceDeps := make(map[string]map[string]bool)
	lines := strings.Split(content, "\n")

	inImporters := false
	currentImporter := ""
	inDepSection := false
	currentDepName := ""

	for i, line := range lines {
		lineNum := i + 1
		indent := countLeadingSpaces(line)
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Top-level section (no indent)
		if indent == 0 {
			if strings.HasPrefix(line, "importers:") {
				inImporters = true
				currentImporter = ""
				inDepSection = false
				currentDepName = ""
			} else if inImporters {
				break // Left importers section
			}
			continue
		}

		if !inImporters {
			continue
		}

		switch {
		case indent == 2:
			// Importer path (e.g. "  ../../../sdk/libs/sdk-ui-kit:")
			if !strings.HasSuffix(trimmed, ":") {
				continue
			}
			rawPath := strings.TrimSuffix(trimmed, ":")
			rawPath = strings.Trim(rawPath, "'\"")
			if rawPath == "." {
				currentImporter = ""
			} else {
				resolved := filepath.Clean(filepath.Join(importerBase, rawPath))
				if rel, err := filepath.Rel(".", resolved); err == nil {
					currentImporter = rel
				} else {
					currentImporter = resolved
				}
			}
			inDepSection = false
			currentDepName = ""

		case indent == 4:
			// Section header (dependencies, devDependencies, optionalDependencies)
			if currentImporter == "" {
				continue
			}
			if trimmed == "dependencies:" || trimmed == "devDependencies:" || trimmed == "optionalDependencies:" {
				inDepSection = true
			} else {
				inDepSection = false
			}
			currentDepName = ""

		case indent == 6:
			// Dep name (e.g. "      react:" or "      '@gooddata/sdk-model':")
			if !inDepSection || currentImporter == "" {
				continue
			}
			if !strings.HasSuffix(trimmed, ":") {
				continue
			}
			currentDepName = strings.TrimSuffix(trimmed, ":")
			currentDepName = strings.Trim(currentDepName, "'\"")
			// Map the dep name line too (in case it's added/removed in the diff)
			lineMap[lineNum] = depLineInfo{
				projectFolder: currentImporter,
				depName:       currentDepName,
			}

		case indent >= 8:
			// Dep property (specifier, version)
			if !inDepSection || currentImporter == "" || currentDepName == "" {
				continue
			}
			lineMap[lineNum] = depLineInfo{
				projectFolder: currentImporter,
				depName:       currentDepName,
			}
			// Detect workspace deps by version: link:...
			if strings.HasPrefix(trimmed, "version:") {
				version := strings.TrimSpace(strings.TrimPrefix(trimmed, "version:"))
				version = strings.Trim(version, "'\"")
				if strings.HasPrefix(version, "link:") {
					if workspaceDeps[currentImporter] == nil {
						workspaceDeps[currentImporter] = make(map[string]bool)
					}
					workspaceDeps[currentImporter][currentDepName] = true
				}
			}
		}
	}

	return lineMap, workspaceDeps
}

func countLeadingSpaces(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != ' ' {
			return i
		}
	}
	return len(s)
}

// ParseLockfileVersion extracts the lockfileVersion value from pnpm-lock.yaml content
// using proper YAML parsing.
func ParseLockfileVersion(content []byte) string {
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return ""
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return ""
	}
	mapping := doc.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i < len(mapping.Content)-1; i += 2 {
		if mapping.Content[i].Value == "lockfileVersion" {
			return mapping.Content[i+1].Value
		}
	}
	return ""
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
