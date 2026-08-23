// Package authtest is a fake OIDC issuer for tests: an RSA key, a JWKS
// endpoint, a discovery document, and a token minter.
//
// It exists so the authorization path — token to scope to 200 or 403 — is
// exercised in every service's unit tests and in the E2E stack, without a real
// Keycloak and without a network. E2E-0 serves the same issuer as `mockidp`, so
// the token shape a service sees locally is the shape it sees in tests.
//
// Tokens are Keycloak-shaped by default: logical colon-form scopes in a
// space-separated `scope` claim, and groups in a plain `groups` claim. Use
// Cognito to mint the resource-server/dot shape instead and prove the dormant
// normalization in platform/authn still works.
package authtest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Shape selects the claim names and scope spelling a token uses.
type Shape int

const (
	// Keycloak is the platform's issuer: `scope` holds space-separated logical
	// colon-form scopes ("cases:read cases:write") and `groups` holds bare
	// group names.
	Keycloak Shape = iota
	// KeycloakGroupPaths is Keycloak with the group-membership mapper's "full
	// group path" option on, so groups arrive as "/collector".
	KeycloakGroupPaths
	// Cognito is the dormant compatibility shape: `scope` holds
	// resource-server-prefixed dot-form scopes ("colx-api/cases.read") and
	// groups arrive in `cognito:groups`.
	Cognito
)

// resourceServer is the Cognito resource server the Cognito shape prefixes
// scopes with (the name FND-6 declared before the Keycloak decision).
const resourceServer = "colx-api"

// keyID is the single signing key id this issuer publishes.
const keyID = "authtest-rsa-1"

// Issuer is a fake OIDC provider backed by an httptest server. Its lifetime is
// the test's: the server is closed by t.Cleanup.
type Issuer struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	// otherKey signs tokens for the "signed by a key the issuer does not
	// publish" case. Its public half is never in the JWKS.
	otherKey *rsa.PrivateKey
}

// NewIssuer starts a fake issuer serving OIDC discovery and JWKS.
//
// The RSA key is 2048 bits — the smallest size a real verifier accepts, so the
// test path and the production path use the same algorithm.
func NewIssuer(t *testing.T) *Issuer {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating the test issuer's RSA key: %v", err)
	}
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating the unpublished RSA key: %v", err)
	}

	issuer := &Issuer{key: key, otherKey: otherKey}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		issuer.writeJSON(t, w, map[string]any{
			"issuer":                                issuer.URL(),
			"authorization_endpoint":                issuer.URL() + "/protocol/openid-connect/auth",
			"token_endpoint":                        issuer.URL() + "/protocol/openid-connect/token",
			"jwks_uri":                              issuer.URL() + "/protocol/openid-connect/certs",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /protocol/openid-connect/certs", func(w http.ResponseWriter, _ *http.Request) {
		issuer.writeJSON(t, w, map[string]any{"keys": []any{jwk(key.Public().(*rsa.PublicKey))}})
	})

	issuer.server = httptest.NewServer(mux)
	t.Cleanup(issuer.server.Close)
	return issuer
}

// URL is the issuer URL to configure authn.OIDCConfig.Issuer with.
func (i *Issuer) URL() string { return i.server.URL }

// HTTPClient is the client to pass as authn.OIDCConfig.HTTPClient. The default
// client would also work against httptest, but going through this one keeps a
// test from reaching anything but the fake issuer.
func (i *Issuer) HTTPClient() *http.Client { return i.server.Client() }

// Token mints a token valid for one hour: subject sub, the given scopes and
// groups, audience aud, in the Keycloak shape.
func (i *Issuer) Token(t *testing.T, sub string, scopes, groups []string, aud string) string {
	t.Helper()

	return i.TokenWith(t, Claims{
		Subject:  sub,
		Scopes:   scopes,
		Groups:   groups,
		Audience: []string{aud},
	})
}

// Claims describes a token to mint. Every zero value has a sensible default, so
// a test states only what it is exercising.
type Claims struct {
	// Subject is the `sub` claim. Defaults to "01M0MEKD80M9S346Q3D25VT4F5".
	Subject string
	// Audience is the `aud` claim. Defaults to none, in which case
	// AuthorizedParty carries the audience instead (the Keycloak access-token
	// shape).
	Audience []string
	// Issuer overrides the `iss` claim, for proving a token from another realm
	// is rejected. Defaults to this issuer's URL.
	Issuer string
	// Scopes and Groups are logical colon-form scopes and bare group names.
	// Shape decides how they are spelled on the wire.
	Scopes []string
	Groups []string
	// Shape selects the provider's claim names and spelling. Defaults to
	// Keycloak.
	Shape Shape
	// ScopeClaim overrides which claim carries the scopes: "" or "scope" for
	// the space-separated OAuth2 claim, "scp" for a JSON array.
	ScopeClaim string
	// AuthorizedParty is the `azp` claim. Defaults to the first audience, or to
	// "colx-api" when no audience is set.
	AuthorizedParty string
	// ClientID is the `client_id` claim of an access token. Empty omits it.
	ClientID string
	// IssuedAt defaults to now, Expiry to one hour from IssuedAt. Set Expiry in
	// the past to mint an expired token.
	IssuedAt time.Time
	Expiry   time.Time
	// Extra adds or overrides raw claims.
	Extra map[string]any
	// SignWithUnknownKey signs with a key whose public half is not in the JWKS,
	// so verification must fail.
	SignWithUnknownKey bool
}

// TokenWith mints a token from an explicit claim set.
func (i *Issuer) TokenWith(t *testing.T, c Claims) string {
	t.Helper()

	if c.Subject == "" {
		c.Subject = "01M0MEKD80M9S346Q3D25VT4F5"
	}
	if c.IssuedAt.IsZero() {
		c.IssuedAt = time.Now()
	}
	if c.Expiry.IsZero() {
		c.Expiry = c.IssuedAt.Add(time.Hour)
	}
	if c.Issuer == "" {
		c.Issuer = i.URL()
	}
	if c.AuthorizedParty == "" {
		if len(c.Audience) > 0 {
			c.AuthorizedParty = c.Audience[0]
		} else {
			c.AuthorizedParty = resourceServer
		}
	}

	claims := map[string]any{
		"iss": c.Issuer,
		"sub": c.Subject,
		"iat": c.IssuedAt.Unix(),
		"exp": c.Expiry.Unix(),
		"azp": c.AuthorizedParty,
		"typ": "Bearer",
	}
	switch len(c.Audience) {
	case 0:
		// Omitted: an OAuth2 access token need not carry `aud`.
	case 1:
		claims["aud"] = c.Audience[0]
	default:
		claims["aud"] = c.Audience
	}
	if c.ClientID != "" {
		claims["client_id"] = c.ClientID
	}

	addScopes(claims, c)
	addGroups(claims, c)

	for k, v := range c.Extra {
		claims[k] = v
	}

	key := i.key
	if c.SignWithUnknownKey {
		key = i.otherKey
	}
	return sign(t, key, claims)
}

// addScopes writes the scope claim in the shape the provider would.
func addScopes(claims map[string]any, c Claims) {
	if len(c.Scopes) == 0 {
		return
	}

	spelled := make([]string, len(c.Scopes))
	for i, s := range c.Scopes {
		if c.Shape == Cognito {
			spelled[i] = resourceServer + "/" + strings.ReplaceAll(s, ":", ".")
			continue
		}
		spelled[i] = s
	}

	if c.ScopeClaim == "scp" {
		claims["scp"] = spelled
		return
	}
	claims["scope"] = strings.Join(spelled, " ")
}

// addGroups writes the group claim in the shape the provider would.
func addGroups(claims map[string]any, c Claims) {
	if len(c.Groups) == 0 {
		return
	}

	switch c.Shape {
	case Cognito:
		claims["cognito:groups"] = c.Groups
	case KeycloakGroupPaths:
		paths := make([]string, len(c.Groups))
		for i, g := range c.Groups {
			paths[i] = "/" + g
		}
		claims["groups"] = paths
	case Keycloak:
		claims["groups"] = c.Groups
	}
}

// sign renders claims as a compact RS256 JWS.
func sign(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()

	header := base64JSON(t, map[string]any{"alg": "RS256", "typ": "JWT", "kid": keyID})
	payload := base64JSON(t, claims)
	signingInput := header + "." + payload

	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("signing the test token: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// base64JSON marshals v and encodes it base64url without padding, as JWS
// requires.
func base64JSON(t *testing.T, v any) string {
	t.Helper()

	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling a token segment: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

// jwk renders an RSA public key as a JWKS entry.
func jwk(pub *rsa.PublicKey) map[string]any {
	return map[string]any{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"kid": keyID,
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(bigEndian(pub.E)),
	}
}

// bigEndian renders the public exponent as the shortest big-endian byte slice,
// which is how a JWK encodes it.
func bigEndian(e int) []byte {
	var out []byte
	for e > 0 {
		out = append([]byte{byte(e & 0xff)}, out...)
		e >>= 8
	}
	return out
}

// writeJSON serves a discovery or JWKS document.
func (i *Issuer) writeJSON(t *testing.T, w http.ResponseWriter, doc map[string]any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(doc); err != nil {
		t.Errorf("serving the test issuer document: %v", err)
	}
}
