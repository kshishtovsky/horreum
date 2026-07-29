#!/usr/bin/env bash
# Generates a professional release body markdown file.
# Usage: release-body.sh <version> <previous-tag> <dist-dir>
set -euo pipefail

VERSION="$1"
TAG_NUM="${VERSION#v}"
PREVIOUS_TAG="$2"
DIST_DIR="$3"

# ── Extract CHANGELOG section ──────────────────────────────────────
CHANGELOG_SECTION=$(
  awk -v ver="$TAG_NUM" '
    BEGIN { in_block = 0 }
    # Escape dots in version for regex match
    { line = $0 }
    /^## \[/ {
      if (in_block) { print ""; exit }
      split($0, a, "[][]")
      if (length(a) >= 2 && a[2] == ver) { in_block = 1; print; next }
    }
    in_block { print }
  ' CHANGELOG.md
)

# ── Contributors list ──────────────────────────────────────────────
CONTRIBUTORS=$(
  if [ -n "$PREVIOUS_TAG" ] && git rev-parse --verify "$PREVIOUS_TAG" &>/dev/null 2>&1; then
    git shortlog -sn "$PREVIOUS_TAG..HEAD" 2>/dev/null
  else
    git shortlog -sn HEAD 2>/dev/null
  fi | grep -viE "bot|dependabot|github-actions" | head -20 \
    | awk '{ $1=""; sub(/^ /, ""); print "- @" $0 }' || echo "- No contributors listed."
)

# ── Checksums ──────────────────────────────────────────────────────
CHECKSUMS=""
if [ -d "$DIST_DIR" ] && [ "$(ls -A "$DIST_DIR"/*.tar.gz 2>/dev/null)" ]; then
  CHECKSUMS=$(cd "$DIST_DIR" && sha256sum ./*.tar.gz)
fi

# ── Build the markdown body ────────────────────────────────────────
{
  echo "# 🚀 Horreum ${VERSION}"
  echo
  echo "> **Arena allocator + mmap storage engine for Go.**"
  echo "> [GitHub](https://github.com/horreum/horreum) ·"
  echo "> [Benchmarks](https://github.com/horreum/horreum#benchmarking)"
  echo
  echo "---"
  echo

  # ── CHANGELOG ──
  if [ -n "$CHANGELOG_SECTION" ]; then
    echo "$CHANGELOG_SECTION"
  else
    echo "## Changes"
    echo
    echo "No changelog entry found for ${VERSION}."
  fi

  # ── Download table ──
  echo
  echo "## Downloads"
  echo
  if [ -d "$DIST_DIR" ]; then
    echo "| OS | Architecture | Download |"
    echo "|:---|:-------------|:---------|"
    BASE_URL="https://github.com/horreum/horreum/releases/download/${VERSION}"

    for FILE in "$DIST_DIR"/*.tar.gz; do
      [ -f "$FILE" ] || continue
      FILENAME=$(basename "$FILE")
      ARCH_PART=$(echo "$FILENAME" | sed "s/horreum-${VERSION}-//;s/\.tar\.gz//")

      OS=$(echo "$ARCH_PART" | cut -d'-' -f1)
      ARCH=$(echo "$ARCH_PART" | cut -d'-' -f2)
      [ "$OS" = "darwin" ] && OS="macOS"
      OS=$(echo "$OS" | awk '{print toupper(substr($0,1,1)) tolower(substr($0,2))}')
      ARCH=$(echo "$ARCH" | awk '{print toupper(substr($0,1,1)) tolower(substr($0,2))}')

      echo "| ${OS} | ${ARCH} | [Download](${BASE_URL}/${FILENAME}) |"
    done
  else
    echo "No pre-built binaries available for this release."
  fi

  # ── Installation ──
  echo
  echo "## Installation"
  echo
  echo '```bash'
  echo "# Linux/macOS (amd64/arm64)"
  echo "curl -LO https://github.com/horreum/horreum/releases/download/${VERSION}/horreum-${VERSION}-linux-amd64.tar.gz"
  echo "tar xzf horreum-${VERSION}-linux-amd64.tar.gz"
  echo "sudo mv horreum /usr/local/bin/horreum"
  echo '```'

  # ── Contributors ──
  echo
  echo "## Contributors"
  echo
  echo "$CONTRIBUTORS"
  echo

  # ── Checksums ──
  if [ -n "$CHECKSUMS" ]; then
    echo "## Checksums"
    echo
    echo "| File | SHA-256 |"
    echo "|:-----|:--------|"
    while IFS= read -r line; do
      HASH=$(echo "$line" | awk '{print $1}')
      FILE=$(echo "$line" | awk '{print $2}')
      echo "| ${FILE} | \`${HASH}\` |"
    done <<< "$CHECKSUMS"
  fi
} > release-body.md

echo "✅ Release body generated: release-body.md"
echo "Total characters: $(wc -c < release-body.md)"
