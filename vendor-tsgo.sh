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

# Clean previous vendor
rm -rf "$VENDOR_DIR"
mkdir -p "_vendor"

# Clone the pinned commit (shallow)
echo "Cloning microsoft/typescript-go at $TSGO_REF..."
git init "$VENDOR_DIR"
git -C "$VENDOR_DIR" remote add origin https://github.com/microsoft/typescript-go.git
git -C "$VENDOR_DIR" fetch --depth 1 origin "$TSGO_REF"
git -C "$VENDOR_DIR" checkout FETCH_HEAD

# Remove git history and stuff we don't need
rm -rf "$VENDOR_DIR/.git" "$VENDOR_DIR/_tools" "$VENDOR_DIR/cmd" "$VENDOR_DIR/_submodules" "$VENDOR_DIR/testdata"

# Rename internal/ to pkg/
echo "Renaming internal/ -> pkg/..."
mv "$VENDOR_DIR/internal" "$VENDOR_DIR/pkg"

# Rewrite all import paths: internal/ -> pkg/
echo "Rewriting import paths..."
find "$VENDOR_DIR" -name "*.go" -type f -exec "${SEDI[@]}" \
  's|github.com/microsoft/typescript-go/internal/|github.com/microsoft/typescript-go/pkg/|g' {} +

# Rewrite the module name in go.mod so we can use a replace directive
echo "Patching go.mod..."
"${SEDI[@]}" 's|^module github.com/microsoft/typescript-go|module goodchanges/tsgo-vendor|' "$VENDOR_DIR/go.mod"

# Also rewrite self-references if any exist beyond internal/
find "$VENDOR_DIR" -name "*.go" -type f -exec "${SEDI[@]}" \
  's|github.com/microsoft/typescript-go|goodchanges/tsgo-vendor|g' {} +

# Fix go.mod replace path references too
"${SEDI[@]}" 's|github.com/microsoft/typescript-go|goodchanges/tsgo-vendor|g' "$VENDOR_DIR/go.mod"

echo "Done. Vendored tsgo at $VENDOR_DIR"
echo "Module: goodchanges/tsgo-vendor"
echo "Import packages like: goodchanges/tsgo-vendor/pkg/parser"