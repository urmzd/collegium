package auth_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/urmzd/collegium/pkg/auth"
	"github.com/urmzd/collegium/pkg/auth/authtest"
)

const (
	client   = "web-client"
	audience = "api://collegium"
)

func verifier(t *testing.T, iss *authtest.Issuer, mut func(*auth.OIDCConfig)) *auth.Verifier {
	t.Helper()
	cfg := auth.OIDCConfig{
		Issuer:   iss.URL,
		ClientID: client,
		Audience: audience,
		Tenant:   "acme",
		GroupRoles: map[string]auth.Role{
			"collegium-admins":    auth.Admin,
			"collegium-operators": auth.Operator,
		},
	}
	if mut != nil {
		mut(&cfg)
	}
	v, err := auth.NewVerifier(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestZeroPrincipalCanDoNothing(t *testing.T) {
	for _, perm := range auth.Permissions() {
		if (auth.Principal{}).Can(perm) {
			t.Errorf("zero principal can %s", perm)
		}
	}
	noTenant := auth.Principal{Subject: "u", Roles: []auth.Role{auth.Admin}}
	if noTenant.Can(auth.RunsRead) {
		t.Error("a principal with no tenant was granted a permission")
	}
}

func TestRoles(t *testing.T) {
	p := func(r auth.Role) auth.Principal {
		return auth.Principal{Subject: "u", Tenant: "acme", Roles: []auth.Role{r}}
	}
	for _, tc := range []struct {
		role auth.Role
		perm auth.Permission
		want bool
	}{
		{auth.Viewer, auth.RunsRead, true},
		{auth.Viewer, auth.RunsWrite, false},
		{auth.Operator, auth.RunsWrite, true},
		{auth.Operator, auth.SchedulesWrite, true},
		{auth.Operator, auth.AgentsWrite, false},
		{auth.Admin, auth.AgentsWrite, true},
		{"root", auth.RunsRead, false},
	} {
		if got := p(tc.role).Can(tc.perm); got != tc.want {
			t.Errorf("%s can %s = %v, want %v", tc.role, tc.perm, got, tc.want)
		}
	}
}

func TestSignInMapsGroupsToRoles(t *testing.T) {
	iss := authtest.New(t)
	v := verifier(t, iss, nil)
	raw := iss.Sign(t, "00u1", client, map[string]any{
		"nonce": "n1", "name": "Ada", "email": "ada@acme.test",
		"groups": []string{"collegium-operators", "everyone"},
	})
	p, err := v.IDToken(context.Background(), raw, "n1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != auth.User || p.Tenant != "acme" || p.Email != "ada@acme.test" {
		t.Fatalf("got %+v", p)
	}
	if !slices.Equal(p.Roles, []auth.Role{auth.Operator}) {
		t.Fatalf("roles = %v; an unmapped group must grant nothing", p.Roles)
	}
}

func TestSignInRejections(t *testing.T) {
	iss := authtest.New(t)
	v := verifier(t, iss, nil)
	for name, raw := range map[string]string{
		"wrong nonce":    iss.Sign(t, "u", client, map[string]any{"nonce": "other"}),
		"no nonce":       iss.Sign(t, "u", client, nil),
		"wrong audience": iss.Sign(t, "u", "someone-else", map[string]any{"nonce": "n1"}),
		"expired":        iss.Sign(t, "u", client, map[string]any{"nonce": "n1", "exp": time.Now().Add(-time.Hour).Unix()}),
		"wrong issuer":   iss.Sign(t, "u", client, map[string]any{"nonce": "n1", "iss": "https://evil.test"}),
		"unknown key":    iss.SignWith(t, "u", client),
		"not a token":    "nope",
	} {
		if _, err := v.IDToken(context.Background(), raw, "n1"); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("%s: got %v, want ErrUnauthenticated", name, err)
		}
	}
}

func TestMachineGetsOnlyItsScopes(t *testing.T) {
	iss := authtest.New(t)
	v := verifier(t, iss, nil)
	raw := iss.Sign(t, "0oa-ci", audience, map[string]any{
		"cid": "0oa-ci",
		"scp": []string{"collegium:runs:write", "collegium:everything", "runs:read", "other:agents:write"},
	})
	p, err := v.Bearer(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != auth.Machine || len(p.Roles) != 0 {
		t.Fatalf("got %+v", p)
	}
	if got := p.Granted(); !slices.Equal(got, []auth.Permission{auth.RunsWrite}) {
		t.Fatalf("granted %v, want only runs:write", got)
	}
}

func TestMachineScopeString(t *testing.T) {
	iss := authtest.New(t)
	v := verifier(t, iss, nil)
	raw := iss.Sign(t, "svc", audience, map[string]any{"scope": "collegium:runs:read collegium:agents:read"})
	p, err := v.Bearer(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Can(auth.RunsRead) || !p.Can(auth.AgentsRead) || p.Can(auth.RunsWrite) {
		t.Fatalf("granted %v", p.Granted())
	}
}

// An ID token is issued to the web client, not to the API. Accepting one as a
// bearer token would let a page's token call the API as that person.
func TestBearerRejectsAnIDToken(t *testing.T) {
	iss := authtest.New(t)
	v := verifier(t, iss, nil)
	raw := iss.Sign(t, "00u1", client, map[string]any{"groups": []string{"collegium-admins"}})
	if _, err := v.Bearer(context.Background(), raw); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("got %v, want ErrUnauthenticated", err)
	}
}

func TestBearerForAPerson(t *testing.T) {
	iss := authtest.New(t)
	v := verifier(t, iss, nil)
	raw := iss.Sign(t, "00u1", audience, map[string]any{"groups": []string{"collegium-admins"}})
	p, err := v.Bearer(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != auth.User || !p.Can(auth.AgentsWrite) {
		t.Fatalf("got %+v", p)
	}
}

func TestTenantClaim(t *testing.T) {
	iss := authtest.New(t)
	v := verifier(t, iss, func(c *auth.OIDCConfig) { c.Tenant = ""; c.TenantClaim = "org" })

	p, err := v.Bearer(context.Background(), iss.Sign(t, "svc", audience, map[string]any{"org": "globex"}))
	if err != nil {
		t.Fatal(err)
	}
	if p.Tenant != "globex" {
		t.Fatalf("tenant = %q", p.Tenant)
	}
	if _, err := v.Bearer(context.Background(), iss.Sign(t, "svc", audience, nil)); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("a token with no tenant claim was accepted: %v", err)
	}
}

func TestConfigFailsClosed(t *testing.T) {
	iss := authtest.New(t)
	for name, mut := range map[string]func(*auth.OIDCConfig){
		"no audience":  func(c *auth.OIDCConfig) { c.Audience = "" },
		"no client":    func(c *auth.OIDCConfig) { c.ClientID = "" },
		"no tenant":    func(c *auth.OIDCConfig) { c.Tenant = "" },
		"http issuer":  func(c *auth.OIDCConfig) { c.Issuer = "http://idp.example.com" },
		"empty issuer": func(c *auth.OIDCConfig) { c.Issuer = "" },
	} {
		cfg := auth.OIDCConfig{Issuer: iss.URL, ClientID: client, Audience: audience, Tenant: "acme"}
		mut(&cfg)
		if _, err := auth.NewVerifier(context.Background(), cfg); err == nil {
			t.Errorf("%s: config was accepted", name)
		}
	}
}
