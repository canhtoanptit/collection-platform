# Work-package brief template

Copy this file for every WP delegated to an implementation agent. A brief is complete when an agent
with **no other context than this document plus `CLAUDE.md`** can build the thing and prove it works.

Rules for the lead agent writing the brief:

- **Deliverable paths are exhaustive.** Nothing outside them may change, and they must equal the WP's
  entry in `docs/ownership.yaml` (added and committed *before* delegation).
- **Every acceptance criterion is a runnable command** with an expected outcome. Prose is not evidence.
- **Sizes**: `S` ≈ one agent session · `M` ≈ 2–3 · `L` **must be decomposed by the lead into ≤4
  sub-briefs before delegation** — never delegate an L brief as-is.
- Cite design sections (`D§n`, `A§n`) and exact contract file paths instead of restating them; the
  agent reads the source.
- Name the exemplar to clone (`services/case`, `docs/service-playbook.md`, `ui/collector-workbench`)
  and the deltas, rather than describing a service from scratch.
- Mark the WP if `docs/review-policy.md` requires adversarial verification.

---

## WP

`<WP-ID>` — `<short title>`

## Size

`S | M | L` · depends on: `<WP-IDs>` · parallel with: `<WP-IDs>` · review:
`standard | ADVERSARIAL` · model: `<per docs/conventions.md model assignment rule>`

## Context

Why this exists in two or three sentences, then the reading list:

- Design: `D§…`, `A§…`
- Contracts: `contracts/openapi/<x>.v1.yaml`, `contracts/schemas/<y>.v1.json`
- Exemplar / playbook: `<path>`
- Related WPs already merged: `<WP-IDs and what they provide>`

## Consumes

Frozen interfaces this WP depends on and must not change — APIs, event types + topics + keys, platform
packages, DB objects owned elsewhere, config/secrets. Anything not listed here is out of reach.

## Provides

What other WPs may rely on after this one merges — endpoints, event types, exported packages/functions,
tables, make targets, dashboards. This becomes someone else's "Consumes"; be exact about names.

## Deliverable paths

Exhaustive list; matches `docs/ownership.yaml`:

```text
path/one/**
path/two/file.go
scripts/verify/<WP-ID>.sh
```

## Implementation requirements

Numbered, individually testable, no ambiguity. Each states the observable behaviour, not the code.

1. …
2. …
3. …

Include explicitly, where relevant: state machine transition table; idempotency behaviour; error codes
and their A§20 mapping; event payload fields; migration DDL; money units; UTC/`--as-of` handling;
metrics names; failure/timeout behaviour.

## Acceptance criteria

Commands the agent must run, with expected results. Standard block for a service WP:

```bash
make -C services/<name> generate && git diff --exit-code
make -C services/<name> lint test coverage test-integration contract-test image
go run ./tools/layoutcheck services/<name> && helm lint deployment/charts/<name>
make verify WP=<WP-ID>
make ownership-check WP=<WP-ID>
```

Plus WP-specific assertions, including at least one **expected-fail** case (a command that must exit
non-zero, proving the guard works) and named test functions where behaviour is subtle.

## Out of scope

What this WP must NOT do, and which WP does it instead. Prevents scope drift and path collisions.
Call out tempting adjacent work: other services, contract edits, infra applies, dashboards, UI.

## Verification script

`scripts/verify/<WP-ID>.sh` — `set -euo pipefail`, exit 0 = pass, runnable from any cwd, no
interactive input, cleans up what it creates. It must assert the acceptance criteria above (including
the expected-fail cases) and print one line per check. See `scripts/verify/README.md`.
