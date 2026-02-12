# goodchanges

Granular change detection for Rush monorepos. Analyzes code changes at the AST level to determine which library exports are affected by a pull request, then propagates taint through the workspace dependency graph to identify which e2e test targets need to run.

## How it works

1. Finds the merge base commit (comparison point)
2. Gets the list of changed files
3. Loads `rush.json` and builds the workspace dependency graph
4. Identifies directly changed projects and lockfile dependency changes
5. Computes the full affected subgraph (transitive dependents)
6. Topologically sorts affected packages (dependencies first)
7. For each **library**: parses old and new TypeScript ASTs, diffs symbols, and propagates taint through import graphs
8. For each **target**: checks if it's affected via direct changes, lockfile changes, tainted imports, or a tainted corresponding app
9. Outputs a JSON array of affected e2e package names to stdout

## Output

```json
["gdc-dashboards-e2e", "home-ui-e2e", "sdk-ui-tests-e2e"]
```

## Environment variables

| Variable | Description | Default |
|---|---|---|
| `LOG_LEVEL` | Logging verbosity. `BASIC` for standard logging, `DEBUG` for verbose AST/taint tracing to stderr | _(no logging)_ |
| `INCLUDE_TYPES` | When set to any non-empty value, includes type-only changes (interfaces, type aliases, type annotations) in taint propagation | _(disabled)_ |
| `INCLUDE_CSS` | When set to any non-empty value, enables CSS/SCSS change detection and taint propagation through `@use`/`@import` chains | _(disabled)_ |
| `COMPARE_COMMIT` | Specific git commit hash to compare against (overrides branch-based comparison) | _(empty)_ |
| `COMPARE_BRANCH` | Git branch to compute merge base against | `origin/master` |

## Library vs app detection

A package is classified as a **library** if its `package.json` contains any of:

- `types` (TypeScript type declarations)
- `exports` (modern package exports field)
- `module` (ES module entry)

Libraries get full AST-level analysis: entrypoint resolution, symbol diffing, and taint propagation through their internal import graph.

Everything else is an **app** (bundled). Apps are not analyzed for granular exports -- if any file in an app changes, the app is considered fully tainted.

## Configuration

Each project can optionally have a `.goodchangesrc.json` file in its root directory.

### Target

Marks a project as an e2e test package. The package name is included in the output when any of the 4 trigger conditions are met.

```json
{
  "type": "target",
  "app": "@gooddata/gdc-dashboards",
  "ignores": ["scenarios/**/*.md"]
}
```

**Trigger conditions:**

1. **Direct file changes** -- files changed in the project folder (excluding ignored paths)
2. **External dependency changes** -- a dependency version changed in `pnpm-lock.yaml`
3. **Tainted workspace imports** -- the target imports a tainted symbol from a workspace library
4. **Corresponding app is tainted** -- the app specified by `app` is affected (any of the above conditions)

### Virtual target

An aggregated target that watches specific directories across a project. Does not correspond to a real package name in the output -- uses `targetName` instead.

```json
{
  "type": "virtual-target",
  "targetName": "sdk-ui-tests-e2e",
  "changeDirs": ["scenarios", "stories"]
}
```

**Trigger conditions:**

- Any file in a `changeDirs` directory is changed
- Any file in a `changeDirs` directory imports a tainted symbol

### Fields reference

| Field | Type | Used by | Description |
|---|---|---|---|
| `type` | `"target"` \| `"virtual-target"` | Both | Declares what kind of target this project is |
| `app` | `string` | Target | Package name of the corresponding app this e2e package tests |
| `targetName` | `string` | Virtual target | Output name emitted when the virtual target is triggered |
| `changeDirs` | `string[]` | Virtual target | Directories (relative to project root) to watch for changes |
| `ignores` | `string[]` | Both | Glob patterns for files to exclude from change detection |

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
- **External deps**: lockfile version changes taint all imports from the affected package

### CSS/SCSS taint (opt-in)

When `INCLUDE_CSS` is set:

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
  -e LOG_LEVEL=BASIC \
  -e COMPARE_BRANCH=origin/master \
  gooddata/gooddata-goodchanges:latest
```

## Example configurations

Example `.goodchangesrc.json` files can be found in:

- `testing_pre_merge/configFiles/` -- configurations used for pre-merge (PR) analysis
- `testing_post_merge/configFiles/` -- configurations used for post-merge analysis

Example analysis reports are in `testing_pre_merge/prs/` and `testing_post_merge/prs/`.

## Project structure

```
main.go                          # Entry point, orchestration
internal/
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
vendor-tsgo.sh                   # Vendor script for typescript-go
TSGO_COMMIT                      # Pinned typescript-go commit hash
Dockerfile                       # Multi-stage Docker build
```
