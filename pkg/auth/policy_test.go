package auth_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/urmzd/collegium/pkg/auth"
	"github.com/urmzd/collegium/pkg/auth/authtest"
)

// legacyRolePermissions is the hardcoded table the policy language replaced,
// copied as it stood. The built-in policy must reproduce it exactly, so that
// a deployment with no policy file behaves as it did.
var legacyRolePermissions = map[auth.Role][]auth.Permission{
	auth.Viewer: {auth.AgentsRead, auth.RunsRead, auth.SchedulesRead, auth.AlertsRead, auth.KnowledgeRead, auth.EvalsRead, auth.BillingRead, auth.ArtifactsRead, auth.ClientsRead},
	auth.Operator: {
		auth.AgentsRead, auth.RunsRead, auth.RunsWrite, auth.SchedulesRead, auth.SchedulesWrite,
		auth.AlertsRead, auth.AlertsWrite, auth.KnowledgeRead, auth.EvalsRead, auth.EvalsWrite,
		auth.BillingRead, auth.ArtifactsRead, auth.ArtifactsWrite, auth.ClientsRead,
	},
	auth.Admin: auth.Permissions(),
}

func sorted(ps []auth.Permission) []auth.Permission {
	out := slices.Clone(ps)
	slices.Sort(out)
	return out
}

func TestDefaultPolicyReproducesTheLegacyRoles(t *testing.T) {
	pol := auth.DefaultPolicy()
	for role, want := range legacyRolePermissions {
		if got := pol.Permissions([]auth.Role{role}); !slices.Equal(sorted(got), sorted(want)) {
			t.Errorf("%s: policy grants %v, legacy table %v", role, got, want)
		}
		who := auth.Principal{Subject: "u", Tenant: "acme", Roles: []auth.Role{role}}
		if got := who.Granted(); !slices.Equal(sorted(got), sorted(want)) {
			t.Errorf("%s: Granted %v, legacy table %v", role, got, want)
		}
	}
	if got := pol.Compiled().RoleNames(); !slices.Equal(got, []string{"viewer", "operator", "admin"}) {
		t.Errorf("roles %v: the built-in policy must declare exactly the three roles", got)
	}
	if len(pol.Compiled().Grants()) != 0 {
		t.Error("the built-in policy grants a role by itself; roles must come from configuration")
	}
}

func TestPolicyFileErrorsStopLoading(t *testing.T) {
	for name, src := range map[string]string{
		"syntax":           "role viewer { allow read on }",
		"unknown resource": "role viewer { allow read on rockets }",
		"empty":            "",
	} {
		if _, err := auth.ParsePolicy("p.authz", src); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := auth.LoadPolicyFile(t.TempDir() + "/missing.authz"); err == nil {
		t.Error("a missing file was accepted")
	}
}

func TestGroupRolesBecomeGrants(t *testing.T) {
	pol, err := auth.DefaultPolicy().WithGroupRoles(map[string]auth.Role{"ops": auth.Operator})
	if err != nil {
		t.Fatal(err)
	}
	g := pol.Compiled().Grants()
	if len(g) != 1 || g[0].String() != `grant operator to group "ops"` {
		t.Fatalf("grants %v", g)
	}
	if _, err := auth.DefaultPolicy().WithGroupRoles(map[string]auth.Role{"ops": "root"}); err == nil {
		t.Fatal("a group mapped to an undeclared role was accepted")
	}
	iss := authtest.New(t)
	_, err = auth.NewVerifier(context.Background(), auth.OIDCConfig{
		Issuer: iss.URL, ClientID: client, Audience: audience, Tenant: "acme",
		GroupRoles: map[string]auth.Role{"ops": "root"},
	})
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("verifier started with a group mapped to an undeclared role: %v", err)
	}
}

const custom = `
role viewer   { allow read on * }
role operator extends viewer { allow write on runs }
role auditor  extends viewer { deny read on billing }
grant viewer   to anyone
grant operator to group "eng-oncall"
grant auditor  to claim department = "finance"
grant operator to email domain "acme.test"
`

func customVerifier(t *testing.T, iss *authtest.Issuer) *auth.Verifier {
	t.Helper()
	pol, err := auth.ParsePolicy("custom.authz", custom)
	if err != nil {
		t.Fatal(err)
	}
	return verifier(t, iss, func(c *auth.OIDCConfig) {
		c.Policy = pol
		c.GroupRoles = map[string]auth.Role{"collegium-operators": auth.Operator}
	})
}

func TestVerifierAssignsRolesThroughThePolicy(t *testing.T) {
	iss := authtest.New(t)
	v := customVerifier(t, iss)
	for _, tc := range []struct {
		name   string
		claims map[string]any
		roles  []auth.Role
	}{
		{"anyone", map[string]any{}, []auth.Role{auth.Viewer}},
		{"group in the policy", map[string]any{"groups": []string{"eng-oncall"}}, []auth.Role{auth.Viewer, auth.Operator}},
		{"group from GroupRoles", map[string]any{"groups": []string{"collegium-operators"}}, []auth.Role{auth.Viewer, auth.Operator}},
		{"claim", map[string]any{"department": "finance"}, []auth.Role{auth.Viewer, "auditor"}},
		{"verified email", map[string]any{"email": "ada@acme.test", "email_verified": true}, []auth.Role{auth.Viewer, auth.Operator}},
		{"unverified email", map[string]any{"email": "ada@acme.test"}, []auth.Role{auth.Viewer}},
		{"email_verified as a string", map[string]any{"email": "ada@acme.test", "email_verified": "true"}, []auth.Role{auth.Viewer}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := map[string]any{"nonce": "n1"}
			for k, val := range tc.claims {
				claims[k] = val
			}
			p, err := v.IDToken(context.Background(), iss.Sign(t, "00u1", client, claims), "n1")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(p.Roles, tc.roles) {
				t.Fatalf("roles %v, want %v", p.Roles, tc.roles)
			}
		})
	}
}

func TestPrincipalIsDecidedByItsVerifiersPolicy(t *testing.T) {
	iss := authtest.New(t)
	v := customVerifier(t, iss)
	p, err := v.IDToken(context.Background(), iss.Sign(t, "00u1", client, map[string]any{"nonce": "n1", "department": "finance"}), "n1")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Can(auth.RunsRead) || p.Can(auth.BillingRead) {
		t.Fatalf("granted %v; the auditor's deny must remove billing:read", p.Granted())
	}
	d := v.Policy().Explain(p, auth.BillingRead)
	if d.Allowed || d.Policy.Role != "auditor" || !strings.Contains(d.String(), "deny read on billing") {
		t.Fatalf("explain:\n%s", d)
	}
	// The same roles under the built-in policy would be decided differently,
	// which is why a principal carries the policy that assigned them.
	if !p.WithPolicy(nil).Can(auth.BillingRead) {
		t.Fatal("WithPolicy(nil) should fall back to the built-in policy")
	}
}

// "anyone" is a grant for people. A machine is not given roles, so a policy
// that grants everyone admin still leaves a machine with only its scopes.
func TestPolicyDoesNotWidenAMachine(t *testing.T) {
	iss := authtest.New(t)
	pol, err := auth.ParsePolicy("open.authz", "role admin { allow * on * }\ngrant admin to anyone")
	if err != nil {
		t.Fatal(err)
	}
	v := verifier(t, iss, func(c *auth.OIDCConfig) { c.Policy = pol; c.GroupRoles = nil })
	p, err := v.Bearer(context.Background(), iss.Sign(t, "svc", audience, map[string]any{"scp": []string{"collegium:runs:read"}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Roles) != 0 || !slices.Equal(p.Granted(), []auth.Permission{auth.RunsRead}) {
		t.Fatalf("machine roles %v, granted %v", p.Roles, p.Granted())
	}
	d := pol.Explain(p, auth.RunsRead)
	if !d.Allowed || !d.Scope {
		t.Fatalf("explain: %+v", d)
	}
}

func TestExplainTheZeroPrincipal(t *testing.T) {
	d := auth.DefaultPolicy().Explain(auth.Principal{Roles: []auth.Role{auth.Admin}}, auth.RunsRead)
	if d.Allowed {
		t.Fatal("the zero principal was allowed")
	}
}

func TestIdentityFromClaims(t *testing.T) {
	cfg := auth.OIDCConfig{TenantClaim: "org", GroupsClaim: "roles"}
	id, err := cfg.Identity("u", map[string]any{"org": "globex", "roles": []any{"a", 3, "b"}, "email": "x@y.z", "email_verified": true})
	if err != nil {
		t.Fatal(err)
	}
	if id.Tenant != "globex" || !slices.Equal(id.Groups, []string{"a", "b"}) || id.Email != "x@y.z" {
		t.Fatalf("got %+v", id)
	}
	if _, err := cfg.Identity("u", map[string]any{}); err == nil {
		t.Fatal("an identity with no tenant claim was accepted")
	}
}
