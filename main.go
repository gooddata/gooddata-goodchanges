package main

import (
	"encoding/json"
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

// envBool returns true if the environment variable is set to a non-empty value.
func envBool(key string) bool {
	return os.Getenv(key) != ""
}

// logf prints to stdout only when LOG_LEVEL is set.
func logf(format string, args ...interface{}) {
	if flagLog {
		fmt.Printf(format, args...)
	}
}

func main() {
	flagIncludeTypes = envBool("INCLUDE_TYPES")
	flagIncludeCSS = envBool("INCLUDE_CSS")

	logLevel := strings.ToUpper(os.Getenv("LOG_LEVEL"))
	flagLog = logLevel == "BASIC" || logLevel == "DEBUG"
	flagDebug = logLevel == "DEBUG"

	analyzer.Debug = flagDebug
	analyzer.IncludeCSS = flagIncludeCSS

	var mergeBase string
	if commit := os.Getenv("COMPARE_COMMIT"); commit != "" {
		mergeBase = commit
	} else {
		compareBranch := os.Getenv("COMPARE_BRANCH")
		if compareBranch == "" {
			compareBranch = "origin/master"
		}
		var err error
		mergeBase, err = git.MergeBase(compareBranch)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error finding merge-base with %s: %v\n", compareBranch, err)
			os.Exit(1)
		}
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

	// Load project configs and detect affected targets.
	// Targets are defined by "type": "target" in .goodchangesrc.json.
	// Virtual targets are defined by "type": "virtual-target".
	changedE2E := make(map[string]bool)

	for _, rp := range rushConfig.Projects {
		cfg := rush.LoadProjectConfig(rp.ProjectFolder)

		if cfg.IsTarget() {
			// Target detection with 4 conditions:
			//   1. Direct file changes (outside ignores)
			//   2. External dep changes in lockfile
			//   3. Tainted workspace imports
			//   4. Corresponding app is tainted
			info := projectMap[rp.PackageName]
			if info == nil {
				continue
			}

			// Condition 1: Direct file changes
			for _, f := range changedFiles {
				if strings.HasPrefix(f, rp.ProjectFolder+"/") {
					relPath := strings.TrimPrefix(f, rp.ProjectFolder+"/")
					if !cfg.IsIgnored(relPath) {
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

			// Condition 3: Tainted workspace imports
			if analyzer.HasTaintedImports(rp.ProjectFolder, allUpstreamTaint, cfg) {
				changedE2E[rp.PackageName] = true
				continue
			}

			// Condition 4: Corresponding app is tainted
			if cfg.App != nil {
				appInfo := projectMap[*cfg.App]
				if appInfo != nil {
					if changedProjects[*cfg.App] != nil {
						changedE2E[rp.PackageName] = true
						continue
					}
					if len(depChangedDeps[appInfo.ProjectFolder]) > 0 {
						changedE2E[rp.PackageName] = true
						continue
					}
					if analyzer.HasTaintedImports(appInfo.ProjectFolder, allUpstreamTaint, nil) {
						changedE2E[rp.PackageName] = true
						continue
					}
				}
			}
		} else if cfg.IsVirtualTarget() && cfg.TargetName != nil {
			// Virtual target: check changeDirs for file changes or tainted imports
			triggered := false
			for _, dir := range cfg.ChangeDirs {
				fullDir := filepath.Join(rp.ProjectFolder, dir)
				for _, f := range changedFiles {
					if strings.HasPrefix(f, fullDir+"/") {
						triggered = true
						break
					}
				}
				if triggered {
					break
				}
				if analyzer.HasTaintedImports(fullDir, allUpstreamTaint, nil) {
					triggered = true
					break
				}
			}
			if triggered {
				changedE2E[*cfg.TargetName] = true
			}
		}
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
