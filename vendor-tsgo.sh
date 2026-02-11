#!/bin/bash
set -e

# Portable in-place sed (macOS needs -i '', Linux needs -i)
if [[ "$(uname)" == "Darwin" ]]; then
  SEDI=(sed -i '')
else
  SEDI=(sed -i)
fi

VENDOR_DIR="_vendor/typescript-go"

# Clean previous vendor
rm -rf "$VENDOR_DIR"
mkdir -p "_vendor"

# Clone just the source (shallow, no submodules)
echo "Cloning microsoft/typescript-go..."
git clone --depth 1 --single-branch https://github.com/microsoft/typescript-go.git "$VENDOR_DIR"

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