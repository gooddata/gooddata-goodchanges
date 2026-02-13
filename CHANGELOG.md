# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.6] - 2026-02-13

### Changed
- Upgrade vendored typescript-go to [`f058889a79ed`](https://github.com/microsoft/typescript-go/commit/f058889a79edf8fef07d4868e39574db00d43454)

## [0.2.5] - 2026-02-13

### Changed
- Upgrade vendored typescript-go to [`f058889a79ed`](https://github.com/microsoft/typescript-go/commit/f058889a79edf8fef07d4868e39574db00d43454)

## [0.2.4] - 2026-02-12

### Changed
- Trim release binaries to 6 targets: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64, windows/arm64

## [0.2.3] - 2026-02-12

### Added
- SHA-256 hash files (`.sha256`) for every release binary

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

[0.2.6]: https://github.com/gooddata/gooddata-goodchanges/compare/v0.2.5...v0.2.6
[0.2.5]: https://github.com/gooddata/gooddata-goodchanges/compare/v0.2.4...v0.2.5
[0.2.4]: https://github.com/gooddata/gooddata-goodchanges/compare/v0.2.3...v0.2.4
[0.2.3]: https://github.com/gooddata/gooddata-goodchanges/compare/v0.2.2...v0.2.3
[0.2.2]: https://github.com/gooddata/gooddata-goodchanges/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/gooddata/gooddata-goodchanges/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/gooddata/gooddata-goodchanges/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/gooddata/gooddata-goodchanges/releases/tag/v0.1.0
