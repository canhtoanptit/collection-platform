package authn_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/canhtoanptit/collection-platform/platform/apierror"
	"github.com/canhtoanptit/collection-platform/platform/authn"
	"github.com/canhtoanptit/collection-platform/platform/authn/authtest"
	"github.com/canhtoanptit/collection-platform/platform/httpkit"
)

const (
	// audience is the API's client id, as FND-6/ADR-0017 name it.
	audience = "colx-api"
	subject  = "01M0MEKD80M9S346Q3D25VT4F5"
)

var correlationIDPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// principalHandler reports the verified principal as JSON, so a test can assert
// on what the middleware produced rather than on a status alone.
var principalHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	p, ok := authn.PrincipalFrom(r.Context())
	if !ok {
		http.Error(w, "no principal", http.StatusTeapot)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(p); err != nil {
		panic(err)
	}
})

// newAuth builds the middleware against a fake issuer.
func newAuth(t *testing.T, issuer *authtest.Issuer, opts ...func(*authn.OIDCConfig)) httpkit.Middleware {
	t.Helper()

	cfg := authn.OIDCConfig{
		Issuer:     issuer.URL(),
		Audience:   audience,
		HTTPClient: issuer.HTTPClient(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	mw, err := authn.Middleware(t.Context(), cfg)
	if err != nil {
		t.Fatalf("authn.Middleware: %v", err)
	}
	return mw
}

// call runs a request with a bearer token through handler.
func call(t *testing.T, handler http.Handler, token string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/v1/cases", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// decodePrincipal reads the principal the handler reported.
func decodePrincipal(t *testing.T, rec *httptest.ResponseRecorder) authn.Principal {
	t.Helper()

	var p authn.Principal
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("response is not a principal (%s): %v", rec.Body, err)
	}
	return p
}

// assertErrorContract checks a denial carries the A§20 body with the code and
// nothing about why the token failed.
func assertErrorContract(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode string) apierror.Error {
	t.Helper()

	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, wantStatus, rec.Body)
	}

	var body apierror.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not the A§20 contract (%s): %v", rec.Body, err)
	}
	if body.Code != wantCode {
		t.Errorf("code = %q, want %q", body.Code, wantCode)
	}
	if body.Message == "" {
		t.Error("message is empty")
	}
	if !correlationIDPattern.MatchString(body.CorrelationID) {
		t.Errorf("correlationId = %q, want a bare ULID", body.CorrelationID)
	}
	// A denial must not explain itself: "expired" versus "bad signature" is
	// information an attacker can use.
	for _, leak := range []string{"expired", "signature", "issuer", "jwks", "audience", "oidc:"} {
		if strings.Contains(strings.ToLower(rec.Body.String()), leak) {
			t.Errorf("the error body leaked %q: %s", leak, rec.Body)
		}
	}
	return body
}

// TestKeycloakTokenShape is the primary path: Keycloak issues logical colon-form
// scopes in a space-separated `scope` claim and bare group names in `groups`.
func TestKeycloakTokenShape(t *testing.T) {
	issuer := authtest.NewIssuer(t)
	handler := newAuth(t, issuer)(principalHandler)

	tests := []struct {
		name       string
		claims     authtest.Claims
		wantScopes []string
		wantGroups []string
	}{
		{
			name: "logical scopes pass through verbatim",
			claims: authtest.Claims{
				Subject:  subject,
				Scopes:   []string{"cases:read", "cases:write"},
				Groups:   []string{"collector"},
				Audience: []string{audience},
			},
			wantScopes: []string{"cases:read", "cases:write"},
			wantGroups: []string{"collector"},
		},
		{
			name: "several groups, several scopes",
			claims: authtest.Claims{
				Scopes:   []string{"strategy:author", "decisions:read", "payments:admin"},
				Groups:   []string{"strategy-author", "business-approver", "admin"},
				Audience: []string{audience},
			},
			wantScopes: []string{"strategy:author", "decisions:read", "payments:admin"},
			wantGroups: []string{"strategy-author", "business-approver", "admin"},
		},
		{
			name: "full group paths are reduced to leaf names",
			claims: authtest.Claims{
				Shape:    authtest.KeycloakGroupPaths,
				Scopes:   []string{"cases:read"},
				Groups:   []string{"collector", "ops-admin"},
				Audience: []string{audience},
			},
			wantScopes: []string{"cases:read"},
			wantGroups: []string{"collector", "ops-admin"},
		},
		{
			name: "standard OIDC scopes ride along untouched",
			claims: authtest.Claims{
				Scopes:   []string{"openid", "profile", "email", "cases:read"},
				Audience: []string{audience},
			},
			wantScopes: []string{"openid", "profile", "email", "cases:read"},
		},
		{
			name: "a machine token with no aud is accepted on azp",
			claims: authtest.Claims{
				Scopes:          []string{"ingestion:write"},
				AuthorizedParty: audience,
			},
			wantScopes: []string{"ingestion:write"},
		},
		{
			name: "a machine token identified only by client_id",
			claims: authtest.Claims{
				Scopes:          []string{"webhook:write"},
				AuthorizedParty: "something-else",
				ClientID:        audience,
			},
			wantScopes: []string{"webhook:write"},
		},
		{
			name: "scopes in an scp array",
			claims: authtest.Claims{
				Scopes:     []string{"cases:read", "cases:write"},
				ScopeClaim: "scp",
				Audience:   []string{audience},
			},
			wantScopes: []string{"cases:read", "cases:write"},
		},
		{
			name: "a token with no scopes at all is authenticated but powerless",
			claims: authtest.Claims{
				Audience: []string{audience},
			},
		},
		{
			name: "audience is matched anywhere in a multi-valued aud",
			claims: authtest.Claims{
				Scopes:   []string{"cases:read"},
				Audience: []string{"account", audience, "realm-management"},
			},
			wantScopes: []string{"cases:read"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(t, handler, issuer.TokenWith(t, tc.claims))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
			}

			got := decodePrincipal(t, rec)
			if !equalStrings(got.Scopes, tc.wantScopes) {
				t.Errorf("scopes = %v, want %v", got.Scopes, tc.wantScopes)
			}
			if !equalStrings(got.Groups, tc.wantGroups) {
				t.Errorf("groups = %v, want %v", got.Groups, tc.wantGroups)
			}
			if got.Subject == "" {
				t.Error("subject is empty")
			}
		})
	}
}

// TestCognitoTokenShapeStillNormalizes exercises the dormant compatibility path:
// a resource-server-prefixed dot-form scope must arrive as a logical scope, and
// `cognito:groups` must be read.
func TestCognitoTokenShapeStillNormalizes(t *testing.T) {
	issuer := authtest.NewIssuer(t)
	handler := newAuth(t, issuer)(principalHandler)

	rec := call(t, handler, issuer.TokenWith(t, authtest.Claims{
		Shape:    authtest.Cognito,
		Subject:  subject,
		Scopes:   []string{"cases:read", "strategy:author"},
		Groups:   []string{"collector", "strategy-author"},
		Audience: []string{audience},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}

	got := decodePrincipal(t, rec)
	if want := []string{"cases:read", "strategy:author"}; !equalStrings(got.Scopes, want) {
		t.Errorf("scopes = %v, want %v — colx-api/cases.read must normalize", got.Scopes, want)
	}
	if want := []string{"collector", "strategy-author"}; !equalStrings(got.Groups, want) {
		t.Errorf("groups = %v, want %v — cognito:groups must be read", got.Groups, want)
	}
}

// TestBothGroupClaimsAreRead: a token carrying `groups` and `cognito:groups`
// (a migration window) yields the union, deduplicated.
func TestBothGroupClaimsAreRead(t *testing.T) {
	issuer := authtest.NewIssuer(t)
	handler := newAuth(t, issuer)(principalHandler)

	rec := call(t, handler, issuer.TokenWith(t, authtest.Claims{
		Groups:   []string{"collector"},
		Audience: []string{audience},
		Extra:    map[string]any{"cognito:groups": []string{"admin", "collector"}},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}

	got := decodePrincipal(t, rec)
	if want := []string{"collector", "admin"}; !equalStrings(got.Groups, want) {
		t.Errorf("groups = %v, want %v", got.Groups, want)
	}
}

func TestUnauthenticatedRequests(t *testing.T) {
	issuer := authtest.NewIssuer(t)
	other := authtest.NewIssuer(t)
	handler := newAuth(t, issuer)(principalHandler)

	tests := []struct {
		name  string
		token func(*testing.T) string
	}{
		{"no Authorization header", func(*testing.T) string { return "" }},
		{
			name: "expired token",
			token: func(t *testing.T) string {
				return issuer.TokenWith(t, authtest.Claims{
					Audience: []string{audience},
					Scopes:   []string{"cases:read"},
					IssuedAt: time.Now().Add(-2 * time.Hour),
					Expiry:   time.Now().Add(-time.Hour),
				})
			},
		},
		{
			name: "token from another issuer (another realm)",
			token: func(t *testing.T) string {
				return other.TokenWith(t, authtest.Claims{Audience: []string{audience}, Scopes: []string{"cases:read"}})
			},
		},
		{
			name: "issuer claim rewritten to another realm",
			token: func(t *testing.T) string {
				return issuer.TokenWith(t, authtest.Claims{
					Issuer:   "https://keycloak.evil.example/realms/colx",
					Audience: []string{audience},
				})
			},
		},
		{
			name: "wrong audience",
			token: func(t *testing.T) string {
				return issuer.TokenWith(t, authtest.Claims{
					Audience:        []string{"some-other-api"},
					AuthorizedParty: "some-other-api",
					Scopes:          []string{"cases:read"},
				})
			},
		},
		{
			name: "no audience, azp or client_id this API recognises",
			token: func(t *testing.T) string {
				return issuer.TokenWith(t, authtest.Claims{
					AuthorizedParty: "another-client",
					ClientID:        "another-client",
					Scopes:          []string{"cases:read"},
				})
			},
		},
		{
			name: "signed by a key the issuer does not publish",
			token: func(t *testing.T) string {
				return issuer.TokenWith(t, authtest.Claims{
					Audience:           []string{audience},
					SignWithUnknownKey: true,
				})
			},
		},
		{
			name: "tampered signature",
			token: func(t *testing.T) string {
				token := issuer.TokenWith(t, authtest.Claims{Audience: []string{audience}})
				return token[:len(token)-4] + "AAAA"
			},
		},
		{
			name: "tampered payload",
			token: func(t *testing.T) string {
				token := issuer.TokenWith(t, authtest.Claims{Audience: []string{audience}})
				parts := strings.Split(token, ".")
				return parts[0] + "." + parts[1][:len(parts[1])-4] + "AAAA." + parts[2]
			},
		},
		{"not a JWT at all", func(*testing.T) string { return "hunter2" }},
		{"two segments only", func(*testing.T) string { return "aaa.bbb" }},
		{"empty bearer value", func(*testing.T) string { return " " }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(t, handler, tc.token(t))
			assertErrorContract(t, rec, http.StatusUnauthorized, apierror.CodeUnauthenticated)
		})
	}
}

func TestMalformedAuthorizationHeaders(t *testing.T) {
	issuer := authtest.NewIssuer(t)
	auth := newAuth(t, issuer)
	handler := auth(principalHandler)
	token := issuer.TokenWith(t, authtest.Claims{Audience: []string{audience}})

	tests := []struct {
		name   string
		header string
		wantOK bool
	}{
		{"canonical Bearer", "Bearer " + token, true},
		{"lowercase scheme (RFC 7235 is case-insensitive)", "bearer " + token, true},
		{"mixed case scheme", "BeArEr " + token, true},
		{"extra whitespace after the scheme", "Bearer   " + token, true},
		{"no scheme", token, false},
		{"Basic credentials", "Basic dXNlcjpwYXNz", false},
		{"scheme only", "Bearer", false},
		{"scheme and a space", "Bearer ", false},
		{"empty header", "", false},
		{"a bearer-looking prefix", "Bearertoken", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/cases", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if tc.wantOK && rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
			}
			if !tc.wantOK && rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401 (body %s)", rec.Code, rec.Body)
			}
		})
	}
}

func TestRequireScope(t *testing.T) {
	issuer := authtest.NewIssuer(t)
	auth := newAuth(t, issuer)

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "granted")
	})

	tests := []struct {
		name       string
		held       []string
		required   []string
		wantStatus int
	}{
		{"exact match", []string{"cases:read"}, []string{"cases:read"}, http.StatusOK},
		{"one of several held", []string{"cases:read", "cases:write"}, []string{"cases:write"}, http.StatusOK},
		{"all required scopes held", []string{"cases:read", "cases:write"}, []string{"cases:read", "cases:write"}, http.StatusOK},
		{"missing scope", []string{"cases:read"}, []string{"cases:write"}, http.StatusForbidden},
		{"only one of two required scopes held", []string{"cases:read"}, []string{"cases:read", "cases:write"}, http.StatusForbidden},
		{"no scopes at all", nil, []string{"cases:read"}, http.StatusForbidden},
		{"a similar-looking scope does not count", []string{"cases:readonly"}, []string{"cases:read"}, http.StatusForbidden},
		{"the dot form is not accepted as a logical scope", []string{"cases:read"}, []string{"cases.read"}, http.StatusForbidden},
		{"an empty requirement denies (deny by default)", []string{"cases:read"}, nil, http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := httpkit.Chain(
				httpkit.CorrelationID(),
				auth,
				authn.RequireScope(tc.required...),
			)(ok)

			token := issuer.TokenWith(t, authtest.Claims{Scopes: tc.held, Audience: []string{audience}})
			rec := call(t, handler, token)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body)
			}
			if tc.wantStatus != http.StatusForbidden {
				return
			}

			body := assertErrorContract(t, rec, http.StatusForbidden, apierror.CodeForbidden)
			if len(body.Details) != 1 {
				t.Fatalf("details = %v, want exactly one", body.Details)
			}
			if body.Details[0].Field != "scope" || body.Details[0].Reason != "MISSING_SCOPE" {
				t.Errorf("detail = %+v, want {scope MISSING_SCOPE}", body.Details[0])
			}
		})
	}
}

func TestRequireGroup(t *testing.T) {
	issuer := authtest.NewIssuer(t)
	auth := newAuth(t, issuer)

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "granted")
	})

	tests := []struct {
		name       string
		held       []string
		shape      authtest.Shape
		required   []string
		wantStatus int
	}{
		{"exact match", []string{"collector"}, authtest.Keycloak, []string{"collector"}, http.StatusOK},
		{
			name: "any one of several satisfies", held: []string{"admin"}, shape: authtest.Keycloak,
			required: []string{"business-approver", "admin"}, wantStatus: http.StatusOK,
		},
		{
			name: "full group paths still match", held: []string{"business-approver"}, shape: authtest.KeycloakGroupPaths,
			required: []string{"business-approver"}, wantStatus: http.StatusOK,
		},
		{
			name: "cognito:groups still match", held: []string{"risk-approver"}, shape: authtest.Cognito,
			required: []string{"risk-approver"}, wantStatus: http.StatusOK,
		},
		{"not a member", []string{"collector"}, authtest.Keycloak, []string{"admin"}, http.StatusForbidden},
		{"no groups at all", nil, authtest.Keycloak, []string{"admin"}, http.StatusForbidden},
		{"an empty requirement denies", []string{"admin"}, authtest.Keycloak, nil, http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := httpkit.Chain(
				httpkit.CorrelationID(),
				auth,
				authn.RequireGroup(tc.required...),
			)(ok)

			token := issuer.TokenWith(t, authtest.Claims{
				Groups:   tc.held,
				Shape:    tc.shape,
				Audience: []string{audience},
			})
			rec := call(t, handler, token)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body)
			}
			if tc.wantStatus != http.StatusForbidden {
				return
			}
			body := assertErrorContract(t, rec, http.StatusForbidden, apierror.CodeForbidden)
			if len(body.Details) != 1 || body.Details[0].Field != "group" || body.Details[0].Reason != "MISSING_GROUP" {
				t.Errorf("details = %+v, want {group MISSING_GROUP}", body.Details)
			}
		})
	}
}

// TestAuthorizationWithoutAuthenticationIsRefused: a route wired with
// RequireScope but no Middleware must answer 401, not run the handler. This is
// the deny-by-default property, tested against the most likely wiring mistake.
func TestAuthorizationWithoutAuthenticationIsRefused(t *testing.T) {
	var reached bool
	handler := httpkit.Chain(
		httpkit.CorrelationID(),
		authn.RequireScope("cases:read"),
	)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/cases", nil))

	if reached {
		t.Error("the handler ran without any authentication")
	}
	assertErrorContract(t, rec, http.StatusUnauthorized, apierror.CodeUnauthenticated)
}

// TestDenialsCarryTheRequestCorrelationID: a 401 or 403 must be traceable to the
// request that produced it.
func TestDenialsCarryTheRequestCorrelationID(t *testing.T) {
	const inbound = "01M0MEKBHXV37E3S3E28JT97KB"

	issuer := authtest.NewIssuer(t)
	handler := httpkit.Chain(
		httpkit.CorrelationID(),
		newAuth(t, issuer),
		authn.RequireScope("cases:write"),
	)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	for _, tc := range []struct {
		name       string
		token      string
		wantStatus int
	}{
		{"401", "", http.StatusUnauthorized},
		{"403", issuer.TokenWith(t, authtest.Claims{Scopes: []string{"cases:read"}, Audience: []string{audience}}), http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/cases", nil)
			req.Header.Set(httpkit.CorrelationHeader, inbound)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			var body apierror.Error
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not the error contract: %v", err)
			}
			if body.CorrelationID != inbound {
				t.Errorf("correlationId = %q, want the inbound %q", body.CorrelationID, inbound)
			}
			if got := rec.Header().Get(httpkit.CorrelationHeader); got != inbound {
				t.Errorf("%s = %q, want %q", httpkit.CorrelationHeader, got, inbound)
			}
		})
	}
}

func TestMiddlewareConfigValidation(t *testing.T) {
	issuer := authtest.NewIssuer(t)

	tests := []struct {
		name   string
		cfg    authn.OIDCConfig
		wantIn string
	}{
		{"no issuer", authn.OIDCConfig{Audience: audience}, "no OIDC issuer"},
		{"no audience", authn.OIDCConfig{Issuer: issuer.URL()}, "no audience"},
		{
			name:   "unreachable issuer",
			cfg:    authn.OIDCConfig{Issuer: "http://127.0.0.1:1/realms/colx", Audience: audience},
			wantIn: "discovering the OIDC issuer",
		},
		{
			name:   "an issuer whose discovery document disagrees with its URL",
			cfg:    authn.OIDCConfig{Issuer: issuer.URL() + "/", Audience: audience, HTTPClient: issuer.HTTPClient()},
			wantIn: "discovering the OIDC issuer",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			mw, err := authn.Middleware(ctx, tc.cfg)
			if err == nil {
				t.Fatal("Middleware accepted an invalid configuration")
			}
			if mw != nil {
				t.Error("Middleware returned a middleware alongside the error")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantIn)
			}
		})
	}
}

func TestPrincipalHelpers(t *testing.T) {
	p := authn.Principal{
		Subject: subject,
		Scopes:  []string{"cases:read", "payments:admin"},
		Groups:  []string{"collector", "admin"},
	}

	tests := []struct {
		name  string
		check bool
		want  bool
	}{
		{"held scope", p.HasScope("cases:read"), true},
		{"other held scope", p.HasScope("payments:admin"), true},
		{"missing scope", p.HasScope("cases:write"), false},
		{"empty scope", p.HasScope(""), false},
		{"member group", p.InGroup("collector"), true},
		{"non-member group", p.InGroup("analyst"), false},
		{"empty group", p.InGroup(""), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.check != tc.want {
				t.Errorf("got %t, want %t", tc.check, tc.want)
			}
		})
	}

	if _, ok := authn.PrincipalFrom(t.Context()); ok {
		t.Error("PrincipalFrom found a principal in a bare context")
	}
	if _, ok := authn.PrincipalFrom(nilContext()); ok {
		t.Error("PrincipalFrom(nil) reported a principal")
	}
	got, ok := authn.PrincipalFrom(authn.ContextWithPrincipal(t.Context(), p))
	if !ok {
		t.Fatal("PrincipalFrom did not find the principal that was just stored")
	}
	if got.Subject != p.Subject || !equalStrings(got.Scopes, p.Scopes) {
		t.Errorf("principal = %+v, want %+v", got, p)
	}
}

// TestExpiryIsCheckedAgainstTheConfiguredClock proves OIDCConfig.Now is wired,
// which is what lets a test pin token lifetimes without sleeping.
func TestExpiryIsCheckedAgainstTheConfiguredClock(t *testing.T) {
	issuer := authtest.NewIssuer(t)

	issuedAt := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	token := issuer.TokenWith(t, authtest.Claims{
		Audience: []string{audience},
		Scopes:   []string{"cases:read"},
		IssuedAt: issuedAt,
		Expiry:   issuedAt.Add(time.Hour),
	})

	inside := newAuth(t, issuer, func(cfg *authn.OIDCConfig) {
		cfg.Now = func() time.Time { return issuedAt.Add(30 * time.Minute) }
	})
	if rec := call(t, inside(principalHandler), token); rec.Code != http.StatusOK {
		t.Errorf("inside the token's lifetime: status = %d, want 200 (%s)", rec.Code, rec.Body)
	}

	outside := newAuth(t, issuer, func(cfg *authn.OIDCConfig) {
		cfg.Now = func() time.Time { return issuedAt.Add(2 * time.Hour) }
	})
	if rec := call(t, outside(principalHandler), token); rec.Code != http.StatusUnauthorized {
		t.Errorf("after the token expired: status = %d, want 401 (%s)", rec.Code, rec.Body)
	}
}

// nilContext returns a nil context.Context. Passing nil is a caller bug, but the
// accessors must not panic on it: they run on error paths, where a panic would
// replace a useful failure with a useless one. Returned from a function so the
// deliberate nil is not mistaken for the accidental kind.
func nilContext() context.Context { return nil }

// equalStrings compares two slices, treating nil and empty as the same.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
