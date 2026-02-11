package analyzer_old

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func parseExportsField(exports json.RawMessage) []Entrypoint {
	var result []Entrypoint

	var str string
	if json.Unmarshal(exports, &str) == nil {
		result = append(result, Entrypoint{ExportPath: ".", SourceFile: str})
		return result
	}

	var obj map[string]json.RawMessage
	if json.Unmarshal(exports, &obj) == nil {
		for key, val := range obj {
			if strings.Contains(key, "*") {
				continue
			}
			resolved := resolveExportValue(val)
			if resolved != "" {
				result = append(result, Entrypoint{ExportPath: key, SourceFile: resolved})
			}
		}
	}

	return result
}

func resolveExportValue(raw json.RawMessage) string {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}

	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil {
		for _, key := range []string{"types", "import", "default", "require"} {
			if v, ok := obj[key]; ok {
				resolved := resolveExportValue(v)
				if resolved != "" {
					return resolved
				}
			}
		}
	}
	return ""
}

func resolveToSource(projectFolder string, builtPath string) string {
	builtPath = strings.TrimPrefix(builtPath, "./")

	var candidates []string
	for _, prefix := range []string{"esm/", "dist/", "lib/", "build/"} {
		if strings.HasPrefix(builtPath, prefix) {
			candidates = append(candidates, "src/"+strings.TrimPrefix(builtPath, prefix))
		}
	}
	candidates = append(candidates, builtPath)

	for _, candidate := range candidates {
		base := candidate
		if strings.HasSuffix(base, ".d.mts") {
			base = strings.TrimSuffix(base, ".d.mts")
		} else if strings.HasSuffix(base, ".d.ts") {
			base = strings.TrimSuffix(base, ".d.ts")
		} else if strings.HasSuffix(base, ".mjs") {
			base = strings.TrimSuffix(base, ".mjs")
		} else if strings.HasSuffix(base, ".cjs") {
			base = strings.TrimSuffix(base, ".cjs")
		} else if strings.HasSuffix(base, ".js") {
			base = strings.TrimSuffix(base, ".js")
		}

		for _, ext := range []string{".ts", ".tsx", ".js", ".jsx"} {
			tryPath := filepath.Join(projectFolder, base+ext)
			if _, err := os.Stat(tryPath); err == nil {
				return base + ext
			}
		}
		for _, ext := range []string{".ts", ".tsx"} {
			tryPath := filepath.Join(projectFolder, base, "index"+ext)
			if _, err := os.Stat(tryPath); err == nil {
				return filepath.Join(base, "index"+ext)
			}
		}
		tryPath := filepath.Join(projectFolder, candidate)
		if _, err := os.Stat(tryPath); err == nil {
			return candidate
		}
	}

	return ""
}

func resolveImportSource(fromDir string, source string, projectFolder string) string {
	if !strings.HasPrefix(source, ".") {
		return ""
	}
	resolved := resolveImportToFile(fromDir, source, projectFolder)
	if resolved == "" {
		return ""
	}
	return stripTSExtension(resolved)
}

func resolveImportToFile(fromDir string, source string, projectFolder string) string {
	base := strings.TrimSuffix(source, ".js")
	base = strings.TrimSuffix(base, ".jsx")
	relPath := filepath.Join(fromDir, base)

	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx"} {
		tryPath := filepath.Join(projectFolder, relPath+ext)
		if _, err := os.Stat(tryPath); err == nil {
			return relPath + ext
		}
	}
	for _, ext := range []string{".ts", ".tsx"} {
		tryPath := filepath.Join(projectFolder, relPath, "index"+ext)
		if _, err := os.Stat(tryPath); err == nil {
			return filepath.Join(relPath, "index"+ext)
		}
	}
	return ""
}

func stripTSExtension(path string) string {
	for _, ext := range []string{".tsx", ".ts", ".jsx", ".js", ".d.ts", ".d.mts"} {
		if strings.HasSuffix(path, ext) {
			return strings.TrimSuffix(path, ext)
		}
	}
	return path
}
