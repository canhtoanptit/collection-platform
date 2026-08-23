// Package authn verifies bearer tokens and enforces scopes, per service, from
// day one.
//
// Every service validates JWTs itself: the gateway is not the authorization
// boundary, so a service stays safe if the gateway is bypassed or removed
// (ADR-0011, which the Keycloak decision in ADR-0017 leaves in force). The
// issuer is **Keycloak** — discovered over OIDC, JWKS cached and refreshed by
// key id — and nothing here is provider-specific beyond one dormant
// compatibility rule documented on NormalizeScope.
//
// Authorization is **deny by default**. A route reaches its handler only through
// RequireScope or RequireGroup; a route with neither has no authorization, which
// is a review finding, not a shortcut. Every denial is the A§20 error body —
// 401 with no usable token, 403 with a token that lacks the scope.
//
// Wiring, inside the httpkit chain so 401 and 403 bodies carry the correlation
// id:
//
//	auth, err := authn.Middleware(ctx, authn.OIDCConfig{Issuer: cfg.OIDCIssuer, Audience: cfg.OIDCAudience})
//	mux.Handle("GET /v1/cases/{caseId}", httpkit.Chain(auth, authn.RequireScope("cases:read"))(handler))
package authn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/canhtoanptit/collection-platform/platform/apierror"
	"github.com/canhtoanptit/collection-platform/platform/httpkit"
)

// Error codes and detail reasons this package emits. They are part of the API
// contract: clients branch on them.
const (
	// codeUnauthenticated is the 401 code — no token, or one this service will
	// not trust.
	codeUnauthenticated = apierror.CodeUnauthenticated
	// codeForbidden is the 403 code — a valid token without the required scope
	// or group.
	codeForbidden = apierror.CodeForbidden

	reasonMissingScope = "MISSING_SCOPE"
	reasonMissingGroup = "MISSING_GROUP"
)

// Messages are deliberately uninformative about *why* a token failed: an
// attacker learns nothing from "expired" versus "bad signature", and an operator
// has the correlation id and the logs.
const (
	msgUnauthenticated = "Authentication required"
	msgForbiddenScope  = "Insufficient scope"
	msgForbiddenGroup  = "Insufficient group membership"
)

// bearerPrefix is the only authorization scheme this platform accepts.
const bearerPrefix = "Bearer "

// OIDCConfig configures token verification.
type OIDCConfig struct {
	// Issuer is the OIDC issuer URL — the Keycloak realm, e.g.
	// https://keycloak.internal/realms/colx. Discovery reads
	// <Issuer>/.well-known/openid-configuration and the `issuer` it returns
	// must match, which is what stops a token from another realm being
	// accepted.
	Issuer string
	// Audience is the audience a token must be issued for: the API's client id.
	// Required — an empty audience would accept any token the issuer ever
	// minted, including one for a completely different application.
	Audience string
	// HTTPClient fetches discovery and JWKS documents. Nil uses the default
	// client. Set it for a proxy, a custom CA, or a test issuer.
	HTTPClient *http.Client
	// Now overrides the clock used for expiry checks. Tests set it; production
	// leaves it nil.
	Now func() time.Time
}

// Principal is the verified identity of a caller.
type Principal struct {
	// Subject is the token's `sub`: a Keycloak user id for a human, a service
	// account id for a machine client.
	Subject string
	// Scopes are the caller's scopes in logical colon form, deduplicated
	// (see NormalizeScope).
	Scopes []string
	// Groups are the caller's groups as bare names (see NormalizeGroup).
	Groups []string
}

// HasScope reports whether the principal holds scope, which must already be in
// logical colon form.
func (p Principal) HasScope(scope string) bool {
	for _, s := range p.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// InGroup reports whether the principal belongs to group.
func (p Principal) InGroup(group string) bool {
	for _, g := range p.Groups {
		if g == group {
			return true
		}
	}
	return false
}

// principalKey is the context key for the verified principal.
type principalKey struct{}

// ContextWithPrincipal returns ctx carrying p. Middleware does this; a test or
// an alternative authentication path (a signed internal call) may do it
// directly.
func ContextWithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the verified principal in ctx. The boolean is false when
// the request did not pass through Middleware — treat that as unauthenticated,
// never as "no restrictions".
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	if ctx == nil {
		return Principal{}, false
	}
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Middleware verifies the bearer token on every request and puts the resulting
// Principal in the context.
//
// It performs OIDC discovery against cfg.Issuer while it is constructed, so a
// misconfigured issuer fails at startup rather than on the first request. ctx is
// used for that discovery only; the returned middleware uses each request's own
// context. Call it once from main.
//
// A request with no token, an unparseable token, a token from another issuer, an
// expired token, or a token for another audience gets 401 with the A§20 body and
// no detail about which of those it was.
func Middleware(ctx context.Context, cfg OIDCConfig) (httpkit.Middleware, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("configuring authentication: no OIDC issuer — a service cannot verify tokens without one")
	}
	if cfg.Audience == "" {
		return nil, errors.New("configuring authentication: no audience — an empty audience accepts every token the issuer ever minted")
	}

	discoveryCtx := ctx
	if cfg.HTTPClient != nil {
		discoveryCtx = oidc.ClientContext(ctx, cfg.HTTPClient)
	}

	provider, err := oidc.NewProvider(discoveryCtx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discovering the OIDC issuer %s: %w", cfg.Issuer, err)
	}

	verifier := provider.Verifier(&oidc.Config{
		// The audience is checked below rather than here: an OAuth2 *access*
		// token does not always carry `aud` (Cognito puts the client id in
		// `client_id`, Keycloak in `azp`), and go-oidc only knows how to check
		// `aud`. Skipping its check and doing all three explicitly is the only
		// way to be both correct and provider-neutral — it is not a relaxation,
		// audienceMatches rejects a token that satisfies none of them.
		SkipClientIDCheck: true,
		Now:               cfg.Now,
	})

	// The key set is fetched lazily and cached by the provider, and refreshed
	// when a token arrives with an unknown key id — so a Keycloak key rotation
	// needs no restart.
	auth := &authenticator{verifier: verifier, audience: cfg.Audience, httpClient: cfg.HTTPClient}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, err := auth.authenticate(r)
			if err != nil {
				apierror.Write(w, r, apierror.
					Unauthorized(codeUnauthenticated, msgUnauthenticated).
					WithCause(err))
				return
			}
			next.ServeHTTP(w, r.WithContext(ContextWithPrincipal(r.Context(), principal)))
		})
	}, nil
}

// authenticator holds the verification state shared by every request.
type authenticator struct {
	verifier   *oidc.IDTokenVerifier
	audience   string
	httpClient *http.Client
}

// authenticate verifies the request's bearer token and builds its Principal.
// The returned error is for logs only — the response body never explains which
// check failed.
func (a *authenticator) authenticate(r *http.Request) (Principal, error) {
	raw, err := bearerToken(r.Header.Get("Authorization"))
	if err != nil {
		return Principal{}, err
	}

	ctx := r.Context()
	if a.httpClient != nil {
		ctx = oidc.ClientContext(ctx, a.httpClient)
	}

	token, err := a.verifier.Verify(ctx, raw)
	if err != nil {
		return Principal{}, fmt.Errorf("verifying the bearer token: %w", err)
	}

	var claims tokenClaims
	if err := token.Claims(&claims); err != nil {
		return Principal{}, fmt.Errorf("reading token claims: %w", err)
	}
	if !audienceMatches(a.audience, token.Audience, claims) {
		return Principal{}, fmt.Errorf("token audience %v is not %q", token.Audience, a.audience)
	}

	return Principal{
		Subject: token.Subject,
		Scopes:  normalizeAll(claims.scopes(), NormalizeScope),
		Groups:  normalizeAll(claims.groups(), NormalizeGroup),
	}, nil
}

// bearerToken extracts the credential from an Authorization header.
func bearerToken(header string) (string, error) {
	if header == "" {
		return "", errors.New("no Authorization header")
	}
	// Case-insensitive scheme per RFC 7235, exact "Bearer " length.
	if len(header) <= len(bearerPrefix) || !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return "", errors.New("the Authorization header is not a Bearer credential")
	}
	token := strings.TrimSpace(header[len(bearerPrefix):])
	if token == "" {
		return "", errors.New("the Bearer credential is empty")
	}
	return token, nil
}

// audienceMatches reports whether the token was issued for this API.
//
// Three claims are accepted, in the order a real deployment produces them:
// `aud` (an ID token, and a Keycloak access token with an audience mapper),
// `azp` (Keycloak's authorized party — the client the token was issued to), and
// `client_id` (an OAuth2 access token from a client-credentials grant).
func audienceMatches(want string, aud []string, claims tokenClaims) bool {
	for _, a := range aud {
		if a == want {
			return true
		}
	}
	return claims.AuthorizedParty == want || claims.ClientID == want
}

// tokenClaims is the claim set this platform reads. Scope and group claims are
// held raw because their JSON shape differs by provider and, for `scp`, by
// deployment: a string and an array both occur in the wild.
type tokenClaims struct {
	// Scope is the OAuth2 `scope` claim: space-separated. This is what Keycloak
	// issues, and the platform's primary shape.
	Scope string `json:"scope"`
	// Scp is the `scp` claim some issuers use instead — a string or an array.
	Scp json.RawMessage `json:"scp"`
	// Groups is the plain `groups` claim, which Keycloak's group-membership
	// mapper populates.
	Groups json.RawMessage `json:"groups"`
	// CognitoGroups is the `cognito:groups` claim. Dormant compatibility — see
	// NormalizeScope.
	CognitoGroups json.RawMessage `json:"cognito:groups"`
	// AuthorizedParty is Keycloak's `azp`.
	AuthorizedParty string `json:"azp"`
	// ClientID is the `client_id` of an OAuth2 access token.
	ClientID string `json:"client_id"`
}

// scopes collects every scope the token declares, before normalization.
func (c tokenClaims) scopes() []string {
	scopes := strings.Fields(c.Scope)
	return append(scopes, flexibleStrings(c.Scp)...)
}

// groups collects every group the token declares, before normalization.
func (c tokenClaims) groups() []string {
	return append(flexibleStrings(c.Groups), flexibleStrings(c.CognitoGroups)...)
}

// flexibleStrings reads a claim that may be a JSON array of strings or a single
// space-separated string. Anything else yields nothing: a malformed claim must
// not grant a scope, and it must not fail the whole request either — the caller
// simply has no scopes and gets a 403.
func flexibleStrings(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}

	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return strings.Fields(single)
	}
	return nil
}

// RequireScope allows the request only if the caller holds **every** listed
// scope, in logical colon form.
//
// It runs after Middleware: no principal in the context means the route was
// wired without authentication, which is answered 401 rather than allowed
// through.
func RequireScope(scopes ...string) httpkit.Middleware {
	return requireClaim(scopes, msgForbiddenScope, reasonMissingScope, "scope",
		func(p Principal, want []string) bool {
			for _, s := range want {
				if !p.HasScope(s) {
					return false
				}
			}
			return true
		})
}

// RequireGroup allows the request if the caller belongs to **at least one** of
// the listed groups.
//
// Any-of, unlike RequireScope's all-of, because groups model roles: a strategy
// activation may be approved by `business-approver` *or* `admin`, and requiring
// both would mean nobody could approve anything.
func RequireGroup(groups ...string) httpkit.Middleware {
	return requireClaim(groups, msgForbiddenGroup, reasonMissingGroup, "group",
		func(p Principal, want []string) bool {
			for _, g := range want {
				if p.InGroup(g) {
					return true
				}
			}
			return false
		})
}

// requireClaim builds an authorization middleware. A middleware constructed with
// no required values denies everything: an empty requirement is a wiring
// mistake, and deny-by-default means it must fail loudly rather than open a
// route.
func requireClaim(
	want []string,
	message, reason, field string,
	allowed func(Principal, []string) bool,
) httpkit.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFrom(r.Context())
			if !ok {
				apierror.Write(w, r, apierror.
					Unauthorized(codeUnauthenticated, msgUnauthenticated).
					WithCause(errors.New("no verified principal: the route is not behind authn.Middleware")))
				return
			}
			if len(want) == 0 || !allowed(principal, want) {
				apierror.Write(w, r, apierror.
					Forbidden(codeForbidden, message, apierror.Detail{Field: field, Reason: reason}).
					WithCause(fmt.Errorf("subject %s holds %v, needs %v", principal.Subject, principal.Scopes, want)))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
