package check

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// asyncAPIIndex is the normative topic -> key -> schema map (CON-2, A§25/A§26).
const asyncAPIIndex = contractsDir + "/asyncapi/collections.v1.yaml"

// refSite is one `$ref` occurrence: the YAML location it was found at (for a
// diagnosable failure) and the raw reference string.
type refSite struct {
	where string
	ref   string
}

// asyncAPIDoc is the parsed topic index plus every reference found in it.
type asyncAPIDoc struct {
	doc any
	// refs is every $ref site in document order.
	refs []refSite
	// fileRefs maps a repo-relative slash path to the ref sites pointing at it.
	// It is what proves a schema is actually wired to a topic.
	fileRefs map[string][]refSite
}

func loadAsyncAPI(r *repo) (*asyncAPIDoc, error) {
	b, err := os.ReadFile(r.path(asyncAPIIndex))
	if err != nil {
		return nil, fmt.Errorf("reading the AsyncAPI index: %w", err)
	}
	var doc any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s is not valid YAML: %w", asyncAPIIndex, err)
	}

	out := &asyncAPIDoc{doc: doc, fileRefs: map[string][]refSite{}}
	collectRefs(doc, "$", &out.refs)
	base := path.Dir(asyncAPIIndex)
	for _, site := range out.refs {
		if strings.HasPrefix(site.ref, "#") {
			continue
		}
		target, _, _ := strings.Cut(site.ref, "#")
		if target == "" {
			continue
		}
		rel := path.Clean(path.Join(base, target))
		out.fileRefs[rel] = append(out.fileRefs[rel], site)
	}
	return out, nil
}

// checkAsyncAPIRefs resolves every reference in the topic index: internal ones
// (`#/components/messages/X`) against the document itself, external ones
// (`../schemas/...`) against the file system. A dangling payload schema ref means
// a topic whose contract does not exist — the index would document a lie, and
// `platform/events` would fail at runtime rather than in CI.
func checkAsyncAPIRefs(r *repo) result {
	var res result

	idx, err := loadAsyncAPI(r)
	if err != nil {
		res.problemf("%v", err)
		return res
	}

	internal, external := 0, 0
	for _, site := range idx.refs {
		switch {
		case strings.HasPrefix(site.ref, "#"):
			internal++
			if _, err := resolvePointer(idx.doc, strings.TrimPrefix(site.ref, "#")); err != nil {
				res.problemf("%s: internal $ref %q does not resolve: %v", site.where, site.ref, err)
			}
		case strings.Contains(site.ref, "://"):
			res.problemf("%s: $ref %q is a network reference; contracts are self-contained", site.where, site.ref)
		default:
			external++
			target, pointer, hasPointer := strings.Cut(site.ref, "#")
			rel := path.Clean(path.Join(path.Dir(asyncAPIIndex), target))
			if !strings.HasPrefix(rel, contractsDir+"/") {
				res.problemf("%s: $ref %q escapes %s/", site.where, site.ref, contractsDir)
				continue
			}
			if !r.exists(rel) {
				res.problemf("%s: $ref %q does not resolve — no file at %s", site.where, site.ref, rel)
				continue
			}
			if !hasPointer || pointer == "" {
				continue
			}
			doc, err := r.readJSON(rel)
			if err != nil {
				res.problemf("%s: $ref %q target unreadable: %v", site.where, site.ref, err)
				continue
			}
			if _, err := resolvePointer(doc, pointer); err != nil {
				res.problemf("%s: $ref %q pointer does not resolve in %s: %v", site.where, site.ref, rel, err)
			}
		}
	}

	res.notef("%s: %d $refs (%d internal, %d file), %d distinct files referenced",
		asyncAPIIndex, len(idx.refs), internal, external, len(idx.fileRefs))
	if external == 0 {
		res.problemf("%s references no payload schema files at all — the index is empty or the parser is broken", asyncAPIIndex)
	}
	return res
}

// collectRefs walks a decoded YAML/JSON tree and records every `$ref` string with
// its location, so a failure names the exact site rather than "somewhere".
func collectRefs(node any, where string, out *[]refSite) {
	switch n := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(n))
		for k := range n {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k == "$ref" {
				if s, ok := n[k].(string); ok {
					*out = append(*out, refSite{where: where, ref: s})
					continue
				}
			}
			collectRefs(n[k], where+"."+k, out)
		}
	case map[any]any:
		converted := make(map[string]any, len(n))
		for k, v := range n {
			converted[fmt.Sprint(k)] = v
		}
		collectRefs(converted, where, out)
	case []any:
		for i, v := range n {
			collectRefs(v, where+"["+strconv.Itoa(i)+"]", out)
		}
	}
}

// resolvePointer walks an RFC 6901 JSON pointer through a decoded document.
func resolvePointer(doc any, pointer string) (any, error) {
	if pointer == "" || pointer == "/" {
		return doc, nil
	}
	cur := doc
	for _, raw := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[token]
			if !ok {
				return nil, fmt.Errorf("no member %q", token)
			}
			cur = next
		case map[any]any:
			next, ok := node[token]
			if !ok {
				return nil, fmt.Errorf("no member %q", token)
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(token)
			if err != nil || i < 0 || i >= len(node) {
				return nil, fmt.Errorf("bad array index %q", token)
			}
			cur = node[i]
		default:
			return nil, fmt.Errorf("cannot descend into %T at %q", cur, token)
		}
	}
	return cur, nil
}
