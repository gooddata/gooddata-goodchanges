package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"goodchanges/internal/analyzer"
	"goodchanges/internal/git"
	"goodchanges/internal/lockfile"
	"goodchanges/internal/rush"
)

var flagIncludeTypes bool
var flagIncludeCSS bool
var flagLog bool
var flagDebug bool

// logf prints to stdout only when --log or --debug is set.
func logf(format string, args ...interface{}) {
	if flagLog {
		fmt.Printf(format, args...)
	}
}

func main() {
	flag.BoolVar(&flagIncludeTypes, "include-types", false, "Include type/interface-only changes in analysis")
	flag.BoolVar(&flagIncludeCSS, "include-css", false, "Track CSS/SCSS changes and propagate taint through style imports")
	flag.BoolVar(&flagLog, "log", false, "Print per-level, per-package analysis output")
	flag.BoolVar(&flagDebug, "debug", false, "Print detailed debug logs to stderr (implies --log)")
	flag.Parse()

	// --debug implies --log
	if flagDebug {
		flagLog = true
	}

	analyzer.Debug = flagDebug
	analyzer.IncludeCSS = flagIncludeCSS

	mergeBase, err := git.MergeBase("master")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error finding merge-base with master: %v\n", err)
		os.Exit(1)
	}

	changedFiles, err := git.ChangedFilesSince(mergeBase)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting changed files: %v\n", err)
		os.Exit(1)
	}

	rushConfig, err := rush.LoadConfig(".")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading rush config: %v\n", err)
		os.Exit(1)
	}

	projectMap := rush.BuildProjectMap(rushConfig)
	changedProjects := rush.FindChangedProjects(rushConfig, projectMap, changedFiles)

	// Detect lockfile dep changes per subspace (folder → set of changed dep names)
	depChangedDeps := findLockfileAffectedProjects(rushConfig, mergeBase)

	// Add dep-affected projects to the changed set (they count as directly changed)
	for folder := range depChangedDeps {
		for _, rp := range rushConfig.Projects {
			if rp.ProjectFolder == folder {
				if changedProjects[rp.PackageName] == nil {
					changedProjects[rp.PackageName] = projectMap[rp.PackageName]
				}
				break
			}
		}
	}

	// Find the full affected subgraph: directly changed + all transitive dependents
	var seeds []string
	for pkgName := range changedProjects {
		seeds = append(seeds, pkgName)
	}
	affectedSet := rush.FindTransitiveDependents(projectMap, seeds)

	// Topologically sort: level 0 = lowest-level (no deps on other affected packages)
	levels := rush.TopologicalSort(projectMap, affectedSet)

	logf("Merge base: %s\n\n", mergeBase)
	logf("Directly changed projects: %d\n", len(changedProjects))
	logf("Dep-affected projects (lockfile): %d\n", len(depChangedDeps))
	logf("Total affected projects (incl. transitive dependents): %d\n", len(affectedSet))
	logf("Processing in %d levels (bottom-up):\n\n", len(levels))

	// Track affected exports per package for cross-package propagation.
	allUpstreamTaint := make(map[string]map[string]bool)

	for levelIdx, level := range levels {
		// TODO: packages within the same level that don't depend on each other
		// can be processed in parallel using goroutines.
		logf("--- Level %d (%d packages) ---\n\n", levelIdx, len(level))

		for _, pkgName := range level {
			info := projectMap[pkgName]
			if info == nil {
				continue
			}
			pkg := info.Package
			lib := analyzer.IsLibrary(pkg)
			directlyChanged := changedProjects[pkgName] != nil
			changedDeps := depChangedDeps[info.ProjectFolder]
			isDepAffected := len(changedDeps) > 0

			logf("=== %s (%s) ===\n", pkgName, info.ProjectFolder)
			if directlyChanged && isDepAffected {
				logf("  [directly changed + dep change in lockfile]\n")
			} else if directlyChanged {
				logf("  [directly changed]\n")
			} else if isDepAffected {
				logf("  [dep change in lockfile]\n")
			} else {
				logf("  [affected via dependencies]\n")
			}

			if !lib {
				logf("  Type: app (not a library) — skipping export analysis\n\n")
				continue
			}

			logf("  Type: library\n")

			entrypoints := analyzer.FindEntrypoints(info.ProjectFolder, pkg)
			if len(entrypoints) == 0 {
				logf("  No entrypoints found — skipping\n\n")
				continue
			}
			logf("  Entrypoints:\n")
			for _, ep := range entrypoints {
				logf("    %s → %s\n", ep.ExportPath, ep.SourceFile)
			}

			if isDepAffected {
				depNames := make([]string, 0, len(changedDeps))
				for d := range changedDeps {
					depNames = append(depNames, d)
				}
				logf("  Changed external deps: %s\n", strings.Join(depNames, ", "))
			}

			// Build upstream taint for this package from its dependencies
			pkgUpstreamTaint := make(map[string]map[string]bool)
			for _, dep := range info.DependsOn {
				for specifier, names := range allUpstreamTaint {
					if strings.HasPrefix(specifier, dep) {
						if pkgUpstreamTaint[specifier] == nil {
							pkgUpstreamTaint[specifier] = make(map[string]bool)
						}
						for n := range names {
							pkgUpstreamTaint[specifier][n] = true
						}
					}
				}
			}

			affected, err := analyzer.AnalyzeLibraryPackage(info.ProjectFolder, entrypoints, mergeBase, changedFiles, flagIncludeTypes, pkgUpstreamTaint, changedDeps)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  Error analyzing package: %v\n", err)
				continue
			}

			if len(affected) == 0 {
				logf("  No affected exports found\n\n")
				continue
			}

			logf("  Affected exports:\n")
			for _, ae := range affected {
				logf("    Entrypoint %q:\n", ae.EntrypointPath)
				for _, name := range ae.ExportNames {
					logf("      - %s\n", name)
				}

				specifier := pkgName
				if ae.EntrypointPath != "." {
					specifier = pkgName + strings.TrimPrefix(ae.EntrypointPath, ".")
				}
				if allUpstreamTaint[specifier] == nil {
					allUpstreamTaint[specifier] = make(map[string]bool)
				}
				for _, name := range ae.ExportNames {
					allUpstreamTaint[specifier][name] = true
				}
			}
			logf("\n")
		}
	}

	// CSS/SCSS taint propagation: when --include-css is set, any changed CSS/SCSS
	// file in a library taints all style imports from that library in downstream packages.
	if flagIncludeCSS {
		cssTaintedPkgs := analyzer.FindCSSTaintedPackages(changedFiles, rushConfig, projectMap)
		for pkgName := range cssTaintedPkgs {
			key := analyzer.CSSTaintPrefix + pkgName
			if allUpstreamTaint[key] == nil {
				allUpstreamTaint[key] = make(map[string]bool)
			}
			allUpstreamTaint[key]["*"] = true
			if flagDebug {
				fmt.Fprintf(os.Stderr, "[DEBUG] CSS taint: %s\n", pkgName)
			}
		}
		// Propagate CSS taint through SCSS @use chains across libraries
		analyzer.PropagateCSSTaint(rushConfig, projectMap, allUpstreamTaint)
	}

	// Collect affected e2e packages with precise 4-condition detection.
	// For each e2e/ project (non-library), check:
	//   1. Direct file changes (outside .goodchangesrc.json ignores)
	//   2. External dep changes in lockfile
	//   3. Tainted workspace imports into the e2e project
	//   4. Corresponding app is tainted (directly changed, lockfile deps, or tainted imports)
	changedE2E := make(map[string]bool)
	for _, rp := range rushConfig.Projects {
		if !strings.HasPrefix(rp.ProjectFolder, "e2e/") {
			continue
		}
		info := projectMap[rp.PackageName]
		if info == nil {
			continue
		}
		if analyzer.IsLibrary(info.Package) {
			continue // e.g. e2e-utils is a library, not an e2e test package
		}

		ignoreCfg := rush.LoadIgnoreConfig(rp.ProjectFolder)

		// Condition 1: Direct file changes (outside ignores)
		for _, f := range changedFiles {
			if strings.HasPrefix(f, rp.ProjectFolder+"/") {
				relPath := strings.TrimPrefix(f, rp.ProjectFolder+"/")
				if !ignoreCfg.IsIgnored(relPath) {
					changedE2E[rp.PackageName] = true
					break
				}
			}
		}
		if changedE2E[rp.PackageName] {
			continue
		}

		// Condition 2: External dep changes in lockfile
		if len(depChangedDeps[rp.ProjectFolder]) > 0 {
			changedE2E[rp.PackageName] = true
			continue
		}

		// Condition 3: Tainted workspace imports (filtered by ignores)
		if analyzer.HasTaintedImports(rp.ProjectFolder, allUpstreamTaint, ignoreCfg) {
			changedE2E[rp.PackageName] = true
			continue
		}

		// Condition 4: Corresponding app is tainted
		for _, dep := range info.DependsOn {
			depInfo := projectMap[dep]
			if depInfo == nil {
				continue
			}
			if analyzer.IsLibrary(depInfo.Package) {
				continue // only check app dependencies
			}
			// App is tainted if: directly changed, lockfile dep changes, or tainted workspace imports
			if changedProjects[dep] != nil {
				changedE2E[rp.PackageName] = true
				break
			}
			if len(depChangedDeps[depInfo.ProjectFolder]) > 0 {
				changedE2E[rp.PackageName] = true
				break
			}
			if analyzer.HasTaintedImports(depInfo.ProjectFolder, allUpstreamTaint, nil) {
				changedE2E[rp.PackageName] = true
				break
			}
		}
	}
	// sdk-ui-tests-e2e lives in sdk/libs/, not e2e/ — separate detection
	if changedProjects["@gooddata/sdk-ui-tests-e2e"] != nil {
		changedE2E["@gooddata/sdk-ui-tests-e2e"] = true
	}
	if len(depChangedDeps["sdk/libs/sdk-ui-tests-e2e"]) > 0 {
		changedE2E["@gooddata/sdk-ui-tests-e2e"] = true
	}
	// Check if sdk-ui-tests-e2e's scenarios app imports any tainted exports
	if analyzer.HasTaintedImports("sdk/libs/sdk-ui-tests-e2e", allUpstreamTaint, nil) {
		changedE2E["@gooddata/sdk-ui-tests-e2e"] = true
	}

	// Neobackstop detection: triggered if any files in specific sdk-ui-tests dirs
	// are modified, or if any of those dirs import tainted exports.
	neobackstopDirs := []string{
		"sdk/libs/sdk-ui-tests/src",
		"sdk/libs/sdk-ui-tests/scenarios",
		"sdk/libs/sdk-ui-tests/stories",
		"sdk/libs/sdk-ui-tests/neobackstop",
	}
	showNeobackstop := false
	for _, f := range changedFiles {
		for _, dir := range neobackstopDirs {
			if strings.HasPrefix(f, dir+"/") {
				showNeobackstop = true
				break
			}
		}
		if showNeobackstop {
			break
		}
	}
	if !showNeobackstop {
		for _, dir := range neobackstopDirs {
			if analyzer.HasTaintedImports(dir, allUpstreamTaint, nil) {
				showNeobackstop = true
				break
			}
		}
	}
	if showNeobackstop {
		changedE2E["neobackstop"] = true
	}

	// Build sorted list of affected e2e packages
	e2eList := make([]string, 0, len(changedE2E))
	for name := range changedE2E {
		e2eList = append(e2eList, name)
	}
	sort.Strings(e2eList)

	if flagLog {
		logf("Affected e2e packages (%d):\n", len(e2eList))
		for _, name := range e2eList {
			logf("  - %s\n", name)
		}
	}

	// Always output JSON to stdout
	jsonBytes, _ := json.Marshal(e2eList)
	fmt.Println(string(jsonBytes))
}

// findLockfileAffectedProjects checks each subspace's pnpm-lock.yaml for dep changes.
// Returns a map of project folder → set of changed external dep package names.
func findLockfileAffectedProjects(config *rush.Config, mergeBase string) map[string]map[string]bool {
	// Collect subspaces: "default" for projects without subspaceName, plus named ones
	subspaces := make(map[string]bool)
	subspaces["default"] = true
	for _, p := range config.Projects {
		if p.SubspaceName != "" {
			subspaces[p.SubspaceName] = true
		}
	}

	result := make(map[string]map[string]bool)
	for subspace := range subspaces {
		lockfilePath := filepath.Join("common", "config", "subspaces", subspace, "pnpm-lock.yaml")
		if _, err := os.Stat(lockfilePath); err != nil {
			continue
		}
		diffText, err := git.DiffSincePath(mergeBase, lockfilePath)
		if err != nil || diffText == "" {
			continue
		}
		affected := lockfile.FindDepAffectedProjects(lockfilePath, subspace, diffText)
		for folder, deps := range affected {
			if result[folder] == nil {
				result[folder] = make(map[string]bool)
			}
			for dep := range deps {
				result[folder][dep] = true
			}
		}
	}
	return result
}
