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

// CollectEntrypointExports parses an entrypoint file and returns all export names.
func CollectEntrypointExports(projectFolder string, ep Entrypoint) []string {
	fullPath := filepath.Join(projectFolder, ep.SourceFile)
	analysis, err := tsparse.ParseFile(fullPath)
	if err != nil {
		return nil
	}
	var names []string
	seen := make(map[string]bool)
	for _, exp := range analysis.Exports {
		name := exp.Name
		if name == "*" {
			continue
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// HasTaintedImports checks if any source file in the given folder imports
// tainted symbols from the upstreamTaint map. Used for app-like packages
// (e.g. e2e scenario apps) where we don't need to trace to entrypoint exports,
// just detect whether any tainted dependency is actually imported.
func HasTaintedImports(folder string, upstreamTaint map[string]map[string]bool, ignoreCfg *rush.IgnoreConfig) bool {
	if len(upstreamTaint) == 0 {
		return false
	}
	allFiles, err := globSourceFiles(folder)
	if err != nil {
		return false
	}
	for _, relPath := range allFiles {
		if ignoreCfg.IsIgnored(relPath) {
			continue
		}
		fullPath := filepath.Join(folder, relPath)
		analysis, err := tsparse.ParseFile(fullPath)
		if err != nil {
			continue
		}
		for _, imp := range analysis.Imports {
			if strings.HasPrefix(imp.Source, ".") {
				continue
			}
			affectedNames, ok := upstreamTaint[imp.Source]
			if !ok || len(affectedNames) == 0 {
				continue
			}
			if len(imp.Names) == 0 {
				// Unassigned import from tainted package
				return true
			}
			for _, name := range imp.Names {
				if strings.HasPrefix(name, "*:") {
					// Namespace import — any taint means affected
					return true
				}
				if affectedNames[name] {
					return true
				}
			}
		}
	}
	return false
}

type importEdge struct {
	fromStem     string
	localNames   []string
	origNames    []string
	isSideEffect bool // true for unassigned imports like import "./foo"
}

// AnalyzeLibraryPackage builds a full internal file dependency graph,
// then propagates taint from changed files and upstream dependencies through unlimited hops.
// upstreamTaint maps import specifiers (e.g. "@gooddata/sdk-ui-kit") to sets of affected export names.
// taintedExternalDeps is a set of external package names that changed in the lockfile — all imports
// from these packages are considered tainted (since we don't know which exports changed).
func AnalyzeLibraryPackage(projectFolder string, entrypoints []Entrypoint, diffText string, includeTypes bool, upstreamTaint map[string]map[string]bool, taintedExternalDeps map[string]bool) ([]AffectedExport, error) {
	fileDiffs := diff.ParseFiles(diffText)

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

	// Build import graph (relative imports only)
	// TODO: handle SCSS/CSS/LESS change detection and taint propagation, in three steps:
	//   Step 1 (basic): if any SCSS/CSS file changes in a package, taint ALL CSS/SCSS files
	//     in that package. Any TS/CSS import of a tainted CSS file becomes tainted (unassigned
	//     import → taint all exports in the importing file).
	//   Step 2 (CSS modules): handle CSS module files properly — the import is unassigned
	//     (import "./styles.css"), so taint the importing TS file and all its exports.
	//   Step 3 (granular, if possible): if an SCSS file changes and it maps to a specific CSS
	//     output file, only that CSS file becomes tainted (instead of all CSS files in the package).
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
				fromStem:     resolvedStem,
				localNames:   localNames,
				origNames:    origNames,
				isSideEffect: len(imp.Names) == 0,
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
			if tainted[stem] == nil {
				tainted[stem] = make(map[string]bool)
			}
			for _, s := range affected {
				tainted[stem][s] = true
			}
		}
	}

	// Seed taint from upstream dependencies (cross-package propagation)
	if len(upstreamTaint) > 0 {
		for stem, analysis := range fileAnalyses {
			for _, imp := range analysis.Imports {
				if strings.HasPrefix(imp.Source, ".") {
					continue
				}
				affectedNames, ok := upstreamTaint[imp.Source]
				if !ok || len(affectedNames) == 0 {
					continue
				}
				if len(imp.Names) == 0 {
					// Unassigned import from tainted upstream dep: taint all symbols
					if tainted[stem] == nil {
						tainted[stem] = make(map[string]bool)
					}
					for _, sym := range analysis.Symbols {
						tainted[stem][sym.Name] = true
					}
					continue
				}
				var taintedLocalNames []string
				for _, name := range imp.Names {
					if strings.HasPrefix(name, "*:") {
						// Namespace import — any upstream taint means the namespace is tainted
						taintedLocalNames = append(taintedLocalNames, name)
					} else if affectedNames[name] {
						taintedLocalNames = append(taintedLocalNames, name)
					}
				}
				if len(taintedLocalNames) == 0 {
					continue
				}
				// Find which symbols in this file use the tainted imports
				usageTainted := findTaintedSymbolsByUsage(analysis, taintedLocalNames)
				// Also check if any tainted local names are directly re-exported
				for _, exp := range analysis.Exports {
					if exp.Source == "" {
						for _, tln := range taintedLocalNames {
							cleanName := tln
							if strings.HasPrefix(cleanName, "*:") {
								cleanName = strings.TrimPrefix(cleanName, "*:")
							}
							if exp.LocalName == cleanName {
								usageTainted = append(usageTainted, exp.Name)
							}
						}
					}
				}
				if len(usageTainted) > 0 {
					if tainted[stem] == nil {
						tainted[stem] = make(map[string]bool)
					}
					for _, s := range usageTainted {
						tainted[stem][s] = true
					}
				}
			}
		}
	}

	// Seed taint from tainted external dependencies (lockfile dep changes).
	// All imports from these deps are considered tainted since we can't know which
	// specific exports of the external package changed.
	if len(taintedExternalDeps) > 0 {
		for stem, analysis := range fileAnalyses {
			// Check imports from tainted external deps
			for _, imp := range analysis.Imports {
				if strings.HasPrefix(imp.Source, ".") {
					continue
				}
				if !isFromTaintedDep(imp.Source, taintedExternalDeps) {
					continue
				}
				if tainted[stem] == nil {
					tainted[stem] = make(map[string]bool)
				}
				if len(imp.Names) == 0 {
					// Unassigned import from tainted external dep: taint all symbols
					for _, sym := range analysis.Symbols {
						tainted[stem][sym.Name] = true
					}
				} else {
					// All imported names are tainted — find symbols that use them
					usageTainted := findTaintedSymbolsByUsage(analysis, imp.Names)
					for _, s := range usageTainted {
						tainted[stem][s] = true
					}
					// Check if any imported names are directly re-exported
					for _, exp := range analysis.Exports {
						if exp.Source == "" {
							for _, name := range imp.Names {
								cleanName := name
								if strings.HasPrefix(cleanName, "*:") {
									cleanName = strings.TrimPrefix(cleanName, "*:")
								}
								if exp.LocalName == cleanName {
									tainted[stem][exp.Name] = true
								}
							}
						}
					}
				}
			}
			// Check re-exports from tainted external deps
			for _, exp := range analysis.Exports {
				if exp.Source == "" || strings.HasPrefix(exp.Source, ".") {
					continue
				}
				if !isFromTaintedDep(exp.Source, taintedExternalDeps) {
					continue
				}
				if tainted[stem] == nil {
					tainted[stem] = make(map[string]bool)
				}
				if exp.IsStar {
					// export * from tainted dep: can't enumerate external exports,
					// use "*" marker so all consumers of this file are tainted
					tainted[stem]["*"] = true
				} else {
					tainted[stem][exp.Name] = true
				}
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

			// Check for side-effect (unassigned) imports and named imports from the tainted source
			hasSideEffectImport := false
			var taintedLocalNames []string
			for _, edge := range importGraph[importerStem] {
				if edge.fromStem != currentStem {
					continue
				}
				if edge.isSideEffect {
					hasSideEffectImport = true
					continue
				}
				for i, origName := range edge.origNames {
					if origName == "*" {
						if len(currentTainted) > 0 {
							taintedLocalNames = append(taintedLocalNames, edge.localNames[i])
						}
					} else if currentTainted[origName] || currentTainted["*"] {
						taintedLocalNames = append(taintedLocalNames, edge.localNames[i])
					}
				}
			}

			if !hasSideEffectImport && len(taintedLocalNames) == 0 {
				continue
			}

			var newlyTainted []string

			// Unassigned import from tainted file: all symbols in this file are tainted
			if hasSideEffectImport && len(currentTainted) > 0 {
				for _, sym := range importerAnalysis.Symbols {
					newlyTainted = append(newlyTainted, sym.Name)
				}
			}

			// Named imports: find symbols that use the tainted imports
			if len(taintedLocalNames) > 0 {
				usageTainted := findTaintedSymbolsByUsage(importerAnalysis, taintedLocalNames)
				newlyTainted = append(newlyTainted, usageTainted...)
			}

			// Handle re-exports
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
						} else if currentTainted[exp.LocalName] || currentTainted["*"] {
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
				if tainted[epStem][exp.LocalName] || tainted[epStem]["*"] {
					affectedNames = append(affectedNames, exp.Name)
				}
				continue
			}

			// Re-exports from tainted external deps (non-relative, non-internal)
			if !strings.HasPrefix(exp.Source, ".") {
				if len(taintedExternalDeps) > 0 && isFromTaintedDep(exp.Source, taintedExternalDeps) {
					if exp.IsStar {
						// TODO: can't enumerate external dep exports for star re-exports at entrypoint level.
						// For now these are handled via the "*" marker in the seeding phase.
					} else {
						affectedNames = append(affectedNames, exp.Name)
					}
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
			} else if srcTainted[exp.LocalName] || srcTainted["*"] {
				affectedNames = append(affectedNames, exp.Name)
			}
		}

		if len(affectedNames) > 0 {
			seen := make(map[string]bool)
			var deduped []string
			for _, n := range affectedNames {
				if n == "*" {
					continue // internal marker, not a real export name
				}
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

// isFromTaintedDep checks if an import source matches any tainted external dep name.
// Handles both exact matches (e.g. "react") and subpath imports (e.g. "react/jsx-runtime"),
// as well as scoped packages (e.g. "@emotion/react", "@emotion/react/utils").
func isFromTaintedDep(importSource string, taintedDeps map[string]bool) bool {
	for depName := range taintedDeps {
		if importSource == depName || strings.HasPrefix(importSource, depName+"/") {
			return true
		}
	}
	return false
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
