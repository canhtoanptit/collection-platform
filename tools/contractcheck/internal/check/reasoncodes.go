package check

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// reasonCodeRegistry is the closed vocabulary every reason code must come from
// (contracts/registries/README.md).
const reasonCodeRegistry = contractsDir + "/registries/reason-codes.v1.json"

// reasonCodeKeys are the field names that carry reason codes anywhere in the
// contracts: a single value, a list of values, or — inside a schema — a subschema
// whose `examples`/`example`/`enum`/`const`/`default` hold sample values.
var reasonCodeKeys = map[string]bool{
	"reasonCode":         true,
	"reasonCodes":        true,
	"suppressionReasons": true,
}

// reasonCodePattern is the registry's own code shape
// (contracts/registries/README.md). Anything under a reason-code key that does not
// look like a code (prose, a ULID, a path) is ignored rather than reported, so the
// check stays about the vocabulary and not about spelling.
var reasonCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,63}$`)

// No exemptions are configured, and none are needed: the band-style model codes
// (`PAYMENT_PROPENSITY_BAND`, `PAYMENT_PROPENSITY_HIGH`, `PAYMENT_PROPENSITY_LOW`)
// are real registry entries, and the one documented exception in
// contracts/registries/README.md — population-line's
// `legacyDecision.reasonCodes`, which may carry unmapped legacy codes verbatim —
// currently uses registry codes only. Keeping the check strict over our own
// artefacts is deliberate (it matches scripts/verify/DEC-1A.sh): a typo in an
// example must be a red build, not a lie in the documentation. If DEC ever ships a
// genuinely unmapped legacy code, add the exemption here with the reason and cite
// the registry README.

// checkReasonCodes proves the reason-code vocabulary is closed: every code used
// in a schema example or a golden example exists in the registry. An unknown code
// has no description to render, so `GET /v1/decisions/{id}/explanation` would
// return an unexplainable decision (A§58).
func checkReasonCodes(r *repo) result {
	var res result

	doc, err := r.readJSON(reasonCodeRegistry)
	if err != nil {
		res.problemf("reading the reason-code registry: %v", err)
		return res
	}
	known, err := registryCodes(doc)
	if err != nil {
		res.problemf("%s: %v", reasonCodeRegistry, err)
		return res
	}
	if len(known) == 0 {
		res.problemf("%s declares no codes — the registry is empty or its shape changed", reasonCodeRegistry)
		return res
	}

	var files []string
	for _, root := range []string{contractsDir + "/examples", contractsDir + "/schemas"} {
		found, err := r.walkFiles(root, ".json")
		if err != nil {
			res.problemf("%v", err)
			return res
		}
		files = append(files, found...)
	}

	// code -> the files that use it, so a failure says where to go and fix it.
	used := map[string][]string{}
	for _, f := range files {
		doc, err := r.readJSON(f)
		if err != nil {
			res.problemf("%v", err)
			continue
		}
		codes := map[string]bool{}
		walkReasonCodes(doc, codes)
		for c := range codes {
			used[c] = append(used[c], f)
		}
	}

	if len(used) == 0 {
		res.problemf("no reason codes found under %s/{examples,schemas} at all — the collector is broken, not the artefacts", contractsDir)
		return res
	}

	unknown := make([]string, 0)
	for code, where := range used {
		if !known[code] {
			sort.Strings(where)
			unknown = append(unknown, fmt.Sprintf("unknown reason code %s used in %s — add it to %s or fix the typo",
				code, strings.Join(where, ", "), reasonCodeRegistry))
		}
	}
	for _, u := range unknown {
		res.problemf("%s", u)
	}

	res.notef("%d distinct codes used across %d JSON artefacts, checked against %d registry codes",
		len(used), len(files), len(known))
	return res
}

// registryCodes reads the `codes[].code` list out of the registry document.
func registryCodes(doc any) (map[string]bool, error) {
	obj, ok := doc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("registry must be a JSON object, got %T", doc)
	}
	list, ok := obj["codes"].([]any)
	if !ok {
		return nil, fmt.Errorf("registry has no `codes` array")
	}
	out := make(map[string]bool, len(list))
	for i, entry := range list {
		e, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("codes[%d] is not an object", i)
		}
		code, ok := e["code"].(string)
		if !ok {
			return nil, fmt.Errorf("codes[%d] has no string `code`", i)
		}
		out[code] = true
	}
	return out, nil
}

// walkReasonCodes finds every reason-code-shaped string reachable from a
// reason-code key.
func walkReasonCodes(node any, sink map[string]bool) {
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			if reasonCodeKeys[k] {
				harvestReasonCodes(v, sink)
			}
			walkReasonCodes(v, sink)
		}
	case []any:
		for _, v := range n {
			walkReasonCodes(v, sink)
		}
	}
}

// harvestReasonCodes extracts code values from whatever a reason-code key holds:
// a value, a list of values, or a subschema carrying sample/enumerated values.
func harvestReasonCodes(node any, sink map[string]bool) {
	switch n := node.(type) {
	case string:
		if reasonCodePattern.MatchString(n) {
			sink[n] = true
		}
	case []any:
		for _, v := range n {
			harvestReasonCodes(v, sink)
		}
	case map[string]any:
		for _, key := range []string{"examples", "example", "enum", "const", "default"} {
			if v, ok := n[key]; ok {
				harvestReasonCodes(v, sink)
			}
		}
		// A subschema may wrap the values one level deeper (`items.enum`).
		if items, ok := n["items"]; ok {
			harvestReasonCodes(items, sink)
		}
	}
}
