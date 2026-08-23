package authn_test

import (
	"testing"

	"github.com/canhtoanptit/collection-platform/platform/authn"
)

// TestNormalizeScope is the FND-6 SCOPE-FORMAT RULING in table form: services
// and contracts speak logical colon-form scopes, and this function is the only
// place a provider's spelling is absorbed.
func TestNormalizeScope(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		// Keycloak — the live path. Normalization is the identity function.
		{"logical scope passes through", "cases:read", "cases:read"},
		{"logical admin scope", "payments:admin", "payments:admin"},
		{"logical author scope", "strategy:author", "strategy:author"},
		{"standard OIDC scope", "openid", "openid"},
		{"profile", "profile", "profile"},
		{"email", "email", "email"},

		// Cognito — dormant compatibility (ADR-0011 superseded by ADR-0017).
		{"resource server prefix and dot form", "colx-api/cases.read", "cases:read"},
		{"resource server prefix, admin scope", "colx-api/payments.admin", "payments:admin"},
		{"resource server prefix, author scope", "colx-api/strategy.author", "strategy:author"},
		{"a URL-shaped resource server", "https://colx-api.example.com/cases.read", "cases:read"},
		{"dot form with no prefix", "cases.read", "cases:read"},
		{"every dot maps, not just the first", "ingestion.files.write", "ingestion:files:write"},
		{"already-logical scope behind a prefix", "colx-api/cases:read", "cases:read"},

		// Edge cases a real token can carry.
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
		{"surrounding whitespace is trimmed", "  cases:read  ", "cases:read"},
		{"prefix with nothing after it", "colx-api/", ""},
		{"a bare slash", "/", ""},
		{"leading slash", "/cases.read", "cases:read"},
		{"trailing dot", "cases.", "cases:"},
		{"multiple slashes take the last segment", "a/b/c/cases.read", "cases:read"},
		{"no separators at all", "admin", "admin"},
		{"mixed case is preserved (scopes are case-sensitive)", "Cases.Read", "Cases:Read"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := authn.NormalizeScope(tc.raw); got != tc.want {
				t.Errorf("NormalizeScope(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestNormalizeScopeIsIdempotent: normalizing a logical scope again must not
// change it, or a second pass anywhere in the stack would corrupt it.
func TestNormalizeScopeIsIdempotent(t *testing.T) {
	for _, raw := range []string{
		"cases:read", "colx-api/cases.read", "openid", "ingestion.files.write", "",
	} {
		once := authn.NormalizeScope(raw)
		if twice := authn.NormalizeScope(once); twice != once {
			t.Errorf("NormalizeScope(%q) = %q, but normalizing again gave %q", raw, once, twice)
		}
	}
}

// TestNormalizeGroup covers the Keycloak group-membership mapper's two
// configurations plus the nested-group case.
func TestNormalizeGroup(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"bare name (mapper without full paths)", "collector", "collector"},
		{"leading slash (mapper with full paths)", "/collector", "collector"},
		{"nested group path", "/collections/collector", "collector"},
		{"deeply nested", "/a/b/c/business-approver", "business-approver"},
		{"hyphenated role", "strategy-author", "strategy-author"},
		{"hyphenated role with a path", "/strategy-author", "strategy-author"},
		{"empty", "", ""},
		{"a bare slash", "/", ""},
		{"surrounding whitespace is trimmed", "  /collector  ", "collector"},
		{"trailing slash yields nothing", "/collector/", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := authn.NormalizeGroup(tc.raw); got != tc.want {
				t.Errorf("NormalizeGroup(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestNormalizeGroupIsIdempotent guards the same property for groups.
func TestNormalizeGroupIsIdempotent(t *testing.T) {
	for _, raw := range []string{"collector", "/collector", "/collections/collector", ""} {
		once := authn.NormalizeGroup(raw)
		if twice := authn.NormalizeGroup(once); twice != once {
			t.Errorf("NormalizeGroup(%q) = %q, but normalizing again gave %q", raw, once, twice)
		}
	}
}
