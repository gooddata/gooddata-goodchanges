#!/bin/sh
set -e

OWNER="gooddata"
REPO="gooddata-goodchanges"
BINARY="goodchanges"
BINDIR="${BINDIR:-/usr/local/bin}"

usage() {
  cat <<EOF
Usage: $0 [version]

Install ${BINARY} binary.

  [version]  Tag to install (e.g. v0.2.5). Defaults to latest release.

Environment:
  BINDIR     Installation directory (default: /usr/local/bin)

Examples:
  $0              # install latest
  $0 v0.2.5       # install specific version
  BINDIR=~/.local/bin $0
EOF
  exit 2
}

case "${1:-}" in
  -h|--help) usage ;;
esac

# --- detect os/arch ---

detect_os() {
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  case "$os" in
    darwin|linux) ;;
    mingw*|msys*|cygwin*) os="windows" ;;
    *) echo "error: unsupported OS: $os" >&2; exit 1 ;;
  esac
  echo "$os"
}

detect_arch() {
  arch=$(uname -m)
  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *) echo "error: unsupported architecture: $arch" >&2; exit 1 ;;
  esac
  echo "$arch"
}

# --- resolve version ---

resolve_version() {
  if [ -n "${1:-}" ]; then
    echo "$1"
    return
  fi
  # GitHub redirects /releases/latest to the latest tag; grab it from the JSON response
  tag=$(curl -sSL "https://api.github.com/repos/${OWNER}/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"//;s/".*//')
  if [ -z "$tag" ]; then
    echo "error: could not determine latest release" >&2
    exit 1
  fi
  echo "$tag"
}

# --- download helpers ---

download() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$1" "$2"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$1" "$2"
  else
    echo "error: curl or wget required" >&2
    exit 1
  fi
}

verify_checksum() {
  target="$1"
  checksum_file="$2"
  want=$(cut -d ' ' -f 1 < "$checksum_file")
  if command -v sha256sum >/dev/null 2>&1; then
    got=$(sha256sum "$target" | cut -d ' ' -f 1)
  elif command -v shasum >/dev/null 2>&1; then
    got=$(shasum -a 256 "$target" | cut -d ' ' -f 1)
  else
    echo "error: no sha256 tool found (need sha256sum or shasum), cannot verify download" >&2
    exit 1
  fi
  if [ "$want" != "$got" ]; then
    echo "error: checksum mismatch" >&2
    echo "  expected: $want" >&2
    echo "  got:      $got" >&2
    exit 1
  fi
}

# --- main ---

OS=$(detect_os)
ARCH=$(detect_arch)
VERSION=$(resolve_version "${1:-}")

case "$OS" in
  windows) ext="zip" ;;
  *)       ext="tar.gz" ;;
esac

asset="${BINARY}-${OS}-${ARCH}.${ext}"
base_url="https://github.com/${OWNER}/${REPO}/releases/download/${VERSION}"

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

echo "Installing ${BINARY} ${VERSION} (${OS}/${ARCH})..."

download "${tmpdir}/${asset}" "${base_url}/${asset}"
download "${tmpdir}/${asset}.sha256" "${base_url}/${asset}.sha256"
verify_checksum "${tmpdir}/${asset}" "${tmpdir}/${asset}.sha256"

# extract
case "$ext" in
  tar.gz) tar -xzf "${tmpdir}/${asset}" -C "$tmpdir" ;;
  zip)    unzip -oq "${tmpdir}/${asset}" -d "$tmpdir" ;;
esac

# install
mkdir -p "$BINDIR" 2>/dev/null || sudo mkdir -p "$BINDIR"
srcname="${BINARY}-${OS}-${ARCH}"
binexe="${BINARY}"
[ "$OS" = "windows" ] && srcname="${srcname}.exe" && binexe="${binexe}.exe"

mv "${tmpdir}/${srcname}" "${BINDIR}/${binexe}" 2>/dev/null \
  || sudo mv "${tmpdir}/${srcname}" "${BINDIR}/${binexe}"

echo "Installed ${BINDIR}/${binexe}"
