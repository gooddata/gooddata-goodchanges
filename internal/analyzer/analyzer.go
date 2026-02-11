package analyzer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"goodchanges/internal/diff"
	"goodchanges/internal/rush"
	"goodchanges/internal/tsparse"
)

type Entrypoint struct {
	ExportPath string // e.g. ".", "./utils/*"
	SourceFile string // resolved source file path relative to project root
}

type AffectedExport struct {
	EntrypointPath string
	ExportNames    []string
}

// IsLibrary determines if a package is a library (transpiled) vs a bundled app.
func IsLibrary(pkg rush.PackageJSON) bool {
	if pkg.Types != "" {
		return true
	}
	if pkg.Exports != nil {
		return true
	}
	if pkg.Module != "" {
		return true
	}
	return false
}

// FindEntrypoints resolves all entrypoints from package.json to source files.
func FindEntrypoints(projectFolder string, pkg rush.PackageJSON) []Entrypoint {
	var entrypoints []Entrypoint

	if pkg.Exports != nil {
		eps := parseExportsField(pkg.Exports)
		for _, ep := range eps {
			resolved := resolveToSource(projectFolder, ep.SourceFile)
			if resolved != "" {
				entrypoints = append(entrypoints, Entrypoint{
					ExportPath: ep.ExportPath,
					SourceFile: resolved,
				})
			}
		}
	}

	if len(entrypoints) == 0 {
		for _, field := range []string{pkg.Main, pkg.Module, pkg.Browser, pkg.Types} {
			if field != "" {
				resolved := resolveToSource(projectFolder, field)
				if resolved != "" {
					entrypoints = append(entrypoints, Entrypoint{
						ExportPath: ".",
						SourceFile: resolved,
					})
					break
				}
			}
		}
	}

	return entrypoints
}

type importEdge struct {
	fromStem   string
	localNames []string
	origNames  []string
}

// AnalyzeLibraryPackage builds a full internal file dependency graph,
// then propagates taint from changed files through unlimited hops.
func AnalyzeLibraryPackage(projectFolder string, entrypoints []Entrypoint, diffText string, includeTypes bool) ([]AffectedExport, error) {
	fileDiffs := diff.ParseFiles(diffText)
	if len(fileDiffs) == 0 {
		return nil, nil
	}

	// Glob and parse ALL source files in the package
	allFiles, err := globSourceFiles(projectFolder)
	if err != nil {
		return nil, fmt.Errorf("globbing source files: %w", err)
	}

	fileAnalyses := make(map[string]*tsparse.FileAnalysis)
	for _, relPath := range allFiles {
		fullPath := filepath.Join(projectFolder, relPath)
		analysis, err := tsparse.ParseFile(fullPath)
		if err != nil {
			continue
		}
		stem := stripTSExtension(relPath)
		fileAnalyses[stem] = analysis
	}

	// Build import graph
	importGraph := make(map[string][]importEdge)

	for stem, analysis := range fileAnalyses {
		fileDir := filepath.Dir(stem + ".ts")
		for _, imp := range analysis.Imports {
			if !strings.HasPrefix(imp.Source, ".") {
				continue
			}
			resolvedStem := resolveImportSource(fileDir, imp.Source, projectFolder)
			if resolvedStem == "" {
				continue
			}
			var localNames, origNames []string
			for _, name := range imp.Names {
				if strings.HasPrefix(name, "*:") {
					localNames = append(localNames, name)
					origNames = append(origNames, "*")
				} else {
					localNames = append(localNames, name)
					origNames = append(origNames, name)
				}
			}
			importGraph[stem] = append(importGraph[stem], importEdge{
				fromStem:   resolvedStem,
				localNames: localNames,
				origNames:  origNames,
			})
		}
	}

	// Seed taint from diff
	tainted := make(map[string]map[string]bool)

	for _, fd := range fileDiffs {
		relToProject := strings.TrimPrefix(fd.Path, projectFolder+"/")
		ext := strings.ToLower(filepath.Ext(relToProject))
		if ext != ".ts" && ext != ".tsx" && ext != ".js" && ext != ".jsx" {
			continue
		}
		stem := stripTSExtension(relToProject)
		analysis := fileAnalyses[stem]
		if analysis == nil {
			continue
		}
		affected := tsparse.FindAffectedSymbols(analysis, fd.ChangedLines, includeTypes)
		if len(affected) > 0 {
			tainted[stem] = make(map[string]bool)
			for _, s := range affected {
				tainted[stem][s] = true
			}
		}
	}

	if len(tainted) == 0 {
		return nil, nil
	}

	// Build reverse import graph
	reverseImports := make(map[string][]string)
	for stem, edges := range importGraph {
		for _, edge := range edges {
			reverseImports[edge.fromStem] = append(reverseImports[edge.fromStem], stem)
		}
	}

	// Propagate taint — BFS, unlimited hops
	queue := make([]string, 0, len(tainted))
	for stem := range tainted {
		queue = append(queue, stem)
	}

	for len(queue) > 0 {
		currentStem := queue[0]
		queue = queue[1:]
		currentTainted := tainted[currentStem]

		for _, importerStem := range reverseImports[currentStem] {
			importerAnalysis := fileAnalyses[importerStem]
			if importerAnalysis == nil {
				continue
			}

			var taintedLocalNames []string
			for _, edge := range importGraph[importerStem] {
				if edge.fromStem != currentStem {
					continue
				}
				for i, origName := range edge.origNames {
					if origName == "*" {
						if len(currentTainted) > 0 {
							taintedLocalNames = append(taintedLocalNames, edge.localNames[i])
						}
					} else if currentTainted[origName] {
						taintedLocalNames = append(taintedLocalNames, edge.localNames[i])
					}
				}
			}

			if len(taintedLocalNames) == 0 {
				continue
			}

			newlyTainted := findTaintedSymbolsByUsage(importerAnalysis, taintedLocalNames)

			importerDir := filepath.Dir(importerStem + ".ts")
			for _, exp := range importerAnalysis.Exports {
				if exp.Source == "" {
					for _, tln := range taintedLocalNames {
						cleanName := tln
						if strings.HasPrefix(cleanName, "*:") {
							cleanName = strings.TrimPrefix(cleanName, "*:")
						}
						if exp.LocalName == cleanName {
							newlyTainted = append(newlyTainted, exp.Name)
						}
					}
				} else {
					reExpStem := resolveImportSource(importerDir, exp.Source, projectFolder)
					if reExpStem == currentStem {
						if exp.IsStar {
							for name := range currentTainted {
								newlyTainted = append(newlyTainted, name)
							}
						} else if currentTainted[exp.LocalName] {
							newlyTainted = append(newlyTainted, exp.Name)
						}
					}
				}
			}

			if len(newlyTainted) == 0 {
				continue
			}

			if tainted[importerStem] == nil {
				tainted[importerStem] = make(map[string]bool)
			}
			addedNew := false
			for _, name := range newlyTainted {
				if !tainted[importerStem][name] {
					tainted[importerStem][name] = true
					addedNew = true
				}
			}
			if addedNew {
				queue = append(queue, importerStem)
			}
		}
	}

	// Check entrypoints for tainted exports
	var result []AffectedExport

	for _, ep := range entrypoints {
		epStem := stripTSExtension(ep.SourceFile)
		epAnalysis := fileAnalyses[epStem]
		if epAnalysis == nil {
			continue
		}

		var affectedNames []string
		epDir := filepath.Dir(ep.SourceFile)

		for _, exp := range epAnalysis.Exports {
			if exp.IsTypeOnly && !includeTypes {
				continue
			}

			if exp.Source == "" {
				if tainted[epStem][exp.LocalName] {
					affectedNames = append(affectedNames, exp.Name)
				}
				continue
			}

			resolvedStem := resolveImportSource(epDir, exp.Source, projectFolder)
			if resolvedStem == "" {
				continue
			}
			srcTainted := tainted[resolvedStem]
			if srcTainted == nil {
				continue
			}

			if exp.IsStar {
				for name := range srcTainted {
					affectedNames = append(affectedNames, name)
				}
			} else if srcTainted[exp.LocalName] {
				affectedNames = append(affectedNames, exp.Name)
			}
		}

		if len(affectedNames) > 0 {
			seen := make(map[string]bool)
			var deduped []string
			for _, n := range affectedNames {
				if !seen[n] {
					seen[n] = true
					deduped = append(deduped, n)
				}
			}
			result = append(result, AffectedExport{
				EntrypointPath: ep.ExportPath,
				ExportNames:    deduped,
			})
		}
	}

	return result, nil
}

func findTaintedSymbolsByUsage(analysis *tsparse.FileAnalysis, taintedNames []string) []string {
	if analysis.SourceFile == nil || len(taintedNames) == 0 {
		return nil
	}

	taintSet := make(map[string]bool)
	for _, n := range taintedNames {
		clean := n
		if strings.HasPrefix(clean, "*:") {
			clean = strings.TrimPrefix(clean, "*:")
		}
		taintSet[clean] = true
	}

	sourceText := analysis.SourceFile.Text()
	lineMap := analysis.SourceFile.ECMALineMap()

	var result []string
	for _, sym := range analysis.Symbols {
		bodyText := tsparse.ExtractTextForLines(sourceText, lineMap, sym.StartLine, sym.EndLine)
		for tName := range taintSet {
			if strings.Contains(bodyText, tName) {
				result = append(result, sym.Name)
				break
			}
		}
	}
	return result
}

func globSourceFiles(projectFolder string) ([]string, error) {
	var files []string
	err := filepath.Walk(projectFolder, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := filepath.Base(path)
			if base == "node_modules" || base == ".git" || base == "dist" || base == "esm" || base == "lib" || base == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".ts" || ext == ".tsx" || ext == ".js" || ext == ".jsx" {
			rel, _ := filepath.Rel(projectFolder, path)
			files = append(files, rel)
		}
		return nil
	})
	return files, err
}
