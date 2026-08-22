#!/usr/bin/env bash
# Minimal contract validation (FND-0). CON-7 extends this with:
# tools/contractcheck (schema compile + example validation + catalogue
# coverage + reason-code cross-reference), vacuum lint, oasdiff breaking-change
# gate, and the released-file immutability check.
set -euo pipefail
cd "$(dirname "$0")/../.."

fail=0

# 1. Every JSON contract artefact must be syntactically valid JSON.
while IFS= read -r -d '' f; do
  if ! python3 -m json.tool "$f" >/dev/null 2>&1; then
    echo "INVALID JSON: $f"
    fail=1
  fi
done < <(find contracts -name '*.json' -print0)

# 2. The contracts module must build (proves the embed FS is intact).
go -C contracts build ./...

# 3. vacuum lint every OpenAPI spec (skips cleanly while none exist yet).
specs=$(find contracts/openapi -name '*.yaml' -not -name 'common*' 2>/dev/null || true)
if [ -n "$specs" ]; then
  for s in $specs; do
    echo "--- vacuum lint $s"
    GOWORK=off go -C tools tool vacuum lint -d -e "../$s" || fail=1
  done
fi

if [ "$fail" -ne 0 ]; then
  echo "contracts-check: FAILED"
  exit 1
fi
echo "contracts-check: OK"
