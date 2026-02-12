# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.2] - 2026-02-12

### Fixed
- Fix runner label for GitHub release job (`runners-cxa-xlarge`, not `cxa-xlarge`)

## [0.2.1] - 2026-02-12

### Changed
- Use cxa-xlarge runner for GitHub release job (cross-compiling 32 binaries)
- Docker images limited to linux/amd64 and linux/arm64 only (other platforms served via standalone binaries)

## [0.2.0] - 2026-02-12

### Added
- Cross-platform standalone binaries attached to GitHub releases (32 targets)
- Support for Linux, macOS, Windows, FreeBSD, OpenBSD, NetBSD, Solaris, Illumos, AIX, DragonFlyBSD

### Changed
- Docker build uses Go cross-compilation instead of QEMU emulation for faster multi-platform builds

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

[0.2.1]: https://github.com/gooddata/gooddata-goodchanges/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/gooddata/gooddata-goodchanges/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/gooddata/gooddata-goodchanges/releases/tag/v0.1.0
