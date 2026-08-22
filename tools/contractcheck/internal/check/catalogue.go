package check

import (
	"strings"
)

// catalogueEvents is the normative event catalogue: the 22 events of A§23, in
// A§23 order. This list is hardcoded on purpose — it is the *specification*, and
// deriving it from the files on disk would make the check tautological (a deleted
// or never-written event would silently pass).
var catalogueEvents = []string{
	"CustomerUpdated",
	"AccountUpdated",
	"DebtUpdated",
	"DelinquencyChanged",
	"CaseCreated",
	"CaseAssigned",
	"CaseResolved",
	"StrategyActivated",
	"DecisionMade",
	"TreatmentSelected",
	"ContactAttempted",
	"ContactCompleted",
	"PromiseCreated",
	"PromiseBroken",
	"ArrangementCreated",
	"ArrangementBroken",
	"PaymentReceived",
	"PaymentAllocated",
	"RecoveryRecorded",
	"DebtPlaced",
	"DebtRecalled",
	"LegalStatusChanged",
}

// extensionEvents are the catalogue extensions agreed in implementation-plan
// §6.5, justified by the A§7.2 ownership matrix: treatment-service and
// strategy-service publish state of their own, and the ingestion control plane
// publishes file lifecycle transitions.
var extensionEvents = []string{
	"TreatmentExecuted",
	"TreatmentSuppressed",
	"StrategyStateChanged",
	"StrategyRetired",
	"RuleSetPublished",
	"GuardrailConfigPublished",
	"FileStatusChanged",
}

// canonicalSnapshots are the ingestion snapshot documents that bridge CDC/files
// to the domain services (plan §6.2, CON-6). They are not events in A§23 but they
// ride the same envelope on the canonical `ingestion.*.v1` topics, so the index
// must reference them and they must ship examples like everything else.
var canonicalSnapshots = []string{
	"CustomerSnapshot",
	"AccountSnapshot",
	"DebtSnapshot",
	"PaymentNotification",
}

// checkCatalogueCoverage proves the catalogue is real: for every normative event
// there is a payload schema, a mirrored golden example, and a topic in the
// AsyncAPI index that carries it. `contracts/validate_test.go` checks the reverse
// direction (every schema on disk ships an example); together they leave no room
// for an event that exists only in prose.
func checkCatalogueCoverage(r *repo) result {
	var res result

	idx, err := loadAsyncAPI(r)
	if err != nil {
		res.problemf("%v", err)
		return res
	}

	groups := []struct {
		label  string
		dir    string
		names  []string
		source string
	}{
		{"A§23 catalogue", contractsDir + "/schemas/events", catalogueEvents, "A§23"},
		{"plan §6.5 extensions", contractsDir + "/schemas/events", extensionEvents, "plan §6.5"},
		{"canonical ingestion snapshots", contractsDir + "/schemas/ingestion", canonicalSnapshots, "plan §6.2 / CON-6"},
	}

	for _, g := range groups {
		covered := 0
		for _, name := range g.names {
			// Events are namespaced by context (schemas/events/<context>/), the
			// snapshots are not, so glob both shapes and accept any major version.
			schemas, err := r.glob(g.dir + "/*/" + name + ".v*.json")
			if err != nil {
				res.problemf("%v", err)
				continue
			}
			flat, err := r.glob(g.dir + "/" + name + ".v*.json")
			if err != nil {
				res.problemf("%v", err)
				continue
			}
			schemas = append(schemas, flat...)

			if len(schemas) == 0 {
				res.problemf("%s %s: no schema — expected %s/<context>/%s.v<N>.json (%s)",
					g.label, name, g.dir, name, g.source)
				continue
			}
			covered++
			for _, schema := range schemas {
				example := exampleFor(schema)
				if !r.exists(example) {
					res.problemf("%s %s: schema %s ships no example — expected %s (mirror rule)",
						g.label, name, schema, example)
				}
				if len(idx.fileRefs[schema]) == 0 {
					res.problemf("%s %s: schema %s is referenced by no message in %s — an event with no topic cannot be published",
						g.label, name, schema, asyncAPIIndex)
				}
			}
		}
		res.notef("%s: %d/%d covered (schema + example + topic)", g.label, covered, len(g.names))
	}

	return res
}

// exampleFor applies the mirror rule (contracts/README.md §3):
// contracts/schemas/<p>/<Name>.v<N>.json -> contracts/examples/<p>/<Name>.v<N>.example.json.
func exampleFor(schemaPath string) string {
	rel := strings.TrimPrefix(schemaPath, contractsDir+"/schemas/")
	return contractsDir + "/examples/" + strings.TrimSuffix(rel, ".json") + ".example.json"
}
