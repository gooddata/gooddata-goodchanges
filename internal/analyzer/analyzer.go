package analyzer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"goodchanges/internal/git"
	"goodchanges/internal/rush"
	"goodchanges/internal/tsparse"
)

// Debug enables verbose logging to stderr when set to true (via --debug flag).
var Debug bool

func debugf(format string, args ...interface{}) {
	if Debug {
		fmt.Fprintf(os.Stderr, "[DEBUG] "+format+"\n", args...)
	}
}

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
// Instead of line-range heuristics, this version diffs OLD and NEW ASTs per symbol.
// mergeBase is the git commit to compare against. changedFiles is the full list of changed
// file paths (repo-relative) — only files within projectFolder are considered.
// upstreamTaint maps import specifiers (e.g. "@gooddata/sdk-ui-kit") to sets of affected export names.
// taintedExternalDeps is a set of external package names that changed in the lockfile.
func AnalyzeLibraryPackage(projectFolder string, entrypoints []Entrypoint, mergeBase string, changedFiles []string, includeTypes bool, upstreamTaint map[string]map[string]bool, taintedExternalDeps map[string]bool) ([]AffectedExport, error) {
	// Filter changed files to those within this project
	var projectChangedFiles []string
	for _, f := range changedFiles {
		if strings.HasPrefix(f, projectFolder+"/") {
			projectChangedFiles = append(projectChangedFiles, f)
		}
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

		// Also treat re-exports (export { X } from "./foo" / export * from "./foo")
		// as import edges — barrel files have no import statements but still depend
		// on the files they re-export from.
		for _, exp := range analysis.Exports {
			if exp.Source == "" || !strings.HasPrefix(exp.Source, ".") {
				continue
			}
			resolvedStem := resolveImportSource(fileDir, exp.Source, projectFolder)
			if resolvedStem == "" {
				continue
			}
			// Check if we already have an import edge to this source
			// (to avoid duplicating edges when a file both imports and re-exports)
			alreadyHasEdge := false
			for _, edge := range importGraph[stem] {
				if edge.fromStem == resolvedStem {
					alreadyHasEdge = true
					break
				}
			}
			if alreadyHasEdge {
				continue
			}
			// Create a synthetic import edge for the re-export
			var localNames, origNames []string
			if exp.IsStar {
				// export * from "./foo" — treat as namespace-like (any taint propagates)
				localNames = append(localNames, "*:__reexport__")
				origNames = append(origNames, "*")
			} else {
				localNames = append(localNames, exp.LocalName)
				origNames = append(origNames, exp.LocalName)
			}
			importGraph[stem] = append(importGraph[stem], importEdge{
				fromStem:   resolvedStem,
				localNames: localNames,
				origNames:  origNames,
			})
		}
	}

	// Seed taint from diff — AST diffing approach.
	// For each changed file, fetch the OLD version from git, parse both OLD and NEW ASTs,
	// compare each symbol's body text to determine which symbols actually changed.
	// Distinguishes runtime changes from type-only changes (e.g. adding `as Type`).
	tainted := make(map[string]map[string]bool)

	debugf("=== Seeding taint from AST diff for %s ===", projectFolder)
	debugf("  Changed files in project: %d", len(projectChangedFiles))
	for _, changedFile := range projectChangedFiles {
		relToProject := strings.TrimPrefix(changedFile, projectFolder+"/")
		ext := strings.ToLower(filepath.Ext(relToProject))
		if ext != ".ts" && ext != ".tsx" && ext != ".js" && ext != ".jsx" {
			debugf("  skipping non-TS file: %s", relToProject)
			continue
		}
		stem := stripTSExtension(relToProject)
		newAnalysis := fileAnalyses[stem]
		if newAnalysis == nil {
			debugf("  WARNING: no analysis found for stem %q", stem)
			continue
		}

		// Get old file content from git
		oldContent, err := git.ShowFile(mergeBase, changedFile)
		if err != nil {
			oldContent = ""
		}

		var oldAnalysis *tsparse.FileAnalysis
		if oldContent != "" {
			oldAnalysis, _ = tsparse.ParseContent(oldContent, changedFile)
		}

		affected := findAffectedSymbolsByASTDiff(oldAnalysis, newAnalysis, oldContent, includeTypes)
		debugf("  %s: affected symbols (AST diff): %v", stem, affected)

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

	debugf("=== Initial taint map (after diff seed) ===")
	for stem, names := range tainted {
		nameList := make([]string, 0, len(names))
		for n := range names {
			nameList = append(nameList, n)
		}
		debugf("  %s: %v", stem, nameList)
	}

	if len(tainted) == 0 {
		debugf("  (empty — no taint seeded from diff)")
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
	debugf("=== Starting BFS taint propagation ===")
	queue := make([]string, 0, len(tainted))
	for stem := range tainted {
		queue = append(queue, stem)
	}

	for len(queue) > 0 {
		currentStem := queue[0]
		queue = queue[1:]
		currentTainted := tainted[currentStem]

		debugf("  BFS visiting: %s (tainted: %v)", currentStem, mapKeys(currentTainted))
		debugf("    reverse importers: %v", reverseImports[currentStem])

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
				debugf("    → %s: no tainted imports from %s (skipping)", importerStem, currentStem)
				continue
			}

			debugf("    → %s: sideEffect=%v taintedLocalNames=%v", importerStem, hasSideEffectImport, taintedLocalNames)

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

			// Intra-file propagation: if symbol A is newly tainted and symbol B
			// references A in its body, B is also tainted. Repeat until stable.
			if len(newlyTainted) > 0 && importerAnalysis.SourceFile != nil {
				taintedSet := make(map[string]bool)
				for _, n := range newlyTainted {
					taintedSet[n] = true
				}
				sourceText := importerAnalysis.SourceFile.Text()
				lineMap := importerAnalysis.SourceFile.ECMALineMap()
				changed := true
				for changed {
					changed = false
					for _, sym := range importerAnalysis.Symbols {
						if taintedSet[sym.Name] {
							continue
						}
						bodyText := tsparse.ExtractTextForLines(sourceText, lineMap, sym.StartLine, sym.EndLine)
						for tName := range taintedSet {
							if strings.Contains(bodyText, tName) {
								taintedSet[sym.Name] = true
								newlyTainted = append(newlyTainted, sym.Name)
								changed = true
								debugf("    → %s: %s tainted via intra-file dep on %s", importerStem, sym.Name, tName)
								break
							}
						}
					}
				}
			}

			if len(newlyTainted) == 0 {
				debugf("    → %s: re-export/usage check found nothing new", importerStem)
				continue
			}

			debugf("    → %s: newly tainted symbols: %v", importerStem, newlyTainted)

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
	debugf("=== Final taint map (after BFS) ===")
	for stem, names := range tainted {
		nameList := make([]string, 0, len(names))
		for n := range names {
			nameList = append(nameList, n)
		}
		debugf("  %s: %v", stem, nameList)
	}

	var result []AffectedExport

	for _, ep := range entrypoints {
		epStem := stripTSExtension(ep.SourceFile)
		epAnalysis := fileAnalyses[epStem]
		if epAnalysis == nil {
			continue
		}

		debugf("=== Checking entrypoint %q (stem=%s) ===", ep.ExportPath, epStem)
		debugf("  Exports in entrypoint:")
		for _, exp := range epAnalysis.Exports {
			debugf("    name=%q local=%q source=%q typeOnly=%v star=%v", exp.Name, exp.LocalName, exp.Source, exp.IsTypeOnly, exp.IsStar)
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
				debugf("    export %q from %q: could not resolve stem", exp.Name, exp.Source)
				continue
			}
			srcTainted := tainted[resolvedStem]
			if srcTainted == nil {
				debugf("    export %q from %q → stem %q: not tainted", exp.Name, exp.Source, resolvedStem)
				continue
			}

			debugf("    export %q from %q → stem %q: tainted=%v star=%v localName=%q",
				exp.Name, exp.Source, resolvedStem, mapKeys(srcTainted), exp.IsStar, exp.LocalName)

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

func mapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
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
