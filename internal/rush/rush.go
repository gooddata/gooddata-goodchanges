package rush

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// FindChangedProjects determines which projects have files in the changed file list.
func FindChangedProjects(config *Config, projectMap map[string]*ProjectInfo, changedFiles []string) map[string]*ProjectInfo {
	result := make(map[string]*ProjectInfo)
	for _, file := range changedFiles {
		if file == "" {
			continue
		}
		for _, rp := range config.Projects {
			if strings.HasPrefix(file, rp.ProjectFolder+"/") {
				if _, exists := result[rp.PackageName]; !exists {
					result[rp.PackageName] = projectMap[rp.PackageName]
				}
				break
			}
		}
	}
	return result
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
