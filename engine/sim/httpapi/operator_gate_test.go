package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHasPermission(t *testing.T) {
	cases := []struct {
		name             string
		perms            map[string][]string
		resource, action string
		want             bool
	}{
		{"exact administer", map[string][]string{"plugins": {"administer"}}, "plugins", "administer", true},
		{"no perms", map[string][]string{}, "plugins", "administer", false},
		{"global superadmin", map[string][]string{"*": {"*"}}, "plugins", "administer", true},
		{"resource wildcard action", map[string][]string{"plugins": {"*"}}, "plugins", "administer", true},
		{"cross-resource administer", map[string][]string{"*": {"administer"}}, "plugins", "administer", true},
		{"read does not grant administer", map[string][]string{"plugins": {"read"}}, "plugins", "administer", false},
		{"hierarchy: write grants read", map[string][]string{"docs": {"write"}}, "docs", "read", true},
		{"hierarchy: read denies write", map[string][]string{"docs": {"read"}}, "docs", "write", false},
		{"wrong resource", map[string][]string{"other": {"administer"}}, "plugins", "administer", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasPermission(&AuthUser{Permissions: tc.perms}, tc.resource, tc.action); got != tc.want {
				t.Errorf("hasPermission(%v, %q, %q) = %v, want %v", tc.perms, tc.resource, tc.action, got, tc.want)
			}
		})
	}
	if hasPermission(nil, "plugins", "administer") {
		t.Error("nil user must fail closed")
	}
}

// permAuth is a fake Authenticator: any non-empty token resolves to a salem-realm
// principal carrying the given permission map.
type permAuth struct{ perms map[string][]string }

func (p permAuth) Verify(token string) VerifyResult {
	if token == "" {
		return VerifyResult{Reason: "missing"}
	}
	return VerifyResult{Valid: true, User: &AuthUser{Username: "op", Realms: []string{"salem"}, Permissions: p.perms}}
}

func TestRequireOperator(t *testing.T) {
	dummy := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	serve := func(auth Authenticator, token string) int {
		srv := NewServer(seededWorld(t), auth)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		srv.requireOperator(dummy)(rec, req)
		return rec.Code
	}

	if c := serve(permAuth{map[string][]string{"plugins": {"administer"}}}, "tok"); c != http.StatusOK {
		t.Errorf("operator with plugins/administer = %d, want 200", c)
	}
	if c := serve(permAuth{map[string][]string{"*": {"*"}}}, "tok"); c != http.StatusOK {
		t.Errorf("superadmin = %d, want 200", c)
	}
	if c := serve(permAuth{nil}, "tok"); c != http.StatusForbidden {
		t.Errorf("authed without permission = %d, want 403", c)
	}
	if c := serve(permAuth{nil}, ""); c != http.StatusUnauthorized {
		t.Errorf("missing token = %d, want 401", c)
	}
}
