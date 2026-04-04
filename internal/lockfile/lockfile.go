package lockfile

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// PnpmLockfile represents a parsed pnpm-lock.yaml.
type PnpmLockfile struct {
	LockfileVersion any                      `yaml:"lockfileVersion"`
	Importers       map[string]ImporterEntry  `yaml:"importers"`
	Snapshots       map[string]SnapshotEntry  `yaml:"snapshots"`
}

// ImporterEntry represents a project in the importers section.
type ImporterEntry struct {
	Dependencies         map[string]DepRef `yaml:"dependencies"`
	DevDependencies      map[string]DepRef `yaml:"devDependencies"`
	OptionalDependencies map[string]DepRef `yaml:"optionalDependencies"`
}

// DepRef represents a dependency reference with specifier and resolved version.
type DepRef struct {
	Specifier string `yaml:"specifier"`
	Version   string `yaml:"version"`
}

// SnapshotEntry represents a resolved package in the snapshots section.
type SnapshotEntry struct {
	Dependencies         map[string]string `yaml:"dependencies"`
	OptionalDependencies map[string]string `yaml:"optionalDependencies"`
}

// ParseLockfile parses pnpm-lock.yaml content into a structured format.
// Returns nil if content is empty or parsing fails.
func ParseLockfile(content []byte) *PnpmLockfile {
	if len(content) == 0 {
		return nil
	}
	var lf PnpmLockfile
	if err := yaml.Unmarshal(content, &lf); err != nil {
		return nil
	}
	return &lf
}

// Version returns the lockfileVersion as a string.
// Returns empty string if the lockfile is nil or has no version.
func (lf *PnpmLockfile) Version() string {
	if lf == nil || lf.LockfileVersion == nil {
		return ""
	}
	return fmt.Sprintf("%v", lf.LockfileVersion)
}

// FindDepChanges compares old and new lockfiles to find which projects had
// dependency version changes (direct or transitive).
// Returns a map of project folder → set of changed direct dependency names.
// Workspace deps (version: link:...) are excluded.
// The subspace parameter resolves importer paths (relative to common/temp/{subspace}/).
func FindDepChanges(oldLf, newLf *PnpmLockfile, subspace string) map[string]map[string]bool {
	if newLf == nil {
		return nil
	}

	importerBase := filepath.Join("common", "temp", subspace)
	result := make(map[string]map[string]bool)

	var oldImporters map[string]ImporterEntry
	var oldSnapshots map[string]SnapshotEntry
	if oldLf != nil {
		oldImporters = oldLf.Importers
		oldSnapshots = oldLf.Snapshots
	}

	for importerPath, newImporter := range newLf.Importers {
		projectFolder := resolveImporterPath(importerPath, importerBase)
		if projectFolder == "" {
			continue
		}

		oldImporter := oldImporters[importerPath]
		newDeps := mergeImporterDeps(newImporter)
		oldDeps := mergeImporterDeps(oldImporter)

		for depName, newRef := range newDeps {
			if strings.HasPrefix(newRef.Version, "link:") {
				continue
			}

			oldRef := oldDeps[depName]

			// Direct version change
			if oldRef.Version != newRef.Version {
				if result[projectFolder] == nil {
					result[projectFolder] = make(map[string]bool)
				}
				result[projectFolder][depName] = true
				continue
			}

			// Check transitive deps for changes
			if len(newLf.Snapshots) > 0 {
				snapshotKey := depName + "@" + newRef.Version
				if hasTransitiveChanges(snapshotKey, oldSnapshots, newLf.Snapshots) {
					if result[projectFolder] == nil {
						result[projectFolder] = make(map[string]bool)
					}
					result[projectFolder][depName] = true
				}
			}
		}
	}

	return result
}

func resolveImporterPath(importerPath, importerBase string) string {
	importerPath = strings.Trim(importerPath, "'\"")
	if importerPath == "." {
		return ""
	}
	resolved := filepath.Clean(filepath.Join(importerBase, importerPath))
	if rel, err := filepath.Rel(".", resolved); err == nil {
		return rel
	}
	return resolved
}

func mergeImporterDeps(entry ImporterEntry) map[string]DepRef {
	result := make(map[string]DepRef)
	for name, ref := range entry.Dependencies {
		result[name] = ref
	}
	for name, ref := range entry.DevDependencies {
		result[name] = ref
	}
	for name, ref := range entry.OptionalDependencies {
		result[name] = ref
	}
	return result
}

// hasTransitiveChanges checks if any package in the transitive closure of
// startKey has different snapshot entries between old and new lockfiles.
func hasTransitiveChanges(startKey string, oldSnapshots, newSnapshots map[string]SnapshotEntry) bool {
	visited := make(map[string]bool)
	queue := []string{startKey}

	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]

		if visited[key] {
			continue
		}
		visited[key] = true

		oldEntry, oldExists := oldSnapshots[key]
		newEntry, newExists := newSnapshots[key]

		if oldExists != newExists {
			return true
		}
		if !newExists {
			continue
		}

		if !stringMapsEqual(oldEntry.Dependencies, newEntry.Dependencies) {
			return true
		}
		if !stringMapsEqual(oldEntry.OptionalDependencies, newEntry.OptionalDependencies) {
			return true
		}

		for name, version := range newEntry.Dependencies {
			queue = append(queue, name+"@"+version)
		}
		for name, version := range newEntry.OptionalDependencies {
			queue = append(queue, name+"@"+version)
		}
	}

	return false
}

func stringMapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
