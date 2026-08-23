package authtest_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/canhtoanptit/collection-platform/platform/authn/authtest"
)

// claimsOf decodes a minted token's payload without verifying it: these tests
// are about what the fake issuer emits, and platform/authn's tests are about
// whether it verifies.
func claimsOf(t *testing.T, token string) map[string]any {
	t.Helper()

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3: %q", len(parts), token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("token payload is not base64url: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("token payload is not JSON: %v", err)
	}
	return claims
}

func TestIssuerServesDiscoveryAndJWKS(t *testing.T) {
	issuer := authtest.NewIssuer(t)

	resp, err := issuer.HTTPClient().Get(issuer.URL() + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatalf("fetching discovery: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("discovery status = %d, want 200", resp.StatusCode)
	}

	var discovery struct {
		Issuer  string   `json:"issuer"`
		JWKSURI string   `json:"jwks_uri"`
		Algs    []string `json:"id_token_signing_alg_values_supported"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&discovery); err != nil {
		t.Fatalf("decoding discovery: %v", err)
	}
	// A discovery document whose issuer disagrees with its URL is rejected by
	// every conforming verifier, so this is the load-bearing assertion.
	if discovery.Issuer != issuer.URL() {
		t.Errorf("discovery issuer = %q, want %q", discovery.Issuer, issuer.URL())
	}
	if len(discovery.Algs) == 0 || discovery.Algs[0] != "RS256" {
		t.Errorf("signing algs = %v, want RS256", discovery.Algs)
	}

	keys, err := issuer.HTTPClient().Get(discovery.JWKSURI)
	if err != nil {
		t.Fatalf("fetching JWKS: %v", err)
	}
	defer func() { _ = keys.Body.Close() }()

	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(keys.Body).Decode(&jwks); err != nil {
		t.Fatalf("decoding JWKS: %v", err)
	}
	if len(jwks.Keys) != 1 {
		t.Fatalf("JWKS holds %d keys, want 1", len(jwks.Keys))
	}
	key := jwks.Keys[0]
	if key.Kty != "RSA" || key.Kid == "" || key.N == "" || key.E == "" {
		t.Errorf("JWKS entry is incomplete: %+v", key)
	}
	// AQAB is base64url for the exponent 65537, which every RSA key here uses.
	if key.E != "AQAB" {
		t.Errorf("public exponent = %q, want AQAB", key.E)
	}
}

func TestTokenDefaults(t *testing.T) {
	issuer := authtest.NewIssuer(t)

	token := issuer.Token(t, "01M0MEKD80M9S346Q3D25VT4F5", []string{"cases:read"}, []string{"collector"}, "colx-api")
	claims := claimsOf(t, token)

	if got := claims["iss"]; got != issuer.URL() {
		t.Errorf("iss = %v, want %v", got, issuer.URL())
	}
	if got := claims["sub"]; got != "01M0MEKD80M9S346Q3D25VT4F5" {
		t.Errorf("sub = %v", got)
	}
	if got := claims["aud"]; got != "colx-api" {
		t.Errorf("aud = %v, want colx-api", got)
	}
	// Keycloak shape by default: space-separated logical scopes, plain groups.
	if got := claims["scope"]; got != "cases:read" {
		t.Errorf("scope = %v, want the logical colon form", got)
	}
	groups, ok := claims["groups"].([]any)
	if !ok || len(groups) != 1 || groups[0] != "collector" {
		t.Errorf("groups = %v, want [collector] in the plain groups claim", claims["groups"])
	}
	if _, present := claims["cognito:groups"]; present {
		t.Error("the default shape emitted cognito:groups")
	}

	exp, ok := claims["exp"].(float64)
	if !ok {
		t.Fatalf("exp = %v, want a number", claims["exp"])
	}
	if until := time.Until(time.Unix(int64(exp), 0)); until < 50*time.Minute || until > 70*time.Minute {
		t.Errorf("token expires in %v, want about an hour", until)
	}
}

func TestTokenShapes(t *testing.T) {
	issuer := authtest.NewIssuer(t)

	tests := []struct {
		name       string
		claims     authtest.Claims
		wantScope  string
		wantGroups []string
		groupClaim string
	}{
		{
			name:       "Keycloak",
			claims:     authtest.Claims{Shape: authtest.Keycloak, Scopes: []string{"cases:read", "cases:write"}, Groups: []string{"collector"}},
			wantScope:  "cases:read cases:write",
			wantGroups: []string{"collector"},
			groupClaim: "groups",
		},
		{
			name:       "Keycloak with full group paths",
			claims:     authtest.Claims{Shape: authtest.KeycloakGroupPaths, Scopes: []string{"cases:read"}, Groups: []string{"collector", "admin"}},
			wantScope:  "cases:read",
			wantGroups: []string{"/collector", "/admin"},
			groupClaim: "groups",
		},
		{
			name:       "Cognito",
			claims:     authtest.Claims{Shape: authtest.Cognito, Scopes: []string{"cases:read", "strategy:author"}, Groups: []string{"collector"}},
			wantScope:  "colx-api/cases.read colx-api/strategy.author",
			wantGroups: []string{"collector"},
			groupClaim: "cognito:groups",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims := claimsOf(t, issuer.TokenWith(t, tc.claims))

			if got := claims["scope"]; got != tc.wantScope {
				t.Errorf("scope = %v, want %q", got, tc.wantScope)
			}
			raw, ok := claims[tc.groupClaim].([]any)
			if !ok {
				t.Fatalf("%s = %v, want an array", tc.groupClaim, claims[tc.groupClaim])
			}
			if len(raw) != len(tc.wantGroups) {
				t.Fatalf("%s = %v, want %v", tc.groupClaim, raw, tc.wantGroups)
			}
			for i, want := range tc.wantGroups {
				if raw[i] != want {
					t.Errorf("%s[%d] = %v, want %q", tc.groupClaim, i, raw[i], want)
				}
			}
		})
	}
}

func TestTokenWithOverrides(t *testing.T) {
	issuer := authtest.NewIssuer(t)
	issuedAt := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		claims authtest.Claims
		check  func(*testing.T, map[string]any)
	}{
		{
			name:   "no audience omits aud entirely",
			claims: authtest.Claims{Scopes: []string{"cases:read"}},
			check: func(t *testing.T, c map[string]any) {
				if _, present := c["aud"]; present {
					t.Errorf("aud = %v, want it omitted", c["aud"])
				}
				if got := c["azp"]; got != "colx-api" {
					t.Errorf("azp = %v, want the default colx-api", got)
				}
			},
		},
		{
			name:   "multiple audiences become an array",
			claims: authtest.Claims{Audience: []string{"account", "colx-api"}},
			check: func(t *testing.T, c map[string]any) {
				aud, ok := c["aud"].([]any)
				if !ok || len(aud) != 2 {
					t.Fatalf("aud = %v, want a two-element array", c["aud"])
				}
			},
		},
		{
			name:   "issuer override",
			claims: authtest.Claims{Issuer: "https://keycloak.evil.example/realms/colx"},
			check: func(t *testing.T, c map[string]any) {
				if got := c["iss"]; got != "https://keycloak.evil.example/realms/colx" {
					t.Errorf("iss = %v", got)
				}
			},
		},
		{
			name:   "expiry in the past",
			claims: authtest.Claims{IssuedAt: issuedAt, Expiry: issuedAt.Add(-time.Hour)},
			check: func(t *testing.T, c map[string]any) {
				exp, _ := c["exp"].(float64)
				if int64(exp) != issuedAt.Add(-time.Hour).Unix() {
					t.Errorf("exp = %v, want %d", c["exp"], issuedAt.Add(-time.Hour).Unix())
				}
			},
		},
		{
			name:   "scp array instead of the scope string",
			claims: authtest.Claims{Scopes: []string{"cases:read", "cases:write"}, ScopeClaim: "scp"},
			check: func(t *testing.T, c map[string]any) {
				if _, present := c["scope"]; present {
					t.Errorf("scope = %v, want it omitted when scp is used", c["scope"])
				}
				scp, ok := c["scp"].([]any)
				if !ok || len(scp) != 2 {
					t.Fatalf("scp = %v, want a two-element array", c["scp"])
				}
			},
		},
		{
			name:   "client_id for a machine token",
			claims: authtest.Claims{ClientID: "platform-services"},
			check: func(t *testing.T, c map[string]any) {
				if got := c["client_id"]; got != "platform-services" {
					t.Errorf("client_id = %v", got)
				}
			},
		},
		{
			name:   "extra claims are merged and win",
			claims: authtest.Claims{Subject: "sub-1", Extra: map[string]any{"sub": "overridden", "realm_access": map[string]any{"roles": []string{"admin"}}}},
			check: func(t *testing.T, c map[string]any) {
				if got := c["sub"]; got != "overridden" {
					t.Errorf("sub = %v, want the Extra override", got)
				}
				if _, present := c["realm_access"]; !present {
					t.Error("realm_access was not merged in")
				}
			},
		},
		{
			name:   "no scopes omits the scope claim",
			claims: authtest.Claims{Audience: []string{"colx-api"}},
			check: func(t *testing.T, c map[string]any) {
				if _, present := c["scope"]; present {
					t.Errorf("scope = %v, want it omitted", c["scope"])
				}
				if _, present := c["groups"]; present {
					t.Errorf("groups = %v, want it omitted", c["groups"])
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, claimsOf(t, issuer.TokenWith(t, tc.claims)))
		})
	}
}

// TestSignWithUnknownKeyProducesADifferentSignature: the unpublished key must
// really be a different key, or the "untrusted signature" test in platform/authn
// would be vacuous.
func TestSignWithUnknownKeyProducesADifferentSignature(t *testing.T) {
	issuer := authtest.NewIssuer(t)
	claims := authtest.Claims{
		Subject:  "01M0MEKD80M9S346Q3D25VT4F5",
		Audience: []string{"colx-api"},
		IssuedAt: time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC),
		Expiry:   time.Date(2026, 8, 22, 11, 0, 0, 0, time.UTC),
	}

	trusted := issuer.TokenWith(t, claims)
	claims.SignWithUnknownKey = true
	untrusted := issuer.TokenWith(t, claims)

	trustedParts := strings.Split(trusted, ".")
	untrustedParts := strings.Split(untrusted, ".")
	if trustedParts[1] != untrustedParts[1] {
		t.Fatal("the two tokens differ in their payload, not just their signature")
	}
	if trustedParts[2] == untrustedParts[2] {
		t.Error("SignWithUnknownKey produced the same signature as the published key")
	}
}

// TestTwoIssuersAreIndependent: a test that needs a second realm must get one
// with its own keys and its own URL.
func TestTwoIssuersAreIndependent(t *testing.T) {
	first := authtest.NewIssuer(t)
	second := authtest.NewIssuer(t)

	if first.URL() == second.URL() {
		t.Fatal("two issuers share one URL")
	}
	claims := authtest.Claims{Subject: "s", Audience: []string{"colx-api"}, IssuedAt: time.Unix(1, 0), Expiry: time.Unix(3601, 0)}
	a := strings.Split(first.TokenWith(t, claims), ".")
	b := strings.Split(second.TokenWith(t, claims), ".")
	if a[2] == b[2] {
		t.Error("two issuers signed identical claims identically — they share a key")
	}
}
