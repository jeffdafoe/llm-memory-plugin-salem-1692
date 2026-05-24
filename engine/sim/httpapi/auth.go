package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// auth.go — authentication for the client read surface. Every read (REST + the
// WS /events stream) requires a valid llm-memory session token whose principal
// belongs to the "salem" realm. Ported from v1's engine/auth.go; the realm
// check is the authorization gate (a valid token from another realm is
// rejected), and a positive-result cache keeps a page-load fan-out of authed
// reads from hammering the verify backend (v1 had no cache and 503'd under load).

// AuthUser is the authenticated principal, stored in the request context for
// handlers / future write-route role gating.
type AuthUser struct {
	Username    string
	Realms      []string
	SessionKind string // "web" (admin login) | "api" (agent login)
	// Permissions is the principal's admin-permission map from llm-memory
	// ({resource: [actions]}), surfaced by /v1/auth/verify. Used by
	// requireOperator to gate the umbilical on plugins/administer. Nil/empty
	// for principals with no admin permissions (the common case).
	Permissions map[string][]string
}

// VerifyResult is the outcome of token verification. Reason is set when Valid
// is false, so callers map to the right status:
//
//	missing → 401 (no token)   invalid → 401 (bad/expired)
//	realm   → 403 (valid token, not a salem member)
//	service → 503 (verify backend unreachable/erroring)
type VerifyResult struct {
	Valid  bool
	Reason string
	User   *AuthUser
}

// Authenticator verifies a session token. The production impl
// (*TokenVerifier) calls llm-memory-api; tests inject a fake.
type Authenticator interface {
	Verify(token string) VerifyResult
}

const (
	// authRealm is the realm a token's principal must belong to.
	authRealm = "salem"
	// defaultAuthCacheTTL caps how long a positive verification is reused. A
	// fan-out of authed reads sharing one token hits the backend once per TTL,
	// not once per request. A revoked token lingers at most this long for
	// reads — 30s keeps revocation latency low (there is no active revocation
	// signal) while still collapsing a page-load fan-out to one verify.
	defaultAuthCacheTTL = 30 * time.Second
	// authVerifyTimeout bounds a single verify call. v1 needed a high ceiling
	// because /v1/auth/verify queues under parallel load; the cache makes the
	// slow path rare, but keep headroom for a cold-cache fan-out.
	authVerifyTimeout = 15 * time.Second
)

// TokenVerifier verifies session tokens against llm-memory-api's
// /v1/auth/verify and caches positive results for ttl. Safe for concurrent use.
type TokenVerifier struct {
	verifyURL string
	client    *http.Client
	ttl       time.Duration

	mu    sync.Mutex
	cache map[string]cachedVerify
}

type cachedVerify struct {
	user      *AuthUser
	expiresAt time.Time
}

// NewTokenVerifier builds a verifier hitting {llmMemoryURL}/v1/auth/verify.
// ttl <= 0 uses defaultAuthCacheTTL.
func NewTokenVerifier(llmMemoryURL string, ttl time.Duration) *TokenVerifier {
	if ttl <= 0 {
		ttl = defaultAuthCacheTTL
	}
	return &TokenVerifier{
		verifyURL: strings.TrimRight(llmMemoryURL, "/") + "/v1/auth/verify",
		client:    &http.Client{Timeout: authVerifyTimeout},
		ttl:       ttl,
		cache:     make(map[string]cachedVerify),
	}
}

// Verify resolves a token: positive cache → POST /v1/auth/verify → salem-realm
// gate. Only positive results are cached, so a freshly issued token is never
// stuck invalid for the TTL; an invalid/foreign-realm token is re-checked each
// time (the slow path, but it's the uncommon one).
func (v *TokenVerifier) Verify(token string) VerifyResult {
	if token == "" {
		return VerifyResult{Reason: "missing"}
	}
	if user, ok := v.cachedUser(token); ok {
		return VerifyResult{Valid: true, User: user}
	}

	body, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return VerifyResult{Reason: "service"}
	}
	resp, err := v.client.Post(v.verifyURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return VerifyResult{Reason: "service"}
	}
	defer resp.Body.Close()

	// Require a 2xx before decoding. A non-2xx (proxy error, misrouted
	// endpoint, backend 500) can still carry a JSON body that happens to match
	// the success shape — decoding it unconditionally would be fail-OPEN. Treat
	// any non-2xx as a service error (fail closed).
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return VerifyResult{Reason: "service"}
	}

	var out struct {
		Valid       bool                `json:"valid"`
		Agent       string              `json:"agent"`
		Realms      []string            `json:"realms"`
		SessionKind string              `json:"session_kind"`
		Permissions map[string][]string `json:"permissions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return VerifyResult{Reason: "service"}
	}
	if !out.Valid {
		return VerifyResult{Reason: "invalid"}
	}
	if !containsRealm(out.Realms, authRealm) {
		return VerifyResult{Reason: "realm"}
	}

	user := &AuthUser{Username: out.Agent, Realms: out.Realms, SessionKind: out.SessionKind, Permissions: out.Permissions}
	v.store(token, user)
	return VerifyResult{Valid: true, User: user}
}

func (v *TokenVerifier) cachedUser(token string) (*AuthUser, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	e, ok := v.cache[token]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expiresAt) {
		delete(v.cache, token) // lazy expiry
		return nil, false
	}
	// Clone on the way out so a caller (a handler reading it from the request
	// context) can't mutate the cached identity and corrupt it for every other
	// request sharing this token.
	return cloneAuthUser(e.user), true
}

func (v *TokenVerifier) store(token string, user *AuthUser) {
	v.mu.Lock()
	// Clone on the way in so the cache owns an isolated copy, independent of
	// the AuthUser handed back to this request's caller.
	v.cache[token] = cachedVerify{user: cloneAuthUser(user), expiresAt: time.Now().Add(v.ttl)}
	v.mu.Unlock()
}

// cloneAuthUser deep-copies an AuthUser (including its Realms slice) so a cached
// principal and a returned principal never share mutable state.
func cloneAuthUser(u *AuthUser) *AuthUser {
	if u == nil {
		return nil
	}
	c := *u
	c.Realms = append([]string(nil), u.Realms...)
	if u.Permissions != nil {
		c.Permissions = make(map[string][]string, len(u.Permissions))
		for k, v := range u.Permissions {
			c.Permissions[k] = append([]string(nil), v...)
		}
	}
	return &c
}

func containsRealm(realms []string, want string) bool {
	for _, r := range realms {
		if r == want {
			return true
		}
	}
	return false
}

// ctxKey is the private context-key type for the authenticated user.
type ctxKey int

const userCtxKey ctxKey = 0

// userFromContext returns the authenticated user a requireAuth-wrapped handler
// was reached with, or nil. (Reads ignore it today; write routes will gate on
// role.)
func userFromContext(ctx context.Context) *AuthUser {
	u, _ := ctx.Value(userCtxKey).(*AuthUser)
	return u
}

// requireAuth wraps a REST handler, requiring a valid salem-realm bearer token.
// The token rides in the Authorization: Bearer header (the WS /events handler
// reads it from ?token= instead, since browsers can't set WS headers).
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res := s.auth.Verify(bearerToken(r))
		if !res.Valid {
			writeAuthError(w, res.Reason)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userCtxKey, res.User)))
	}
}

// Operator gate — the umbilical debug/control surface authorizes on the
// llm-memory "plugins/administer" admin-permission, NOT salem-realm membership
// (every player is salem-realm) and NOT an in-world admin actor (the operators
// — work/home — have no salem actor row). The capability is identity-level and
// out-of-world, which is the whole point.
const (
	permResourcePlugins  = "plugins"
	permActionAdminister = "administer"
	permWildcard         = "*"
)

// actionRank mirrors llm-memory's admin-permissions hierarchy (read < write <
// delete): a higher grant satisfies a lower required action. Non-hierarchical
// actions (e.g. "administer") aren't ranked and match only exactly or via "*".
var actionRank = map[string]int{"read": 1, "write": 2, "delete": 3}

// hasPermission reports whether the principal holds (resource, action),
// mirroring llm-memory's admin-permissions.hasPermission semantics: a "*"
// action on the resource (or on the "*" resource) grants anything; a ranked
// grant >= the required rank grants it; otherwise an exact action match.
// nil user / nil map → false (fail closed).
func hasPermission(u *AuthUser, resource, action string) bool {
	if u == nil {
		return false
	}
	required := actionRank[action]
	match := func(actions []string) bool {
		for _, a := range actions {
			if a == permWildcard {
				return true
			}
			if required != 0 && actionRank[a] >= required {
				return true
			}
			if a == action {
				return true
			}
		}
		return false
	}
	// The resource's own grants, plus any "*"-resource (cross-resource) grants —
	// the latter covers the */* superadmin (a "*" action under the "*" resource).
	return match(u.Permissions[resource]) || match(u.Permissions[permWildcard])
}

// requireOperator wraps a handler in requireAuth (valid salem-realm token) PLUS
// the plugins/administer capability check. Everything in the umbilical route
// group is wrapped in this. Fails closed to 403 for an authenticated principal
// lacking the capability.
func (s *Server) requireOperator(next http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		user := userFromContext(r.Context())
		if !hasPermission(user, permResourcePlugins, permActionAdminister) {
			writeError(w, http.StatusForbidden, "operator permission required (plugins/administer)")
			return
		}
		next(w, r)
	})
}

// bearerToken pulls the token from an Authorization: Bearer header. Tolerant of
// surrounding whitespace and a case-insensitive scheme ("Bearer"/"bearer"); a
// header with no token after the scheme yields "" (→ the missing-token path).
func bearerToken(r *http.Request) string {
	typ, tok, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(typ, "Bearer") {
		return ""
	}
	return strings.TrimSpace(tok)
}

// writeAuthError maps a VerifyResult.Reason to an HTTP error response.
func writeAuthError(w http.ResponseWriter, reason string) {
	status := http.StatusUnauthorized
	msg := "invalid or expired session token"
	switch reason {
	case "missing":
		msg = "missing session token"
	case "realm":
		status, msg = http.StatusForbidden, "access denied: not a member of the salem realm"
	case "service":
		status, msg = http.StatusServiceUnavailable, "auth service unavailable"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
