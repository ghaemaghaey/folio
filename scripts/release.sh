#!/usr/bin/env bash
# Create a GitHub release with gh CLI after tagging.
# Usage: ./scripts/release.sh v0.6.0
set -euo pipefail

VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
  echo "Usage: $0 vX.Y.Z"
  exit 1
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "GitHub CLI (gh) is required: https://cli.github.com/"
  exit 1
fi

if ! gh auth status >/dev/null 2>&1; then
  echo "Run: gh auth login"
  exit 1
fi

git rev-parse --is-inside-work-tree >/dev/null

# Prefer CI-built assets; this script only tags + opens the release if needed.
if git rev-parse "$VERSION" >/dev/null 2>&1; then
  echo "Tag $VERSION already exists."
else
  git tag -a "$VERSION" -m "Folio $VERSION"
  git push origin "$VERSION"
  echo "Pushed tag $VERSION — GitHub Actions will build and attach binaries."
fi

echo "Watch: gh run list --workflow=release.yml"
echo "Release: gh release view $VERSION"
