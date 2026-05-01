package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"goodchanges/internal/log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"

	"goodchanges/internal/analyzer"
	"goodchanges/internal/git"
	"goodchanges/internal/lockfile"
	"goodchanges/internal/rush"
)

//go:embed VERSION
var version string

var flagIncludeTypes bool
var flagIncludeCSS bool
var flagIgnoreAppRelationship bool
var flagLog bool
var flagDebug bool

type TargetResult struct {
	Name       string   `json:"name"`
	Detections []string `json:"detections,omitempty"`
}

// envBool returns true if the environment variable is set to a non-empty value.
func envBool(key string) bool {
	return os.Getenv(key) != ""
}

func main() {
	for _, arg := range os.Args[1:] {
		if arg == "-v" || arg == "--version" {
			fmt.Print(strings.TrimSpace(version))
			fmt.Println()
			os.Exit(0)
		}
		if arg == "--list" {
			rushConfig, err := rush.LoadConfig(".")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading rush config: %v\n", err)
				os.Exit(1)
			}
			data, err := json.MarshalIndent(rushConfig.Projects, "", "  ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error marshalling projects: %v\n", err)
				os.Exit(1)
			}
			fmt.Println(string(data))
			os.Exit(0)
		}
	}

	flagIncludeTypes = envBool("INCLUDE_TYPES")
	flagIncludeCSS = envBool("INCLUDE_CSS")
	flagIgnoreAppRelationship = envBool("IGNORE_APP_RELATIONSHIP")

	logLevel := strings.ToUpper(os.Getenv("LOG_LEVEL"))
	flagLog = logLevel == "BASIC" || logLevel == "DEBUG"
	flagDebug = logLevel == "DEBUG"

	log.Debug = flagDebug
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
	configMap := rush.LoadAllProjectConfigs(rushConfig)

	// Parse TARGETS filter early to skip expensive detection for non-matching targets
	var targetPatterns []string
	if targetsEnv := os.Getenv("TARGETS"); targetsEnv != "" {
		targetPatterns = strings.Split(targetsEnv, ",")
	}

	// When TARGETS is set, compute the relevant package set: active targets + their
	// transitive dependencies. Only these packages need change detection and analysis.
	var relevantPackages map[string]bool
	if len(targetPatterns) > 0 {
		var targetSeeds []string
		for _, rp := range rushConfig.Projects {
			cfg := configMap[rp.ProjectFolder]
			if cfg == nil {
				continue
			}
			for _, td := range cfg.Targets {
				if matchesTargetFilter(td.OutputName(rp.PackageName), targetPatterns) {
					targetSeeds = append(targetSeeds, rp.PackageName)
				}
			}
		}
		relevantPackages = rush.FindTransitiveDependencies(projectMap, targetSeeds)
	}

	changedProjects := rush.FindChangedProjects(rushConfig, projectMap, changedFiles, configMap, relevantPackages)

	// Detect lockfile dep changes per subspace (folder → set of changed dep names)
	depChangedDeps, versionChangedSubspaces := findLockfileAffectedProjects(rushConfig, mergeBase)

	// When lockfileVersion changes in a subspace, treat all projects in that subspace
	// as having all external deps changed. This feeds into the existing taint propagation:
	// depChangedDeps → changedProjects → affectedSet → library analysis → target detection.
	for _, rp := range rushConfig.Projects {
		subspace := rp.SubspaceName
		if subspace == "" {
			subspace = "default"
		}
		if versionChangedSubspaces[subspace] {
			if depChangedDeps[rp.ProjectFolder] == nil {
				depChangedDeps[rp.ProjectFolder] = make(map[string]bool)
			}
			depChangedDeps[rp.ProjectFolder]["*"] = true
		}
	}

	// Add dep-affected projects to the changed set (they count as directly changed)
	for folder := range depChangedDeps {
		for _, rp := range rushConfig.Projects {
			if rp.ProjectFolder == folder {
				if relevantPackages != nil && !relevantPackages[rp.PackageName] {
					break
				}
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

	// Narrow to relevant packages when TARGETS is set
	if relevantPackages != nil {
		for pkg := range affectedSet {
			if !relevantPackages[pkg] {
				delete(affectedSet, pkg)
			}
		}
	}

	// Topologically sort: level 0 = lowest-level (no deps on other affected packages)
	levels := rush.TopologicalSort(projectMap, affectedSet)

	log.Basicf("Merge base: %s\n\n", mergeBase)
	log.Basicf("Directly changed projects: %d\n", len(changedProjects))
	log.Basicf("Dep-affected projects (lockfile): %d\n", len(depChangedDeps))
	log.Basicf("Total affected projects (incl. transitive dependents): %d\n", len(affectedSet))
	log.Basicf("Processing in %d levels (bottom-up):\n\n", len(levels))

	// Track affected exports per package for cross-package propagation.
	allUpstreamTaint := make(map[string]map[string]bool)

	// Seed upstream taint for libraries in version-changed subspaces.
	// A lockfileVersion change means we can't reliably diff individual deps,
	// so treat all exports as tainted. This propagates through the analysis loop.
	for _, rp := range rushConfig.Projects {
		subspace := rp.SubspaceName
		if subspace == "" {
			subspace = "default"
		}
		if !versionChangedSubspaces[subspace] {
			continue
		}
		info := projectMap[rp.PackageName]
		if info == nil {
			continue
		}
		if analyzer.IsLibrary(info.Package) {
			if allUpstreamTaint[rp.PackageName] == nil {
				allUpstreamTaint[rp.PackageName] = make(map[string]bool)
			}
			allUpstreamTaint[rp.PackageName]["*"] = true
		}
	}

	type pkgResult struct {
		pkgName  string
		affected []analyzer.AffectedExport
	}

	for levelIdx, level := range levels {
		log.Basicf("--- Level %d (%d packages) ---\n\n", levelIdx, len(level))

		var wg sync.WaitGroup
		resultsCh := make(chan pkgResult, len(level))

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

			log.Basicf("=== %s (%s) ===\n", pkgName, info.ProjectFolder)
			if directlyChanged && isDepAffected {
				log.Basicf("  [directly changed + dep change in lockfile]\n")
			} else if directlyChanged {
				log.Basicf("  [directly changed]\n")
			} else if isDepAffected {
				log.Basicf("  [dep change in lockfile]\n")
			} else {
				log.Basicf("  [affected via dependencies]\n")
			}

			if !lib {
				log.Basicf("  Type: app (not a library) — skipping export analysis\n\n")
				continue
			}

			log.Basicf("  Type: library\n")

			entrypoints := analyzer.FindEntrypoints(info.ProjectFolder, pkg)
			if len(entrypoints) == 0 {
				log.Basicf("  No entrypoints found — skipping\n\n")
				continue
			}
			log.Basicf("  Entrypoints:\n")
			for _, ep := range entrypoints {
				log.Basicf("    %s → %s\n", ep.ExportPath, ep.SourceFile)
			}

			if isDepAffected {
				depNames := make([]string, 0, len(changedDeps))
				for d := range changedDeps {
					depNames = append(depNames, d)
				}
				log.Basicf("  Changed external deps: %s\n", strings.Join(depNames, ", "))
			}

			// Global changeDirs: if triggered, enumerate all exports per entrypoint
			// and seed them as tainted (skip expensive per-symbol analysis).
			libCfg := configMap[info.ProjectFolder]
			if libCfg != nil && len(libCfg.ChangeDirs) > 0 {
				if globalChangeDirTriggered(libCfg.ChangeDirs, changedFiles, info.ProjectFolder, libCfg) {
					totalExports := 0
					for _, ep := range entrypoints {
						specifier := pkgName
						if ep.ExportPath != "." {
							specifier = pkgName + strings.TrimPrefix(ep.ExportPath, ".")
						}
						exports := analyzer.CollectEntrypointExports(info.ProjectFolder, ep)
						if allUpstreamTaint[specifier] == nil {
							allUpstreamTaint[specifier] = make(map[string]bool)
						}
						for _, name := range exports {
							allUpstreamTaint[specifier][name] = true
						}
						totalExports += len(exports)
					}
					log.Basicf("  Global changeDirs triggered — %d exports tainted across %d entrypoints\n\n", totalExports, len(entrypoints))
					continue
				}
			}

			// Build upstream taint for this package from its dependencies.
			// allUpstreamTaint is only read here — writes happen after the level completes.
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

			wg.Add(1)
			go func(pkgName string, projectFolder string, entrypoints []analyzer.Entrypoint, pkgUpstreamTaint map[string]map[string]bool, changedDeps map[string]bool) {
				defer wg.Done()
				affected, err := analyzer.AnalyzeLibraryPackage(projectFolder, entrypoints, mergeBase, changedFiles, flagIncludeTypes, pkgUpstreamTaint, changedDeps)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  Error analyzing package %s: %v\n", pkgName, err)
					return
				}
				if len(affected) > 0 {
					resultsCh <- pkgResult{pkgName: pkgName, affected: affected}
				}
			}(pkgName, info.ProjectFolder, entrypoints, pkgUpstreamTaint, changedDeps)
		}

		wg.Wait()
		close(resultsCh)

		// Merge results into allUpstreamTaint after all goroutines in this level are done
		for res := range resultsCh {
			log.Basicf("  Affected exports for %s:\n", res.pkgName)
			for _, ae := range res.affected {
				log.Basicf("    Entrypoint %q:\n", ae.EntrypointPath)
				for _, name := range ae.ExportNames {
					log.Basicf("      - %s\n", name)
				}

				specifier := res.pkgName
				if ae.EntrypointPath != "." {
					specifier = res.pkgName + strings.TrimPrefix(ae.EntrypointPath, ".")
				}
				if allUpstreamTaint[specifier] == nil {
					allUpstreamTaint[specifier] = make(map[string]bool)
				}
				for _, name := range ae.ExportNames {
					allUpstreamTaint[specifier][name] = true
				}
			}
			log.Basicf("\n")
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

	// Detect affected targets from .goodchangesrc.json configs.
	changedE2E := make(map[string]*TargetResult)
	defaultChangeDirs := []rush.ChangeDir{{Glob: "**/*"}}

	for _, rp := range rushConfig.Projects {
		cfg := configMap[rp.ProjectFolder]
		if cfg == nil {
			continue
		}

		// Global changeDirs: if triggered, add ALL targets for this package
		if len(cfg.ChangeDirs) > 0 {
			if globalChangeDirTriggered(cfg.ChangeDirs, changedFiles, rp.ProjectFolder, cfg) {
				for _, td := range cfg.Targets {
					name := td.OutputName(rp.PackageName)
					if len(targetPatterns) > 0 && !matchesTargetFilter(name, targetPatterns) {
						continue
					}
					changedE2E[name] = &TargetResult{Name: name}
				}
				continue
			}
		}

		for _, td := range cfg.Targets {
			name := td.OutputName(rp.PackageName)
			if len(targetPatterns) > 0 && !matchesTargetFilter(name, targetPatterns) {
				continue
			}

			// Merge global + per-target ignores for this target's detection
			targetCfg := cfg.WithTargetIgnores(td)

			// Quick check: lockfile dep changes (project-wide)
			if len(depChangedDeps[rp.ProjectFolder]) > 0 {
				changedE2E[name] = &TargetResult{Name: name}
				continue
			}

			// Quick check: app taint
			if td.App != nil && !flagIgnoreAppRelationship {
				appInfo := projectMap[*td.App]
				if appInfo != nil {
					if changedProjects[*td.App] != nil {
						changedE2E[name] = &TargetResult{Name: name}
						continue
					}
					if len(depChangedDeps[appInfo.ProjectFolder]) > 0 {
						changedE2E[name] = &TargetResult{Name: name}
						continue
					}
					if analyzer.HasTaintedImports(appInfo.ProjectFolder, allUpstreamTaint, nil) {
						changedE2E[name] = &TargetResult{Name: name}
						continue
					}
				}
			}

			// ChangeDirs detection (defaults to **/* if not configured)
			changeDirs := td.ChangeDirs
			if len(changeDirs) == 0 {
				changeDirs = defaultChangeDirs
			}

			normalTriggered := false
			var fineGrainedDetections []string

			for _, cd := range changeDirs {
				if cd.IsFineGrained() {
					filterPattern := ""
					if cd.Filter != nil {
						filterPattern = *cd.Filter
					}
					detected := analyzer.FindAffectedFiles(cd.Glob, filterPattern, allUpstreamTaint, changedFiles, rp.ProjectFolder, targetCfg, depChangedDeps[rp.ProjectFolder], mergeBase, flagIncludeTypes)
					if len(detected) > 0 {
						fineGrainedDetections = append(fineGrainedDetections, detected...)
					}
				} else {
					// Normal: check for any changed file matching the glob
					for _, f := range changedFiles {
						if !strings.HasPrefix(f, rp.ProjectFolder+"/") {
							continue
						}
						relPath := strings.TrimPrefix(f, rp.ProjectFolder+"/")
						if targetCfg.IsIgnored(relPath) {
							continue
						}
						if matched, _ := doublestar.Match(cd.Glob, relPath); matched {
							normalTriggered = true
							break
						}
					}
					if !normalTriggered {
						if analyzer.HasTaintedImportsForGlob(rp.ProjectFolder, cd.Glob, allUpstreamTaint, targetCfg) {
							normalTriggered = true
						}
					}
				}
				if normalTriggered {
					break
				}
			}

			if normalTriggered {
				changedE2E[name] = &TargetResult{Name: name}
			} else if len(fineGrainedDetections) > 0 {
				sort.Strings(fineGrainedDetections)
				changedE2E[name] = &TargetResult{
					Name:       name,
					Detections: fineGrainedDetections,
				}
			}
		}
	}

	// Build sorted list of affected targets
	e2eList := make([]*TargetResult, 0, len(changedE2E))
	for _, result := range changedE2E {
		e2eList = append(e2eList, result)
	}
	sort.Slice(e2eList, func(i, j int) bool {
		return e2eList[i].Name < e2eList[j].Name
	})

	if flagLog {
		log.Basicf("Affected e2e packages (%d):\n", len(e2eList))
		for _, result := range e2eList {
			if len(result.Detections) > 0 {
				log.Basicf("  - %s (fine-grained: %d files)\n", result.Name, len(result.Detections))
				for _, d := range result.Detections {
					log.Basicf("      %s\n", d)
				}
			} else {
				log.Basicf("  - %s\n", result.Name)
			}
		}
	}

	// Always output JSON to stdout
	jsonBytes, _ := json.Marshal(e2eList)
	fmt.Println(string(jsonBytes))
}

// findLockfileAffectedProjects checks each subspace's pnpm-lock.yaml for dep changes.
// Parses old (merge base) and new (current) lockfiles as YAML and compares resolved
// versions for direct and transitive dependencies.
// Returns:
//   - depChanges: project folder → set of changed external dep package names
//   - versionChanges: subspace name → true for subspaces where lockfileVersion changed
func findLockfileAffectedProjects(config *rush.Config, mergeBase string) (map[string]map[string]bool, map[string]bool) {
	// Collect subspaces: "default" for projects without subspaceName, plus named ones
	subspaces := make(map[string]bool)
	subspaces["default"] = true
	for _, p := range config.Projects {
		if p.SubspaceName != "" {
			subspaces[p.SubspaceName] = true
		}
	}

	result := make(map[string]map[string]bool)
	versionChanged := make(map[string]bool)
	for subspace := range subspaces {
		lockfilePath := filepath.Join("common", "config", "subspaces", subspace, "pnpm-lock.yaml")
		newContent, err := os.ReadFile(lockfilePath)
		if err != nil {
			continue
		}
		oldContent, _ := git.ShowFile(mergeBase, lockfilePath)

		oldLf := lockfile.ParseLockfile([]byte(oldContent))
		newLf := lockfile.ParseLockfile(newContent)

		if oldLf.Version() != newLf.Version() {
			versionChanged[subspace] = true
			log.Basicf("lockfileVersion changed in subspace %q: %q → %q\n", subspace, oldLf.Version(), newLf.Version())
		}

		affected := lockfile.FindDepChanges(oldLf, newLf, subspace)
		for folder, deps := range affected {
			if result[folder] == nil {
				result[folder] = make(map[string]bool)
			}
			for dep := range deps {
				result[folder][dep] = true
			}
		}
	}
	return result, versionChanged
}

// matchesTargetFilter checks if a target name matches any of the given patterns.
// Patterns support * as a wildcard matching any characters (including /).
// globalChangeDirTriggered checks if any changed file matches a global changeDir glob.
func globalChangeDirTriggered(changeDirs []rush.ChangeDir, changedFiles []string, projectFolder string, cfg *rush.ProjectConfig) bool {
	for _, cd := range changeDirs {
		for _, f := range changedFiles {
			if !strings.HasPrefix(f, projectFolder+"/") {
				continue
			}
			relPath := strings.TrimPrefix(f, projectFolder+"/")
			if cfg.IsIgnored(relPath) {
				continue
			}
			if matched, _ := doublestar.Match(cd.Glob, relPath); matched {
				return true
			}
		}
	}
	return false
}

func matchesTargetFilter(name string, patterns []string) bool {
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Convert glob pattern to regex: * -> .*, escape the rest
		var re strings.Builder
		re.WriteString("^")
		for _, ch := range p {
			if ch == '*' {
				re.WriteString(".*")
			} else {
				re.WriteString(regexp.QuoteMeta(string(ch)))
			}
		}
		re.WriteString("$")
		if matched, _ := regexp.MatchString(re.String(), name); matched {
			return true
		}
	}
	return false
}
