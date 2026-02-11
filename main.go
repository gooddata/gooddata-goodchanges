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

	// Get filtered diff per changed project
	type projectWithDiff struct {
		info *rush.ProjectInfo
		diff string
	}
	projects := make(map[string]*projectWithDiff)
	for pkgName, info := range changedProjects {
		d, err := git.DiffSincePath(mergeBase, info.ProjectFolder)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not get diff for %s: %v\n", pkgName, err)
			continue
		}
		projects[pkgName] = &projectWithDiff{info: info, diff: d}
	}

	fmt.Printf("Merge base: %s\n\n", mergeBase[:12])
	fmt.Printf("Changed projects (%d):\n\n", len(projects))

	var changedApps []string

	for pkgName, p := range projects {
		pkg := p.info.Package
		lib := analyzer.IsLibrary(pkg)

		fmt.Printf("=== %s (%s) ===\n", pkgName, p.info.ProjectFolder)

		if !lib {
			fmt.Printf("  Type: app (not a library) — skipping export analysis\n\n")
			changedApps = append(changedApps, pkgName)
			continue
		}

		fmt.Printf("  Type: library\n")

		entrypoints := analyzer.FindEntrypoints(p.info.ProjectFolder, pkg)
		if len(entrypoints) == 0 {
			fmt.Printf("  No entrypoints found — skipping\n\n")
			continue
		}
		fmt.Printf("  Entrypoints:\n")
		for _, ep := range entrypoints {
			fmt.Printf("    %s → %s\n", ep.ExportPath, ep.SourceFile)
		}

		affected, err := analyzer.AnalyzeLibraryPackage(p.info.ProjectFolder, entrypoints, p.diff, flagIncludeTypes)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error analyzing package: %v\n", err)
			continue
		}

		if len(affected) == 0 {
			fmt.Printf("  No affected exports found\n\n")
			continue
		}

		fmt.Printf("  Affected exports:\n")
		for _, ae := range affected {
			fmt.Printf("    Entrypoint %q:\n", ae.EntrypointPath)
			for _, name := range ae.ExportNames {
				fmt.Printf("      - %s\n", name)
			}
		}
		fmt.Println()
	}

	fmt.Printf("Changed apps (%d):\n", len(changedApps))
	for _, name := range changedApps {
		fmt.Printf("  - %s\n", name)
	}

	_ = strings.Join // keep strings import for future use
}
