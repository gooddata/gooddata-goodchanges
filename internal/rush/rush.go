package rush

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

type Project struct {
	PackageName   string   `json:"packageName"`
	ProjectFolder string   `json:"projectFolder"`
	ShouldPublish bool     `json:"shouldPublish"`
	SubspaceName  string   `json:"subspaceName"`
	Tags          []string `json:"tags"`
}

type Config struct {
	Projects []Project `json:"projects"`
}

type PackageJSON struct {
	Name            string            `json:"name"`
	Main            string            `json:"main"`
	Module          string            `json:"module"`
	Browser         string            `json:"browser"`
	Types           string            `json:"types"`
	Exports         json.RawMessage   `json:"exports"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

type ProjectInfo struct {
	Project
	Package      PackageJSON
	DependsOn    []string // package names of local rush projects this depends on
	DependedOnBy []string // package names of local rush projects that depend on this
}

// LoadConfig reads and parses rush.json from the given directory.
func LoadConfig(dir string) (*Config, error) {
	data, err := os.ReadFile(filepath.Join(dir, "rush.json"))
	if err != nil {
		return nil, fmt.Errorf("reading rush.json: %w", err)
	}
	cleaned := stripJSONCommentsAndTrailingCommas(data)
	var config Config
	if err := json.Unmarshal(cleaned, &config); err != nil {
		return nil, fmt.Errorf("parsing rush.json: %w", err)
	}
	return &config, nil
}

// BuildProjectMap parses each project's package.json and builds the dependency graph.
func BuildProjectMap(config *Config) map[string]*ProjectInfo {
	rushPackageSet := make(map[string]bool)
	for _, p := range config.Projects {
		rushPackageSet[p.PackageName] = true
	}

	projectMap := make(map[string]*ProjectInfo)
	for _, rp := range config.Projects {
		pkgPath := filepath.Join(rp.ProjectFolder, "package.json")
		pkgData, err := os.ReadFile(pkgPath)
		if err != nil {
			projectMap[rp.PackageName] = &ProjectInfo{Project: rp}
			continue
		}
		var pkg PackageJSON
		if err := json.Unmarshal(pkgData, &pkg); err != nil {
			projectMap[rp.PackageName] = &ProjectInfo{Project: rp}
			continue
		}

		info := &ProjectInfo{
			Project: rp,
			Package: pkg,
		}

		for depName, depVersion := range pkg.Dependencies {
			if strings.HasPrefix(depVersion, "workspace:") && rushPackageSet[depName] {
				info.DependsOn = append(info.DependsOn, depName)
			}
		}
		for depName, depVersion := range pkg.DevDependencies {
			if strings.HasPrefix(depVersion, "workspace:") && rushPackageSet[depName] {
				info.DependsOn = append(info.DependsOn, depName)
			}
		}

		projectMap[rp.PackageName] = info
	}

	// Build reverse edges
	for name, info := range projectMap {
		for _, dep := range info.DependsOn {
			if target, ok := projectMap[dep]; ok {
				target.DependedOnBy = append(target.DependedOnBy, name)
			}
		}
	}

	return projectMap
}

type ChangeDir struct {
	Glob   string  `json:"glob"`
	Filter *string `json:"filter,omitempty"` // optional output filter glob (fine-grained only)
	Type   *string `json:"type,omitempty"`   // nil = normal, "fine-grained"
}

// IsFineGrained returns true if this changeDir is configured for fine-grained detection.
func (cd ChangeDir) IsFineGrained() bool {
	return cd.Type != nil && *cd.Type == "fine-grained"
}

type TargetDef struct {
	Type       string      `json:"type"`                 // "target", "virtual-target"
	App        *string     `json:"app,omitempty"`        // rush project name of corresponding app
	TargetName *string     `json:"targetName,omitempty"` // output name for virtual targets
	ChangeDirs []ChangeDir `json:"changeDirs,omitempty"` // globs to watch for virtual targets
	Ignores    []string    `json:"ignores,omitempty"`    // per-target ignore globs (additive with global)
}

// IsTarget returns true if this target definition is a regular target.
func (td TargetDef) IsTarget() bool {
	return td.Type == "target"
}

// IsVirtualTarget returns true if this target definition is a virtual target.
func (td TargetDef) IsVirtualTarget() bool {
	return td.Type == "virtual-target"
}

type ProjectConfig struct {
	Targets []TargetDef `json:"targets,omitempty"`
	Ignores []string    `json:"ignores,omitempty"`
}

// LoadProjectConfig reads .goodchangesrc.json from the project folder.
// Returns nil if the file doesn't exist.
func LoadProjectConfig(projectFolder string) *ProjectConfig {
	data, err := os.ReadFile(filepath.Join(projectFolder, ".goodchangesrc.json"))
	if err != nil {
		return nil
	}
	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	return &cfg
}

// LoadAllProjectConfigs reads .goodchangesrc.json for every project in the config.
// Returns a map keyed by project folder. Entries are nil for projects without a config file.
func LoadAllProjectConfigs(config *Config) map[string]*ProjectConfig {
	result := make(map[string]*ProjectConfig, len(config.Projects))
	for _, rp := range config.Projects {
		result[rp.ProjectFolder] = LoadProjectConfig(rp.ProjectFolder)
	}
	return result
}

// IsIgnored checks if a file path (relative to project root) matches any ignore glob.
// The config file itself (.goodchangesrc.json) is always ignored.
func (pc *ProjectConfig) IsIgnored(relPath string) bool {
	if relPath == ".goodchangesrc.json" {
		return true
	}
	if pc == nil {
		return false
	}
	for _, pattern := range pc.Ignores {
		if matched, _ := doublestar.Match(pattern, relPath); matched {
			return true
		}
	}
	return false
}

// WithTargetIgnores returns a new ProjectConfig with the target's ignores merged in.
// The returned config's IsIgnored checks both global and per-target patterns.
func (pc *ProjectConfig) WithTargetIgnores(td TargetDef) *ProjectConfig {
	if len(td.Ignores) == 0 {
		return pc
	}
	merged := &ProjectConfig{
		Targets: pc.Targets,
		Ignores: make([]string, 0, len(pc.Ignores)+len(td.Ignores)),
	}
	merged.Ignores = append(merged.Ignores, pc.Ignores...)
	merged.Ignores = append(merged.Ignores, td.Ignores...)
	return merged
}

// FindChangedProjects determines which projects have files in the changed file list.
// Files matching ignore globs in .goodchangesrc.json are excluded.
// If relevantPackages is non-nil, only projects in that set are considered.
func FindChangedProjects(config *Config, projectMap map[string]*ProjectInfo, changedFiles []string, configMap map[string]*ProjectConfig, relevantPackages map[string]bool) map[string]*ProjectInfo {
	result := make(map[string]*ProjectInfo)
	for _, file := range changedFiles {
		if file == "" {
			continue
		}
		for _, rp := range config.Projects {
			if strings.HasPrefix(file, rp.ProjectFolder+"/") {
				if relevantPackages != nil && !relevantPackages[rp.PackageName] {
					break
				}
				relPath := strings.TrimPrefix(file, rp.ProjectFolder+"/")
				cfg := configMap[rp.ProjectFolder]
				if cfg.IsIgnored(relPath) {
					break
				}
				if _, exists := result[rp.PackageName]; !exists {
					result[rp.PackageName] = projectMap[rp.PackageName]
				}
				break
			}
		}
	}
	return result
}

// FindTransitiveDependencies returns all packages that the seed packages transitively depend on.
// The seeds themselves are included in the result. Walks DependsOn edges (downward).
func FindTransitiveDependencies(projectMap map[string]*ProjectInfo, seeds []string) map[string]bool {
	visited := make(map[string]bool)
	queue := make([]string, 0, len(seeds))
	for _, s := range seeds {
		visited[s] = true
		queue = append(queue, s)
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		info := projectMap[current]
		if info == nil {
			continue
		}
		for _, dep := range info.DependsOn {
			if !visited[dep] {
				visited[dep] = true
				queue = append(queue, dep)
			}
		}
	}
	return visited
}

// FindTransitiveDependents returns all packages that transitively depend on any of the seed packages.
// The seeds themselves are included in the result.
func FindTransitiveDependents(projectMap map[string]*ProjectInfo, seeds []string) map[string]bool {
	visited := make(map[string]bool)
	queue := make([]string, 0, len(seeds))
	for _, s := range seeds {
		visited[s] = true
		queue = append(queue, s)
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		info := projectMap[current]
		if info == nil {
			continue
		}
		for _, dep := range info.DependedOnBy {
			if !visited[dep] {
				visited[dep] = true
				queue = append(queue, dep)
			}
		}
	}
	return visited
}

// TopologicalSort returns packages grouped by level (dependencies first).
// Level 0 = packages with no dependencies on other packages in the set.
// Only considers dependencies within the given set.
func TopologicalSort(projectMap map[string]*ProjectInfo, packages map[string]bool) [][]string {
	inDegree := make(map[string]int)
	for p := range packages {
		inDegree[p] = 0
	}
	for p := range packages {
		info := projectMap[p]
		if info == nil {
			continue
		}
		for _, dep := range info.DependsOn {
			if packages[dep] {
				inDegree[p]++
			}
		}
	}

	remaining := make(map[string]bool)
	for p := range packages {
		remaining[p] = true
	}

	var levels [][]string
	for len(remaining) > 0 {
		var level []string
		for p := range remaining {
			if inDegree[p] == 0 {
				level = append(level, p)
			}
		}
		if len(level) == 0 {
			// Cycle — dump remaining to avoid infinite loop
			for p := range remaining {
				level = append(level, p)
			}
		}
		levels = append(levels, level)
		for _, p := range level {
			delete(remaining, p)
			info := projectMap[p]
			if info == nil {
				continue
			}
			for _, dependent := range info.DependedOnBy {
				if remaining[dependent] {
					inDegree[dependent]--
				}
			}
		}
	}

	return levels
}

func stripJSONCommentsAndTrailingCommas(data []byte) []byte {
	s := string(data)
	lines := strings.Split(s, "\n")
	var result []string
	inBlockComment := false
	for _, line := range lines {
		if inBlockComment {
			if idx := strings.Index(line, "*/"); idx != -1 {
				line = line[idx+2:]
				inBlockComment = false
			} else {
				continue
			}
		}
		var cleaned strings.Builder
		inString := false
		escaped := false
		i := 0
		for i < len(line) {
			ch := line[i]
			if escaped {
				cleaned.WriteByte(ch)
				escaped = false
				i++
				continue
			}
			if inString {
				cleaned.WriteByte(ch)
				if ch == '\\' {
					escaped = true
				} else if ch == '"' {
					inString = false
				}
				i++
				continue
			}
			if ch == '"' {
				inString = true
				cleaned.WriteByte(ch)
				i++
				continue
			}
			if ch == '/' && i+1 < len(line) {
				if line[i+1] == '/' {
					break
				}
				if line[i+1] == '*' {
					end := strings.Index(line[i+2:], "*/")
					if end != -1 {
						i = i + 2 + end + 2
						continue
					}
					inBlockComment = true
					break
				}
			}
			cleaned.WriteByte(ch)
			i++
		}
		result = append(result, cleaned.String())
	}
	joined := strings.Join(result, "\n")
	re := regexp.MustCompile(",\\s*([\\]})])")
	joined = re.ReplaceAllString(joined, "$1")
	return []byte(joined)
}
