package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"goodchanges/internal/analyzer"
	"goodchanges/internal/git"
	"goodchanges/internal/lockfile"
	"goodchanges/internal/rush"
)

var flagIncludeTypes bool

func main() {
	flag.BoolVar(&flagIncludeTypes, "include-types", false, "Include type/interface-only changes in analysis")
	flag.Parse()

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

	// Get diffs for directly changed projects
	directDiffs := make(map[string]string)
	for pkgName, info := range changedProjects {
		d, err := git.DiffSincePath(mergeBase, info.ProjectFolder)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not get diff for %s: %v\n", pkgName, err)
			continue
		}
		directDiffs[pkgName] = d
	}

	// Find the full affected subgraph: directly changed + all transitive dependents
	var seeds []string
	for pkgName := range changedProjects {
		seeds = append(seeds, pkgName)
	}
	affectedSet := rush.FindTransitiveDependents(projectMap, seeds)

	// Topologically sort: level 0 = lowest-level (no deps on other affected packages)
	levels := rush.TopologicalSort(projectMap, affectedSet)

	fmt.Printf("Merge base: %s\n\n", mergeBase[:12])
	fmt.Printf("Directly changed projects: %d\n", len(changedProjects))
	fmt.Printf("Dep-affected projects (lockfile): %d\n", len(depChangedDeps))
	fmt.Printf("Total affected projects (incl. transitive dependents): %d\n", len(affectedSet))
	fmt.Printf("Processing in %d levels (bottom-up):\n\n", len(levels))

	// Track affected exports per package for cross-package propagation.
	allUpstreamTaint := make(map[string]map[string]bool)

	sdkLibsAffected := false

	for levelIdx, level := range levels {
		// TODO: packages within the same level that don't depend on each other
		// can be processed in parallel using goroutines.
		fmt.Printf("--- Level %d (%d packages) ---\n\n", levelIdx, len(level))

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

			fmt.Printf("=== %s (%s) ===\n", pkgName, info.ProjectFolder)
			if directlyChanged && isDepAffected {
				fmt.Printf("  [directly changed + dep change in lockfile]\n")
			} else if directlyChanged {
				fmt.Printf("  [directly changed]\n")
			} else if isDepAffected {
				fmt.Printf("  [dep change in lockfile]\n")
			} else {
				fmt.Printf("  [affected via dependencies]\n")
			}

			if !lib {
				fmt.Printf("  Type: app (not a library) — skipping export analysis\n\n")
				continue
			}

			fmt.Printf("  Type: library\n")

			entrypoints := analyzer.FindEntrypoints(info.ProjectFolder, pkg)
			if len(entrypoints) == 0 {
				fmt.Printf("  No entrypoints found — skipping\n\n")
				continue
			}
			fmt.Printf("  Entrypoints:\n")
			for _, ep := range entrypoints {
				fmt.Printf("    %s → %s\n", ep.ExportPath, ep.SourceFile)
			}

			if isDepAffected {
				depNames := make([]string, 0, len(changedDeps))
				for d := range changedDeps {
					depNames = append(depNames, d)
				}
				fmt.Printf("  Changed external deps: %s\n", strings.Join(depNames, ", "))
				if strings.HasPrefix(info.ProjectFolder, "sdk/libs/") {
					sdkLibsAffected = true
				}
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

			diffText := directDiffs[pkgName]

			affected, err := analyzer.AnalyzeLibraryPackage(info.ProjectFolder, entrypoints, diffText, flagIncludeTypes, pkgUpstreamTaint, changedDeps)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  Error analyzing package: %v\n", err)
				continue
			}

			if len(affected) == 0 {
				fmt.Printf("  No affected exports found\n\n")
				continue
			}

			fmt.Printf("  Affected exports:\n")
			if strings.HasPrefix(info.ProjectFolder, "sdk/libs/") {
				sdkLibsAffected = true
			}
			for _, ae := range affected {
				fmt.Printf("    Entrypoint %q:\n", ae.EntrypointPath)
				for _, name := range ae.ExportNames {
					fmt.Printf("      - %s\n", name)
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
			fmt.Println()
		}
	}

	// Collect affected e2e packages from the dependency graph
	changedE2E := make(map[string]bool)
	for pkgName := range affectedSet {
		info := projectMap[pkgName]
		if info == nil {
			continue
		}
		if strings.HasPrefix(info.ProjectFolder, "e2e/") {
			changedE2E[pkgName] = true
		}
		// sdk-ui-tests-e2e lives in sdk/libs/, not e2e/ — detect direct changes
		if info.ProjectFolder == "sdk/libs/sdk-ui-tests-e2e" && changedProjects[pkgName] != nil {
			changedE2E[pkgName] = true
		}
	}
	if sdkLibsAffected {
		changedE2E["@gooddata/sdk-ui-tests-e2e"] = true
	}

	fmt.Printf("Affected e2e packages (%d):\n", len(changedE2E))
	for name := range changedE2E {
		fmt.Printf("  - %s\n", name)
	}
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
