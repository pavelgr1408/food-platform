#!/usr/bin/env bash
# Проверка шаблона: копия репозитория во временной папке → new-service → build/vet/test + schema values.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
(cd "$ROOT" && git ls-files -z --cached --others --exclude-standard | xargs -0 tar cf -) | tar xf - -C "$TMP"
cd "$TMP"
tools/new-service/new-service.sh tplcheck 8199 >/dev/null
gofmt -l services/tplcheck | (! grep .) || { echo "template output is not gofmt-clean"; exit 1; }
go vet ./services/tplcheck/...
go test ./services/tplcheck/...
"${PYTHON:-python3}" tools/validate-values/validate_values.py >/dev/null
echo "template OK"
