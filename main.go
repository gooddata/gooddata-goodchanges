package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"goodchanges/internal/analyzer"
	"goodchanges/internal/git"
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
	fmt.Printf("Total affected projects (incl. transitive dependents): %d\n", len(affectedSet))
	fmt.Printf("Processing in %d levels (bottom-up):\n\n", len(levels))

	// Track affected exports per package for cross-package propagation.
	// Maps import specifier (e.g. "@gooddata/sdk-ui-kit") to set of affected export names.
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

			fmt.Printf("=== %s (%s) ===\n", pkgName, info.ProjectFolder)
			if directlyChanged {
				fmt.Printf("  [directly changed]\n")
			} else {
				fmt.Printf("  [affected via dependencies]\n")
			}

			if !lib {
				fmt.Printf("  Type: app (not a library) — skipping export analysis\n\n")
				// Map apps/{name} → e2e/{name}-e2e
				if strings.HasPrefix(info.ProjectFolder, "apps/") {
					appName := strings.TrimPrefix(info.ProjectFolder, "apps/")
					changedE2E["e2e/"+appName+"-e2e"] = true
				}
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

			diffText := directDiffs[pkgName] // empty string if not directly changed

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

				// Register affected exports for downstream packages to consume.
				// Map entrypoint to import specifier:
				//   "." → pkgName, "./internal" → pkgName + "/internal"
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
