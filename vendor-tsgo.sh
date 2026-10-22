#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMMIT_FILE="$SCRIPT_DIR/TSGO_COMMIT"

# Portable in-place sed (macOS needs -i '', Linux needs -i)
if [[ "$(uname)" == "Darwin" ]]; then
  SEDI=(sed -i '')
else
  SEDI=(sed -i)
fi

# Read pinned commit hash
if [[ ! -f "$COMMIT_FILE" ]]; then
  echo "Error: TSGO_COMMIT file not found." >&2
  exit 1
fi
TSGO_REF=$(tr -d '[:space:]' < "$COMMIT_FILE")
if [[ -z "$TSGO_REF" ]]; then
  echo "Error: TSGO_COMMIT is empty." >&2
  exit 1
fi

VENDOR_DIR="_vendor/typescript-go"

# The Go port lives in the tsc/ subdirectory of microsoft/TypeScript
# (microsoft/typescript-go was the staging repo and is now archived).
UPSTREAM_REPO="https://github.com/microsoft/TypeScript.git"
UPSTREAM_SUBDIR="tsc"
UPSTREAM_MODULE="github.com/microsoft/TypeScript/tsc"

# Clean previous vendor
rm -rf "$VENDOR_DIR"
mkdir -p "_vendor"

# Fetch only what we need: a single commit (no history), with blobs fetched
# lazily (--filter=blob:none) and a sparse checkout restricted to tsc/, so
# nothing outside that subdirectory is ever downloaded.
CLONE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/tsgo-vendor.XXXXXX")"
trap 'rm -rf "$CLONE_DIR"' EXIT
echo "Fetching $UPSTREAM_SUBDIR/ from microsoft/TypeScript at $TSGO_REF..."
git init -q "$CLONE_DIR"
git -C "$CLONE_DIR" remote add origin "$UPSTREAM_REPO"
git -C "$CLONE_DIR" sparse-checkout set "$UPSTREAM_SUBDIR"
git -C "$CLONE_DIR" fetch -q --depth 1 --filter=blob:none origin "$TSGO_REF"
git -C "$CLONE_DIR" checkout -q FETCH_HEAD

# Move the subdirectory into place; the temp clone (and its .git) is discarded
mv "$CLONE_DIR/$UPSTREAM_SUBDIR" "$VENDOR_DIR"

# Remove stuff we don't need
rm -rf "$VENDOR_DIR/_tools" "$VENDOR_DIR/cmd" "$VENDOR_DIR/_submodules" "$VENDOR_DIR/testdata"

# Rename internal/ to pkg/
echo "Renaming internal/ -> pkg/..."
mv "$VENDOR_DIR/internal" "$VENDOR_DIR/pkg"

# Rewrite all import paths: internal/ -> pkg/
echo "Rewriting import paths..."
find "$VENDOR_DIR" -name "*.go" -type f -exec "${SEDI[@]}" \
  "s|$UPSTREAM_MODULE/internal/|$UPSTREAM_MODULE/pkg/|g" {} +

# Rewrite the module name in go.mod so we can use a replace directive
echo "Patching go.mod..."
"${SEDI[@]}" "s|^module $UPSTREAM_MODULE|module goodchanges/tsgo-vendor|" "$VENDOR_DIR/go.mod"

# Also rewrite self-references if any exist beyond internal/
find "$VENDOR_DIR" -name "*.go" -type f -exec "${SEDI[@]}" \
  "s|$UPSTREAM_MODULE|goodchanges/tsgo-vendor|g" {} +

# Fix go.mod replace path references too
"${SEDI[@]}" "s|$UPSTREAM_MODULE|goodchanges/tsgo-vendor|g" "$VENDOR_DIR/go.mod"

echo "Done. Vendored tsgo at $VENDOR_DIR"
echo "Module: goodchanges/tsgo-vendor"
echo "Import packages like: goodchanges/tsgo-vendor/pkg/parser"