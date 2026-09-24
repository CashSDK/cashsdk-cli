#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

NOTICE=THIRD_PARTY_NOTICES.md
missing=0

while IFS= read -r module; do
  [[ -z "$module" || "$module" == "github.com/cashsdk/cashsdk-cli" ]] && continue
  if ! grep -Fq -- "\`$module\`" "$NOTICE"; then
    echo "third-party notice missing module: $module" >&2
    missing=1
  fi
done < <(go list -deps -f '{{with .Module}}{{.Path}}{{end}}' ./... | sed '/^$/d' | sort -u)

exit "$missing"
