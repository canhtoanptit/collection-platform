package check

import (
	"strings"
)

// checkSchemaIDs asserts the naming rule every cross-file `$ref` depends on
// (contracts/README.md §2): a schema's `$id` is
// `https://contracts.collections.internal/<path within the contracts module>`.
// The embedded loader resolves refs through that URL, so a wrong `$id` silently
// breaks every consumer that follows a ref — including `platform/events` at
// runtime. Checked here from disk (rather than only through the module's embed.FS
// in contracts/validate_test.go) so the same rule also covers JSON outside
// `schemas/` that copy-pastes an `$id`.
func checkSchemaIDs(r *repo) result {
	var res result

	files, err := r.walkFiles(contractsDir, ".json")
	if err != nil {
		res.problemf("%v", err)
		return res
	}

	schemas, withID := 0, 0
	for _, f := range files {
		doc, err := r.readJSON(f)
		if err != nil {
			res.problemf("%v", err)
			continue
		}
		obj, ok := doc.(map[string]any)
		underSchemas := strings.HasPrefix(f, contractsDir+"/schemas/")
		if underSchemas {
			schemas++
		}
		if !ok {
			if underSchemas {
				res.problemf("%s: a schema must be a JSON object", f)
			}
			continue
		}

		raw, present := obj["$id"]
		if !present {
			if underSchemas {
				res.problemf("%s: no $id — every schema declares $id %s%s",
					f, schemaBaseURL, strings.TrimPrefix(f, contractsDir+"/"))
			}
			continue
		}
		withID++
		id, ok := raw.(string)
		if !ok {
			res.problemf("%s: $id must be a string, got %T", f, raw)
			continue
		}
		want := schemaBaseURL + strings.TrimPrefix(f, contractsDir+"/")
		if id != want {
			res.problemf("%s: $id is %q, want %q ($id must equal the file's path in the contracts module)", f, id, want)
		}
	}

	res.notef("%d JSON artefacts, %d under schemas/, %d carry an $id — all checked against their path", len(files), schemas, withID)
	if schemas == 0 {
		res.problemf("no schemas found under %s/schemas — the tool is looking in the wrong place", contractsDir)
	}
	return res
}

// checkExampleNaming enforces the example file-name half of the mirror rule
// (contracts/README.md §3). The name is not cosmetic: it is how the harness finds
// the schema an example must validate against, so a `foo.json` under examples/ is
// an artefact nothing validates.
func checkExampleNaming(r *repo) result {
	var res result

	examplesRoot := contractsDir + "/examples"
	files, err := r.walkFiles(examplesRoot, ".json")
	if err != nil {
		res.problemf("%v", err)
		return res
	}

	good := 0
	for _, f := range files {
		if strings.HasSuffix(f, ".example.json") {
			good++
			continue
		}
		res.problemf("%s: every *.json under %s/ must be named <Name>.v<N>.example.json (mirror rule)", f, examplesRoot)
	}

	res.notef("%d JSON files under %s/, %d correctly named", len(files), examplesRoot, good)
	if len(files) == 0 {
		res.problemf("no examples found under %s/ — at least the envelope example must exist", examplesRoot)
	}
	return res
}
