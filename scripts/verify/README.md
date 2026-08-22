# `scripts/verify/` — per-WP verification scripts

Every work package ships exactly one script here: `scripts/verify/<WP-ID>.sh`. It is the mechanical
answer to "is this WP done?" and it is run by the implementer, the reviewer, and the phase-gate
verifier — so it must be honest and self-contained.

```bash
make verify WP=OPS-1        # runs scripts/verify/OPS-1.sh
bash scripts/verify/OPS-1.sh
```

## Requirements

1. `#!/usr/bin/env bash` and `set -euo pipefail`. Nothing else is accepted.
2. Exit `0` = pass, non-zero = fail. No "warnings" that pass.
3. Runnable from any working directory — resolve the repo root from `BASH_SOURCE`, never assume cwd.
4. Non-interactive: no prompts, no `read`, no credentials typed in. Configuration comes from env vars
   with documented names and sane local defaults.
5. Prints one line per check (`ok: …` / `FAIL: …`) and a final summary with counts. A reviewer must be
   able to see *which* assertion failed without reading the script.
6. Asserts observable outcomes: exit codes, HTTP status and body fields, DB rows, Kafka messages, S3
   objects, metric values, file contents. Never assert on log text.
7. Includes at least one **expected-fail** assertion — a command that must exit non-zero — proving a
   guard rejects bad input rather than merely accepting good input.
8. Cleans up everything it creates (`mktemp -d` + `trap ... EXIT`). It must be re-runnable back to back
   with the same result.
9. States its environment need at the top: nothing / local compose stack (`make compose-up`) / dev EKS.
   If the environment is absent, exit non-zero with a clear message — never skip silently.
10. No new dependencies. Bash, coreutils, `git`, `python3` (stdlib only), and tools the repo already
    pins via `go -C tools tool <name>`. `jq`, `psql`, `kubectl`, `aws` may be used where the WP's
    environment already requires them — check for them and fail with a clear message if missing.

## Skeleton

```bash
#!/usr/bin/env bash
# scripts/verify/<WP-ID>.sh — <what this proves>. Environment: <none|compose|dev EKS>.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0
fail=0
ok()   { printf 'ok:   %s\n' "$1"; pass=$((pass + 1)); }
bad()  { printf 'FAIL: %s\n' "$1" >&2; fail=$((fail + 1)); }

check()      { if eval "$2" >/dev/null 2>&1; then ok "$1"; else bad "$1"; fi; }   # must succeed
check_fails() { if eval "$2" >/dev/null 2>&1; then bad "$1 (command unexpectedly succeeded)"; else ok "$1"; fi; }

check       "domain rejects a closed case reopen"  "..."
check_fails "audit table rejects UPDATE"           "psql -c 'update decision_audit ...'"

printf '\n<WP-ID>: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
```

`scripts/verify/OPS-1.sh` is the worked example — it verifies the delegation pack itself, including
expected-fail assertions against `tools/check-ownership.sh` and `tools/lint-runbook.sh`.

## Naming and ownership

The script is listed in the WP's deliverable paths and in `docs/ownership.yaml` under that WP, so no two
agents ever edit the same verify script. When a later WP changes behaviour that an earlier script
asserts, the later WP's brief must say so explicitly and its ownership entry must include the earlier
script — otherwise leave it alone.
