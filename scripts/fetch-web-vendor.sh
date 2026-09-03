#!/usr/bin/env bash
# Refresh vendored browser assets used by the embedded web UI.
# Usage: scripts/fetch-web-vendor.sh [mermaid-version]
set -euo pipefail

version="${1:-11}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dest="$root/web/assets/vendor/mermaid.min.js"

echo "Fetching mermaid@$version -> $dest"
curl -fsSL "https://cdn.jsdelivr.net/npm/mermaid@${version}/dist/mermaid.min.js" -o "$dest"
echo "Done. Review the diff and update web/assets/vendor/README.md if the version changed."
