#!/usr/bin/env bash
#
# check-ownership.sh — assert that every touched file belongs to a work package.
#
# Usage:
#   tools/check-ownership.sh <WP-ID>
#   tools/check-ownership.sh <WP-ID> --files "<newline-separated paths>"
#   tools/check-ownership.sh <WP-ID> --ownership <path/to/ownership.yaml>
#
# Default file list = `git diff --name-only HEAD` + untracked files
# (`git ls-files --others --exclude-standard`), i.e. everything this working tree
# changed relative to HEAD. `--files` bypasses git entirely so the checker is
# unit-testable without git state (see scripts/verify/OPS-1.sh).
#
# Ownership map: docs/ownership.yaml (restricted YAML subset — see its header).
# Parsed with python3 stdlib only; no PyYAML, no third-party deps anywhere.
#
# Exit codes: 0 = all files owned · 1 = ownership violation
#             2 = usage or parse error · 3 = unknown work package
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
OWNERSHIP="$REPO_ROOT/docs/ownership.yaml"

usage() {
	sed -n '3,17p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2
}

WP=""
FILES=""
HAVE_FILES=0

while [ "$#" -gt 0 ]; do
	case "$1" in
	-h | --help)
		usage
		exit 0
		;;
	--files)
		[ "$#" -ge 2 ] || {
			echo "check-ownership: --files needs a value" >&2
			exit 2
		}
		FILES="$2"
		HAVE_FILES=1
		shift 2
		;;
	--ownership)
		[ "$#" -ge 2 ] || {
			echo "check-ownership: --ownership needs a value" >&2
			exit 2
		}
		OWNERSHIP="$2"
		shift 2
		;;
	-*)
		echo "check-ownership: unknown option '$1'" >&2
		usage
		exit 2
		;;
	*)
		[ -z "$WP" ] || {
			echo "check-ownership: unexpected argument '$1' (one WP id only)" >&2
			exit 2
		}
		WP="$1"
		shift
		;;
	esac
done

if [ -z "$WP" ]; then
	echo "check-ownership: missing <WP-ID>" >&2
	usage
	exit 2
fi

if [ ! -f "$OWNERSHIP" ]; then
	echo "check-ownership: ownership map not found: $OWNERSHIP" >&2
	exit 2
fi

if [ "$HAVE_FILES" -eq 0 ]; then
	git -C "$REPO_ROOT" rev-parse --git-dir >/dev/null 2>&1 || {
		echo "check-ownership: $REPO_ROOT is not a git repository; pass --files instead" >&2
		exit 2
	}
	changed="$(git -C "$REPO_ROOT" diff --name-only -z HEAD | tr '\0' '\n')"
	untracked="$(git -C "$REPO_ROOT" ls-files -z --others --exclude-standard | tr '\0' '\n')"
	FILES="$(printf '%s\n%s\n' "$changed" "$untracked")"
fi

CHECK_OWNERSHIP_FILES="$FILES" \
	python3 - "$WP" "$OWNERSHIP" <<'PY'
import os
import re
import sys

wp, ownership_path = sys.argv[1], sys.argv[2]

# ------------------------------------------------------------------ parsing --
# Restricted YAML subset: comments, `KEY:` at column 0, `  - "glob"` items.
KEY_RE = re.compile(r'^([A-Za-z_][A-Za-z0-9_.\-]*):[ \t]*(#.*)?$')
ITEM_RE = re.compile(r'^[ ]+-[ \t]+(.*)$')


def die(msg, code=2):
    sys.stderr.write("check-ownership: %s\n" % msg)
    raise SystemExit(code)


def parse_item(raw, lineno):
    raw = raw.rstrip()
    if not raw or raw[0] not in "\"'":
        die('%s:%d: list item must be a quoted glob, e.g. - "docs/foo/**" (got: %s)'
            % (ownership_path, lineno, raw or "<empty>"))
    quote = raw[0]
    end = raw.find(quote, 1)
    if end == -1:
        die("%s:%d: unterminated %s quote" % (ownership_path, lineno, quote))
    value = raw[1:end]
    trailer = raw[end + 1:].strip()
    if trailer and not trailer.startswith("#"):
        die("%s:%d: unexpected text after the quoted glob: %s"
            % (ownership_path, lineno, trailer))
    if not value:
        die("%s:%d: empty glob" % (ownership_path, lineno))
    return value


def load(path):
    owners = {}
    order = []
    current = None
    with open(path, "r", encoding="utf-8") as fh:
        for lineno, line in enumerate(fh, start=1):
            line = line.rstrip("\n")
            if "\t" in line:
                die("%s:%d: tabs are not allowed" % (path, lineno))
            stripped = line.strip()
            if not stripped or stripped.startswith("#"):
                continue
            m = KEY_RE.match(line)
            if m:
                current = m.group(1)
                if current in owners:
                    die("%s:%d: duplicate key %s" % (path, lineno, current))
                owners[current] = []
                order.append(current)
                continue
            m = ITEM_RE.match(line)
            if m:
                if current is None:
                    die("%s:%d: list item before any WP key" % (path, lineno))
                owners[current].append(parse_item(m.group(1), lineno))
                continue
            die('%s:%d: unsupported syntax (only "# comment", "WP-ID:" and '
                '\'  - "glob"\' lines are allowed): %s' % (path, lineno, line))
    return owners, order


# ------------------------------------------------------------------ globbing --
def glob_to_regex(glob):
    out = [r"\A"]
    i, n = 0, len(glob)
    while i < n:
        c = glob[i]
        if c == "*":
            if glob[i:i + 3] == "**/":
                out.append(r"(?:[^/]+/)*")
                i += 3
                continue
            if glob[i:i + 2] == "**":
                out.append(r".*")
                i += 2
                continue
            out.append(r"[^/]*")
            i += 1
            continue
        if c == "?":
            out.append(r"[^/]")
            i += 1
            continue
        if c == "[":
            j = glob.find("]", i + 1)
            if j != -1:
                cls = glob[i + 1:j]
                if cls.startswith("!"):
                    cls = "^" + cls[1:]
                out.append("[" + cls + "]")
                i = j + 1
                continue
        out.append(re.escape(c))
        i += 1
    out.append(r"\Z")
    return re.compile("".join(out))


def normalize(path):
    path = path.strip().replace(os.sep, "/")
    while path.startswith("./"):
        path = path[2:]
    return path.strip("/")


owners, order = load(ownership_path)
public = [k for k in order if not k.startswith("_")]

if wp.startswith("_") or wp not in owners:
    sys.stderr.write(
        'check-ownership: unknown work package "%s".\n'
        "%s declares: %s\n\n"
        "Add an entry BEFORE starting work (lead agent, committed separately):\n\n"
        "  %s:\n"
        '    - "path/you/own/**"\n'
        '    - "scripts/verify/%s.sh"\n\n'
        "The globs must equal the brief's \"Deliverable paths\" section exactly.\n"
        % (wp, ownership_path, ", ".join(public) or "<nothing>", wp, wp))
    raise SystemExit(3)

patterns = [(g, glob_to_regex(g)) for g in owners[wp]]
others = [(other, [(g, glob_to_regex(g)) for g in globs])
          for other, globs in owners.items() if other != wp and not other.startswith("_")]

seen = set()
files = []
for raw in (os.environ.get("CHECK_OWNERSHIP_FILES") or "").splitlines():
    f = normalize(raw)
    if not f or f in seen:
        continue
    seen.add(f)
    files.append(f)

if not files:
    print("check-ownership: %s — no changed files to check: OK" % wp)
    raise SystemExit(0)

violations = []
for f in files:
    if any(rx.match(f) for _, rx in patterns):
        continue
    hint = sorted(other for other, globs in others if any(rx.match(f) for _, rx in globs))
    violations.append((f, hint))

if not violations:
    print("check-ownership: %s — %d file(s) matched %d owned glob(s): OK"
          % (wp, len(files), len(patterns)))
    raise SystemExit(0)

sys.stderr.write("check-ownership: %s — %d of %d file(s) outside this WP's ownership:\n"
                 % (wp, len(violations), len(files)))
for f, hint in violations:
    who = ("owned by: " + ", ".join(hint)) if hint else "owned by: nobody"
    sys.stderr.write("  %-60s (%s)\n" % (f, who))
sys.stderr.write(
    "\nAllowed globs for %s (%s):\n" % (wp, ownership_path))
for g in owners[wp]:
    sys.stderr.write("  %s\n" % g)
sys.stderr.write(
    "\nFix one of these ways:\n"
    "  * revert the out-of-scope edits (usual case — another WP owns them);\n"
    "  * hand the change to the owning WP;\n"
    "  * ask the lead agent to widen this WP's brief AND its ownership entry.\n")
raise SystemExit(1)
PY
