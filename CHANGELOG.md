# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-02-11

### Added
- Initial release
- AST-level change detection for Rush monorepo libraries using vendored typescript-go parser
- Taint propagation through workspace dependency graph (unlimited BFS hops)
- Target and virtual target support via `.goodchangesrc.json` configuration
- Lockfile dependency change detection (pnpm-lock.yaml)
- Optional CSS/SCSS taint tracking and propagation through `@use`/`@import` chains
- Optional type-only change detection (interfaces, type aliases, annotations)
- Multi-stage Docker build
- Automated vendor upgrade workflow

[0.1.0]: https://github.com/gooddata/gooddata-goodchanges/releases/tag/v0.1.0
