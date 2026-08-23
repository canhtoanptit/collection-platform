package authn

import "strings"

// NormalizeScope maps a provider's scope string to the platform's logical form.
//
// Contracts and services speak **logical colon-form scopes** — `cases:read`,
// `payments:admin`, `strategy:author` (plan FND-6 SCOPE-FORMAT RULING). Keycloak,
// the platform's issuer, emits exactly that, so normalization is the identity
// function on the live path and this is where a different provider is absorbed
// instead of leaking its spelling into every OpenAPI spec and handler.
//
// The transformation is: drop everything through the last `/`, then map `.` to
// `:`.
//
//	cases:read              -> cases:read              (Keycloak, unchanged)
//	openid                  -> openid                  (standard OIDC scope)
//	colx-api/cases.read     -> cases:read              (Cognito resource server)
//	cases.read              -> cases:read              (dot form, no prefix)
//	ingestion.files.write   -> ingestion:files:write    (every dot maps)
//
// The `resource-server/dot` handling is **dormant compatibility**: Cognito was
// the original issuer (ADR-0011) and was replaced by Keycloak (ADR-0017) before
// anything was applied. It is kept, and tested, because it costs two lines and
// removes a whole class of migration surprise if a provider that spells scopes
// that way is ever put in front of these services.
func NormalizeScope(raw string) string {
	scope := strings.TrimSpace(raw)
	if i := strings.LastIndex(scope, "/"); i >= 0 {
		scope = scope[i+1:]
	}
	return strings.ReplaceAll(scope, ".", ":")
}

// NormalizeGroup maps a provider's group name to the platform's form: a bare
// name, with no leading path separator.
//
// Keycloak's group-membership mapper emits either `collector` or `/collector`
// depending on whether "full group path" is switched on, and a nested group
// arrives as `/collections/collector`. Services check membership by leaf name
// (`RequireGroup("business-approver")`), so the path is stripped here rather
// than in every call site — a realm reconfiguration must not silently turn every
// authorization check into a denial.
func NormalizeGroup(raw string) string {
	group := strings.TrimSpace(raw)
	if i := strings.LastIndex(group, "/"); i >= 0 {
		group = group[i+1:]
	}
	return group
}

// normalizeAll applies fn to every entry, dropping empties and duplicates while
// preserving first-seen order so a Principal is stable and comparable.
func normalizeAll(raw []string, fn func(string) string) []string {
	if len(raw) == 0 {
		return nil
	}

	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, r := range raw {
		v := fn(r)
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
