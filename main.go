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

	// Detect lockfile dep changes per subspace
	depAffectedFolders := findLockfileAffectedProjects(rushConfig, mergeBase)

	// Add dep-affected projects to the changed set (they count as directly changed)
	for folder := range depAffectedFolders {
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
	fmt.Printf("Dep-affected projects (lockfile): %d\n", len(depAffectedFolders))
	fmt.Printf("Total affected projects (incl. transitive dependents): %d\n", len(affectedSet))
	fmt.Printf("Processing in %d levels (bottom-up):\n\n", len(levels))

	// Track affected exports per package for cross-package propagation.
	allUpstreamTaint := make(map[string]map[string]bool)

	changedE2E := make(map[string]bool)
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
			isDepAffected := depAffectedFolders[info.ProjectFolder]

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
				if strings.HasPrefix(info.ProjectFolder, "apps/") {
					appName := strings.TrimPrefix(info.ProjectFolder, "apps/")
					changedE2E["e2e/"+appName+"-e2e"] = true
				}
				// Direct changes in e2e packages themselves
				if strings.HasPrefix(info.ProjectFolder, "e2e/") && directlyChanged {
					changedE2E[info.ProjectFolder] = true
				}
				if info.ProjectFolder == "sdk/libs/sdk-ui-tests-e2e" && directlyChanged {
					changedE2E[info.ProjectFolder] = true
				}
				continue
			}

			fmt.Printf("  Type: library\n")

			// Direct changes in e2e library packages (e.g. sdk-ui-tests-e2e)
			if directlyChanged && info.ProjectFolder == "sdk/libs/sdk-ui-tests-e2e" {
				changedE2E[info.ProjectFolder] = true
			}

			entrypoints := analyzer.FindEntrypoints(info.ProjectFolder, pkg)
			if len(entrypoints) == 0 {
				fmt.Printf("  No entrypoints found — skipping\n\n")
				continue
			}
			fmt.Printf("  Entrypoints:\n")
			for _, ep := range entrypoints {
				fmt.Printf("    %s → %s\n", ep.ExportPath, ep.SourceFile)
			}

			// If this project has a dep change in the lockfile, all exports are tainted.
			// TODO: instead of tainting all exports, find which direct deps changed (including
			// transitive changes via the lockfile dep graph), find all imports of those deps
			// in this package's source, taint all usages of those imports, then trace up to
			// the package's exports — same as intra-package taint propagation.
			if isDepAffected {
				fmt.Printf("  [all exports tainted due to lockfile dep change]\n")
				for _, ep := range entrypoints {
					specifier := pkgName
					if ep.ExportPath != "." {
						specifier = pkgName + strings.TrimPrefix(ep.ExportPath, ".")
					}
					// Collect all export names from this entrypoint
					epExports := analyzer.CollectEntrypointExports(info.ProjectFolder, ep)
					if len(epExports) > 0 {
						if allUpstreamTaint[specifier] == nil {
							allUpstreamTaint[specifier] = make(map[string]bool)
						}
						for _, name := range epExports {
							allUpstreamTaint[specifier][name] = true
						}
						fmt.Printf("    Entrypoint %q: all %d exports tainted\n", ep.ExportPath, len(epExports))
					}
				}
				fmt.Println()
				if strings.HasPrefix(info.ProjectFolder, "sdk/libs/") {
					sdkLibsAffected = true
				}
				// Still run normal analysis to catch code changes too
				// but skip if no direct code diff
				if directDiffs[pkgName] == "" {
					continue
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

			affected, err := analyzer.AnalyzeLibraryPackage(info.ProjectFolder, entrypoints, diffText, flagIncludeTypes, pkgUpstreamTaint)
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

	if sdkLibsAffected {
		changedE2E["sdk/libs/sdk-ui-tests-e2e"] = true
	}

	fmt.Printf("Affected e2e packages (%d):\n", len(changedE2E))
	for folder := range changedE2E {
		fmt.Printf("  - %s\n", folder)
	}
}

// findLockfileAffectedProjects checks each subspace's pnpm-lock.yaml for dep changes.
func findLockfileAffectedProjects(config *rush.Config, mergeBase string) map[string]bool {
	// Collect subspaces: "default" for projects without subspaceName, plus named ones
	subspaces := make(map[string]bool)
	subspaces["default"] = true
	for _, p := range config.Projects {
		if p.SubspaceName != "" {
			subspaces[p.SubspaceName] = true
		}
	}

	result := make(map[string]bool)
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
		for folder := range affected {
			result[folder] = true
		}
	}
	return result
}
