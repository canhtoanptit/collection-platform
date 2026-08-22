package check

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// idempotencyKeyRef is the one legitimate way to require the header: the shared
// parameter in common.v1.yaml (A§21, contracts/README.md §8). Requiring the *ref*
// rather than just a header named `Idempotency-Key` is deliberate — a hand-rolled
// copy would drift from the shared description and validation.
const idempotencyKeyRef = "common.v1.yaml#/components/parameters/IdempotencyKey"

// idempotencyKeyHeader is the header name, accepted only to produce a better
// message when someone declares the parameter inline instead of referencing it.
const idempotencyKeyHeader = "Idempotency-Key"

// idempotencyExemption is a POST that must NOT carry `Idempotency-Key`, with the
// reason it is exempt. The list is closed and self-policing: a stale entry (the
// operation gained the header, was renamed, or disappeared) fails the check, so
// this cannot rot into a blanket opt-out.
type idempotencyExemption struct {
	spec        string // file name under contracts/openapi/
	operationID string
	reason      string
}

var idempotencyExemptions = []idempotencyExemption{
	{
		spec:        "model.v1.yaml",
		operationID: "scoreModelVersion",
		reason: "non-mutating POST: scoring computes a value and stores nothing, so there is no " +
			"side effect to replay (contracts/README.md §8 — non-mutating POSTs must not reference the parameter)",
	},
	{
		spec:        "treatment.v1.yaml",
		operationID: "recordProviderDeliveryStatus",
		reason: "inbound provider webhook (security: []): external providers do not send " +
			"Idempotency-Key, so dedupe is by the natural key (provider, providerRef) instead",
	},
}

// checkPostIdempotency asserts A§21 across every spec: every POST command
// references the shared `Idempotency-Key` parameter, no other method does, and
// every exemption is still true.
//
// This lives here rather than in contracts/vacuum-ruleset.yaml on purpose. The
// positive rule is expressible in vacuum (a `schema` function over
// `$.paths.*[?(@property == 'post')].parameters`), but the two legitimate
// exemptions are not: vacuum can only silence them per-path in an ignore file,
// which would never notice an exemption that stopped being true. The gate is only
// worth having if its escape hatches are themselves checked.
func checkPostIdempotency(r *repo) result {
	var res result

	specs, err := r.walkFiles(contractsDir+"/openapi", ".yaml")
	if err != nil {
		res.problemf("%v", err)
		return res
	}
	if len(specs) == 0 {
		res.problemf("no OpenAPI specs found under %s/openapi", contractsDir)
		return res
	}

	exempt := map[string]idempotencyExemption{}
	for _, e := range idempotencyExemptions {
		exempt[e.spec+":"+e.operationID] = e
	}
	claimed := map[string]bool{}

	posts, withKey, others := 0, 0, 0
	for _, spec := range specs {
		specName := path.Base(spec)
		b, err := os.ReadFile(r.path(spec))
		if err != nil {
			res.problemf("reading %s: %v", spec, err)
			continue
		}
		var doc map[string]any
		if err := yaml.Unmarshal(b, &doc); err != nil {
			res.problemf("%s is not valid YAML: %v", spec, err)
			continue
		}

		paths, _ := doc["paths"].(map[string]any)
		for _, url := range sortedKeys(paths) {
			item, ok := paths[url].(map[string]any)
			if !ok {
				continue
			}
			// Parameters declared on the path item apply to every operation in it.
			pathLevel := hasIdempotencyKey(item["parameters"])

			for _, method := range sortedKeys(item) {
				op, ok := item[method].(map[string]any)
				if !ok || !isHTTPMethod(method) {
					continue
				}
				opID, _ := op["operationId"].(string)
				if opID == "" {
					opID = strings.ToUpper(method) + " " + url
				}
				present := pathLevel || hasIdempotencyKey(op["parameters"])

				if method != "post" {
					others++
					if present {
						res.problemf("%s %s (%s %s): references %s — PUT/PATCH/DELETE are idempotent by construction and must not use the header (contracts/README.md §8)",
							specName, opID, strings.ToUpper(method), url, idempotencyKeyHeader)
					}
					continue
				}

				posts++
				key := specName + ":" + opID
				ex, isExempt := exempt[key]
				switch {
				case isExempt:
					claimed[key] = true
					if present {
						res.problemf("%s %s: is exempt (%s) but now references %s — remove the exemption from tools/contractcheck/internal/check/openapi.go",
							specName, opID, ex.reason, idempotencyKeyHeader)
					}
				case !present:
					res.problemf("%s %s (POST %s): does not reference the %s parameter — add `- $ref: './%s'` (A§21)",
						specName, opID, url, idempotencyKeyHeader, idempotencyKeyRef)
				default:
					withKey++
				}
			}
		}
	}

	for _, e := range idempotencyExemptions {
		key := e.spec + ":" + e.operationID
		if !claimed[key] {
			res.problemf("stale exemption: %s has no POST operation %q — delete the entry in tools/contractcheck/internal/check/openapi.go",
				e.spec, e.operationID)
			continue
		}
		res.notef("exempt: %s %s — %s", e.spec, e.operationID, e.reason)
	}

	res.notef("%d specs: %d POST operations (%d carry %s, %d exempt), %d other operations checked for stray use",
		len(specs), posts, withKey, idempotencyKeyHeader, len(idempotencyExemptions), others)
	return res
}

// hasIdempotencyKey reports whether an OpenAPI `parameters` list references the
// shared Idempotency-Key parameter (or declares a header with that name).
func hasIdempotencyKey(node any) bool {
	list, ok := node.([]any)
	if !ok {
		return false
	}
	for _, entry := range list {
		p, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if ref, ok := p["$ref"].(string); ok && strings.HasSuffix(ref, idempotencyKeyRef) {
			return true
		}
		if name, ok := p["name"].(string); ok && name == idempotencyKeyHeader {
			return true
		}
	}
	return false
}

func isHTTPMethod(k string) bool {
	switch k {
	case "get", "put", "post", "delete", "options", "head", "patch", "trace":
		return true
	default:
		return false
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, fmt.Sprint(k))
	}
	sort.Strings(out)
	return out
}
