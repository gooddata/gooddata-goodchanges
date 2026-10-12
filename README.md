# goodchanges

Granular change detection for Rush monorepos. Analyzes code changes at the AST level to determine which library exports are affected by a pull request, then propagates taint through the workspace dependency graph to identify which e2e test targets need to run.

## Install

```bash
# latest version
curl -fsSL https://raw.githubusercontent.com/gooddata/gooddata-goodchanges/master/install.sh | sh

# specific version
curl -fsSL https://raw.githubusercontent.com/gooddata/gooddata-goodchanges/master/install.sh | sh -s v0.2.5

# custom install directory
curl -fsSL https://raw.githubusercontent.com/gooddata/gooddata-goodchanges/master/install.sh | BINDIR=~/.local/bin sh
```

Or run locally:

```bash
BINDIR=~/.local/bin ./install.sh v0.2.5
```

## Usage

The CLI is command-based: `goodchanges <command> [flags]`.

```bash
goodchanges detect [flags]     # run change detection, outputs JSON to stdout
goodchanges list-projects      # print the rush project list as JSON
goodchanges version            # print version (also: goodchanges -v / --version)

goodchanges --help             # list commands
goodchanges <command> --help   # a command's flags, generated from its schema
```

Unknown flags and unexpected positional arguments fail fast with a non-zero
exit code. Everything after a bare `--` is treated as positional.

### detect

Runs change detection against the rush monorepo in the current working
directory and prints the affected targets as JSON on stdout.

```bash
goodchanges detect --include-css --compare-commit <sha> --targets 'gdc-dashboards-e2e,*backstop*'
```

| Flag                        | Description                                                                                                          | Default         |
|-----------------------------|----------------------------------------------------------------------------------------------------------------------|-----------------|
| `--targets <list>`          | Comma-delimited list of target names to include in output. Supports `*` wildcard (e.g. `*backstop*,@gooddata/sdk-*`) | _(all targets)_ |
| `--compare-commit <commit>` | Specific git commit to compare against (overrides branch-based comparison)                                           | _(empty)_       |
| `--compare-branch <branch>` | Git branch to compute merge base against                                                                             | `origin/master` |
| `--include-css`             | Enables CSS/SCSS change detection and taint propagation through `@use`/`@import` chains                              | _(disabled)_    |
| `--include-types`           | Includes type-only changes (interfaces, type aliases, type annotations) in taint propagation                         | _(disabled)_    |
| `--log-level <level>`       | Logging verbosity on stderr. `BASIC` for standard logging, `DEBUG` for verbose AST/taint tracing                     | _(no logging)_  |

> Until v0.25.x these inputs were environment variables (`TARGETS`,
> `COMPARE_COMMIT`, `COMPARE_BRANCH`, `INCLUDE_CSS`, `INCLUDE_TYPES`,
> `LOG_LEVEL`) on a bare `goodchanges` invocation, and `list-projects` was
> `goodchanges --list`. The env vars are no longer read.

## How it works

`goodchanges detect`:

1. Finds the merge base commit (comparison point)
2. Gets the list of changed files
3. Loads `rush.json` and builds the workspace dependency graph
4. Identifies directly changed projects and lockfile dependency changes
5. Computes the full affected subgraph (transitive dependents)
6. Topologically sorts affected packages (dependencies first)
7. For each **library**: parses old and new TypeScript ASTs, diffs symbols, and propagates taint through import graphs
8. For each **target**: checks if it's affected via direct changes, lockfile changes, or tainted imports
9. Outputs a JSON array of affected e2e package names to stdout

## Output

`detect` prints a JSON array of target objects on stdout:

```json
[
  {"name": "gdc-dashboards-e2e"},
  {"name": "neobackstop", "detections": ["stories/Button.stories.tsx", "stories/Dialog.stories.tsx"]}
]
```

- Normal targets and fully-triggered virtual targets: `{"name": "..."}`
- Virtual targets where only fine-grained directories detected changes: `{"name": "...", "detections": ["..."]}` with the specific affected file paths

## Library vs app detection

Detection can be overridden by setting the top-level `type` field in `.goodchangesrc.json` to `"library"` or `"app"` (see [Configuration](#configuration)). When unset, classification is inferred.

A package is inferred as a **library** if its `package.json` contains any of:

- `types` (TypeScript type declarations)
- `exports` (modern package exports field)
- `module` (ES module entry)

Libraries get full AST-level analysis: entrypoint resolution, symbol diffing, and taint propagation through their internal import graph.

Everything else is inferred as an **app** (bundled). Apps are not analyzed for granular exports -- if any file in an app changes, the app is considered fully tainted.

## Configuration

Each project can optionally have a `.goodchangesrc.json` file in its root directory. A single config file can define multiple targets via the `targets` array.

```json
{
  "targets": [
    {
      "targetName": "neobackstop",
      "changeDirs": [
        { "glob": "src/**/*" },
        { "glob": "scenarios/**/*" },
        { "glob": "stories/**/*.stories.tsx", "type": "fine-grained" },
        { "glob": "neobackstop/**/*" }
      ]
    }
  ],
  "ignores": ["scenarios/**/*.md"]
}
```

### Global changeDirs

Top-level `changeDirs` apply to the entire package. When any changed file matches a global changeDir glob, all library exports are wildcard-tainted and all targets are triggered. This is useful for files that affect everything but aren't tracked by the AST analysis (e.g. locale bundles, config files).

```json
{
  "changeDirs": [
    { "glob": "src/localization/bundles/en-US.json" }
  ],
  "targets": [
    { "targetName": "my-tests", "changeDirs": [{ "glob": "**/*", "type": "fine-grained", "filter": "**/*.test.ts*" }] }
  ]
}
```

### Trigger conditions

Each target is triggered by any of these conditions:

1. **Direct file changes** -- files matching `changeDirs` globs changed (excluding ignored paths). Defaults to `**/*` (entire project) when `changeDirs` is not set.
2. **External dependency changes** -- a dependency version changed in `pnpm-lock.yaml`
3. **Tainted workspace imports** -- a file matching `changeDirs` globs imports a tainted symbol from a workspace library

### changeDirs

Each `changeDirs` entry is an object with:

- `glob` -- glob pattern to match files (relative to project root). Uses doublestar syntax: `*` matches files in current directory only, `**/*` matches all nested files, `**/*.stories.tsx` matches specific patterns recursively.
- `filter` -- optional output filter glob (fine-grained only). When set, the `glob` defines the analysis scope and `filter` narrows which affected files appear in the output. Example: `{"glob": "src/**/*", "filter": "src/**/*.test.ts", "type": "fine-grained"}` analyzes all files in `src/` but only returns affected test files.
- `type` -- optional, set to `"fine-grained"` for granular file-level detection

**Ignores override globs:** if a file matches a `changeDirs` glob but also matches an `ignores` pattern, the file is excluded.

**Normal globs** (no `type` or omitted): any matching file change or tainted import triggers a full run.

**Fine-grained globs** (`"type": "fine-grained"`): instead of triggering a full run, collects the specific affected TS/TSX source files. A file is affected if it:
- Was directly changed
- Imports tainted symbols from upstream workspace libraries
- Imports from a file that is affected (transitive within the matched set)

**Output behavior:**
- If any **normal** glob triggers: `{"name": "neobackstop"}` (full run, no detections)
- If **only fine-grained** globs have detections: `{"name": "neobackstop", "detections": ["stories/Button.stories.tsx"]}` (specific files)

### Fields reference

**Top-level fields:**

| Field        | Type                 | Description                                                                                                                                                                    |
|--------------|----------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `type`       | `"library" \| "app"` | Optional. Forces this package's classification, skipping the inference described in [Library vs app detection](#library-vs-app-detection). Invalid values cause a fatal error. |
| `targets`    | `TargetDef[]`        | Array of target definitions (see below)                                                                                                                                        |
| `ignores`    | `string[]`           | Glob patterns for files to exclude from change detection                                                                                                                       |
| `changeDirs` | `ChangeDir[]`        | Global changeDirs. When triggered, taints all library exports and triggers all targets in this package.                                                                        |

**TargetDef fields (each entry in `targets`):**

| Field        | Type          | Description                                                                                                                                 |
|--------------|---------------|---------------------------------------------------------------------------------------------------------------------------------------------|
| `targetName` | `string`      | Custom output name (defaults to the package name when not set)                                                                              |
| `changeDirs` | `ChangeDir[]` | Glob patterns to match files. Defaults to `**/*` (entire project). Each entry: `{"glob": "...", "filter?": "...", "type?": "fine-grained"}` |
| `ignores`    | `string[]`    | Per-target ignore globs. Additive with the global `ignores` -- only applies to this target's detection                                      |

The `.goodchangesrc.json` file itself is always ignored.

## How analysis works

### Entrypoint resolution

Library entrypoints are resolved from `package.json`:

1. If `exports` field exists, all export paths are parsed (supports nested conditional exports)
2. Otherwise, falls back to `main`, `module`, `browser`, `types` fields

Build output paths (e.g. `dist/index.js`) are resolved back to source files (e.g. `src/index.ts`) by trying candidates in order: `src/` prefix, original path, and index files.

### AST diffing

For each changed `.ts`/`.tsx`/`.js`/`.jsx` file in a library:

1. Fetches the old file content from git at the merge base
2. Parses both old and new versions into ASTs using the vendored TypeScript parser
3. Compares each symbol's body text to detect changes
4. Distinguishes runtime changes from type-only changes (stripping type annotations, casts, generics)

### Taint propagation

Taint spreads through the import graph via unlimited BFS hops:

- **Named imports**: if `import { Button } from "./components"` and `Button` is tainted, symbols in the importing file that reference `Button` become tainted
- **Namespace imports**: `import * as X from "./foo"` -- any taint in `foo` propagates
- **Side-effect imports**: `import "./setup"` -- if the imported file is tainted, all symbols in the importing file are tainted
- **Re-exports**: `export { X } from "./foo"` and `export * from "./foo"` are tracked as import edges
- **Cross-package**: taint from upstream workspace dependencies is passed into downstream packages
- **Intra-file**: if symbol A is tainted and symbol B references A in its body, B becomes tainted
- **External deps**: lockfile dependency changes (detected by YAML-diffing old and new `pnpm-lock.yaml`, including transitive deps via BFS) taint all imports from the affected package

### CSS/SCSS taint (opt-in)

When `--include-css` is set:

- Any changed `.css`/`.scss` file taints the entire package's styles
- Style imports (`*.css`, `*.scss`, paths containing `/styles/`) from tainted packages are detected
- SCSS `@use` and `@import` chains are followed transitively across packages

## Vendored TypeScript parser

The tool vendors [microsoft/typescript-go](https://github.com/microsoft/typescript-go) for AST parsing. The pinned commit hash is stored in `TSGO_COMMIT`.

### Why vendoring is necessary

We cannot use `typescript-go` as a regular Go dependency because its parser, AST types, and all other packages live under `internal/`. Go enforces that `internal/` packages can only be imported by code within the same module, so no external project can `import "github.com/microsoft/typescript-go/internal/parser"`. The project does not expose a public Go API — it is structured as a standalone tool, not a library.

The vendor script works around this by shallow-cloning the repository, renaming `internal/` to `pkg/`, and rewriting all import paths. This makes the parser packages importable from our module via a local `replace` directive in `go.mod`.

### Vendoring

```bash
# Vendor using the pinned commit
bash vendor-tsgo.sh

# Update to latest and vendor
bash vendor-tsgo.sh --update
```

The vendor script:

1. Reads the commit hash from `TSGO_COMMIT`
2. Shallow-clones that specific commit
3. Renames `internal/` to `pkg/` (to make packages importable)
4. Rewrites import paths and module name

### Automated upgrades

The `vendor ~ upgrade` workflow (`.github/workflows/vendor-upgrade.yml`) can be triggered manually:

- **Input**: `commit_hash` (defaults to `"latest"` which resolves to newest main)
- Updates `TSGO_COMMIT`, runs the vendor script, updates Go version in `Dockerfile` and `go.mod`, runs `go mod tidy`, and opens a PR

## Docker

```dockerfile
# Build stage: Go + git + bash
FROM golang:X.Y.Z-alpine AS builder
# Vendors typescript-go, builds the binary

# Runtime stage: Alpine + git
FROM alpine:3.23
# Runs /usr/bin/goodchanges
```

Usage:

```bash
docker run --rm \
  -v /path/to/rush-monorepo:/repo \
  -w /repo \
  gooddata/gooddata-goodchanges:latest \
  detect --log-level BASIC --compare-branch origin/master
```

The image's default command is `detect`, so a bare `docker run ... gooddata/gooddata-goodchanges:latest`
runs change detection with default flags. Pass any other command (e.g. `version`) to override.

## Example configurations

Example `.goodchangesrc.json` files can be found in:

- `testing_pre_merge/configFiles/` -- configurations used for pre-merge (PR) analysis
- `testing_post_merge/configFiles/` -- configurations used for post-merge analysis

Example analysis reports are in `testing_pre_merge/prs/` and `testing_post_merge/prs/`.

## Project structure

```
main.go                          # Dispatcher: command map, argv parse/validate, --help/usage
cmd/
  detect/
    detect.go                    # `detect` command: change-detection orchestration + schema
  list_projects/
    list_projects.go             # `list-projects` command: rush project dump + schema
  version/
    version.go                   # `version` command (also -v/--version) + schema
internal/
  cli/
    types.go                     # Flag/Schema types (single source of truth per command)
    parse.go                     # Schema-aware argv parsing (unknown flags fail fast)
    validate.go                  # Arity, required, repeatable, allowed-values checks
    help.go                      # Per-command --help rendering from the schema
  analyzer/
    analyzer.go                  # Library analysis, taint propagation, CSS tracking
    astdiff.go                   # AST-level symbol diffing, type-only detection
    resolve.go                   # Entrypoint and import path resolution
  diff/
    diff.go                      # Unified diff parser (line ranges)
  git/
    git.go                       # Git operations (merge-base, diff, show)
  lockfile/
    lockfile.go                  # pnpm-lock.yaml parser, dep change detection
  rush/
    rush.go                      # Rush config, dependency graph, project configs
  tsparse/
    tsparse.go                   # TypeScript parser (imports, exports, symbols)
install.sh                       # Standalone binary installer
vendor-tsgo.sh                   # Vendor script for typescript-go
TSGO_COMMIT                      # Pinned typescript-go commit hash
Dockerfile                       # Multi-stage Docker build
```
