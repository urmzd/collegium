package authz_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/urmzd/collegium/pkg/authz"
)

// perms stands in for the platform's list. The tests in pkg/auth run the
// real one.
var perms = []string{
	"agents:read", "agents:write", "runs:read", "runs:write",
	"schedules:read", "schedules:write", "billing:read", "billing:write",
}

const example = `
# The example from docs/authorization.md, on a smaller vocabulary.
role viewer   { allow read on * }
role operator extends viewer {
  allow write on runs, schedules
}
role admin    { allow * on * }
role auditor  extends viewer { deny read on billing }

grant viewer   to anyone
grant operator to group "eng-oncall"
grant admin    to group "platform-admins"
grant auditor  to claim department = "finance"
`

func load(t *testing.T, src string) *authz.Policy {
	t.Helper()
	p, err := authz.Load("test.authz", src, perms)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func who(mut func(*authz.Identity)) authz.Identity {
	id := authz.Identity{Subject: "u1", Tenant: "acme"}
	if mut != nil {
		mut(&id)
	}
	return id
}

func TestParseErrorsCarryPositionAndExpectation(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{"unknown statement", "allow read on *", `1:1: expected "role" or "grant", found "allow"`},
		{"missing brace", "role a allow", `1:8: expected "extends" or "{"`},
		{"missing on", "role a {\n  allow read runs }", `2:14: expected "," or "on", found "runs"`},
		{"unclosed role", "role a { allow read on *", `1:25: expected "allow", "deny" or "}", found end of policy`},
		{"reserved role name", "role grant {}", `1:6: expected a role name, found the reserved word "grant"`},
		{"grant without to", "grant a group \"x\"", `1:9: expected "," or "to", found "group"`},
		{"bad subject", "grant a to everyone", `1:12: expected "group", "subject", "email", "claim" or "anyone"`},
		{"email without domain", "grant a to email \"x.com\"", `1:18: expected "domain"`},
		{"group needs a string", "grant a to group admins", `1:18: expected a quoted string, found "admins"`},
		{"claim needs equals", "grant a to claim dept \"x\"", `1:23: expected "="`},
		{"empty value", `grant a to group ""`, `1:18: expected a value, found an empty string`},
		{"address as domain", `grant a to email domain "a@b.com"`, `expected a domain`},
		{"unclosed string", "grant a to group \"x\n", `1:18: string is not closed`},
		{"bad escape", `grant a to group "\n"`, `1:19: unknown escape`},
		{"stray character", "role a { allow read on runs; }", `1:28: unexpected character ';'`},
		{"invalid utf8", "role \xff", `1:6: invalid UTF-8`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := authz.Parse("p", tc.src)
			var perr *authz.Error
			if !errors.As(err, &perr) {
				t.Fatalf("got %v, want an *authz.Error", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestParseReadsTheExample(t *testing.T) {
	f, err := authz.Parse("ex", example)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Roles) != 4 || len(f.Grants) != 4 {
		t.Fatalf("roles %d, grants %d", len(f.Roles), len(f.Grants))
	}
	if got := f.Grants[3].String(); got != `grant auditor to claim department = "finance"` {
		t.Fatalf("grant round trip: %s", got)
	}
	if got := f.Roles[1].Rules[0].String(); got != "allow write on runs, schedules" {
		t.Fatalf("rule round trip: %s", got)
	}
}

func TestParseAcceptsKeywordAndQuotedClaimNames(t *testing.T) {
	f, err := authz.Parse("", `role r {} grant r to claim email = "a@b.c" grant r to claim "https://x/roles" = "y"`)
	if err != nil {
		t.Fatal(err)
	}
	if f.Grants[0].Subject.Claim != "email" || f.Grants[1].Subject.Claim != "https://x/roles" {
		t.Fatalf("got %+v", f.Grants)
	}
}

func TestCompileErrorsAtLoadTime(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{"empty policy", "# nothing\n", "policy is empty"},
		{"grants only", `grant a to anyone`, "policy is empty"},
		{"duplicate role", "role a {}\nrole a {}", `2:6: role "a" is already declared at 1:6`},
		{"unknown extends", "role a extends b {}", `1:16: role "a" extends unknown role "b"`},
		{"unknown grant role", "role a {}\ngrant a, b to anyone", `2:10: grant names unknown role "b"`},
		{"self cycle", "role a extends a {}", "inheritance cycle: a extends a"},
		{"long cycle", "role a extends b {}\nrole b extends c {}\nrole c extends a {}", "inheritance cycle: a extends b extends c extends a"},
		{"unknown action", "role a { allow delete on runs }", `1:16: unknown action "delete", expected one of: read, write`},
		{"unknown resource", "role a { allow read on rockets }", `1:24: unknown resource "rockets"`},
		{"too deep", deepChain(authz.MaxDepth + 1), "inheritance is deeper than 32 roles"},
		{"too many roles", strings.Repeat("role r {}\n", authz.MaxRoles+1), "too many roles"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := authz.Load("", tc.src, perms)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func deepChain(n int) string {
	var b strings.Builder
	b.WriteString("role r0 { allow read on runs }\n")
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, "role r%d extends r%d {}\n", i, i-1)
	}
	return b.String()
}

func TestDeepestAllowedChainCompiles(t *testing.T) {
	p := load(t, deepChain(authz.MaxDepth))
	if !p.Allows([]string{fmt.Sprintf("r%d", authz.MaxDepth-1)}, "runs:read") {
		t.Fatal("a rule did not reach the end of the chain")
	}
}

func TestDefaultDeny(t *testing.T) {
	p := load(t, "role reader { allow read on runs }\nrole empty {}")
	for _, tc := range []struct {
		name  string
		roles []string
		perm  string
		want  bool
	}{
		{"no roles", nil, "runs:read", false},
		{"role with no rules", []string{"empty"}, "runs:read", false},
		{"permission no rule names", []string{"reader"}, "runs:write", false},
		{"undeclared role", []string{"root"}, "runs:read", false},
		{"unknown permission", []string{"reader"}, "rockets:launch", false},
		{"allowed", []string{"reader"}, "runs:read", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.Allows(tc.roles, tc.perm); got != tc.want {
				t.Fatalf("Allows(%v, %s) = %v, want %v", tc.roles, tc.perm, got, tc.want)
			}
		})
	}
}

func TestDenyOverridesAllow(t *testing.T) {
	p := load(t, `
role all       { allow * on * }
role nobill    { deny * on billing }
role inherits  extends nobill { allow read on billing }   # inherited-over: the child's allow loses
role child     extends all { deny write on agents }        # the child's deny beats the parent's allow
role grandkid  extends child { allow * on * }             # an inherited deny still wins
role same      { allow read on runs  deny read on runs }  # within one role
`)
	for _, tc := range []struct {
		name  string
		roles []string
		perm  string
		want  bool
	}{
		{"deny in another held role", []string{"all", "nobill"}, "billing:read", false},
		{"deny in parent beats child allow", []string{"inherits"}, "billing:read", false},
		{"deny in child beats inherited allow", []string{"child"}, "agents:write", false},
		{"inherited deny beats own allow", []string{"grandkid"}, "agents:write", false},
		{"deny in the same role", []string{"same"}, "runs:read", false},
		{"unrelated permission still allowed", []string{"all", "nobill"}, "runs:write", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.Allows(tc.roles, tc.perm); got != tc.want {
				t.Fatalf("Allows(%v, %s) = %v, want %v", tc.roles, tc.perm, got, tc.want)
			}
		})
	}
}

func TestEffectiveSetIsOwnRulesPlusExtended(t *testing.T) {
	p := load(t, `
role a { allow read on runs }
role b { allow read on agents }
role c extends a, b { allow write on runs }
role d extends c {}
role diamond extends c, a {}
`)
	for _, tc := range []struct {
		role string
		want []string
	}{
		{"a", []string{"runs:read"}},
		{"c", []string{"agents:read", "runs:read", "runs:write"}},
		{"d", []string{"agents:read", "runs:read", "runs:write"}},
		{"diamond", []string{"agents:read", "runs:read", "runs:write"}},
	} {
		t.Run(tc.role, func(t *testing.T) {
			got := p.Permissions([]string{tc.role})
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRolesAreEveryGrantTheClaimsMatch(t *testing.T) {
	p := load(t, example+`
grant admin to subject "00u-root"
grant operator to email domain "ops.example.com"
`)
	for _, tc := range []struct {
		name string
		id   authz.Identity
		want []string
	}{
		{"anyone only", who(nil), []string{"viewer"}},
		{"group", who(func(i *authz.Identity) { i.Groups = []string{"eng-oncall", "unrelated"} }), []string{"viewer", "operator"}},
		{"several grants", who(func(i *authz.Identity) {
			i.Groups = []string{"platform-admins"}
			i.Claims = map[string]any{"department": "finance"}
		}), []string{"viewer", "admin", "auditor"}},
		{"subject", who(func(i *authz.Identity) { i.Subject = "00u-root" }), []string{"viewer", "admin"}},
		{"email domain, any case", who(func(i *authz.Identity) { i.Email = "ada@OPS.example.com" }), []string{"viewer", "operator"}},
		{"email subdomain is not the domain", who(func(i *authz.Identity) { i.Email = "ada@x.ops.example.com" }), []string{"viewer"}},
		{"email with two at signs", who(func(i *authz.Identity) { i.Email = "a@evil.test@ops.example.com" }), []string{"viewer"}},
		{"group is case sensitive", who(func(i *authz.Identity) { i.Groups = []string{"ENG-ONCALL"} }), []string{"viewer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.Roles(tc.id); !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAnyoneNeedsSubjectAndTenant(t *testing.T) {
	p := load(t, example)
	for _, tc := range []struct {
		name string
		id   authz.Identity
		want bool
	}{
		{"zero identity", authz.Identity{}, false},
		{"no tenant", authz.Identity{Subject: "u"}, false},
		{"no subject", authz.Identity{Tenant: "acme"}, false},
		{"zero identity with matching group", authz.Identity{Groups: []string{"platform-admins"}}, false},
		{"authenticated", who(nil), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(p.Roles(tc.id)) > 0; got != tc.want {
				t.Fatalf("matched = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUnknownOrNonStringClaimNeverMatches(t *testing.T) {
	p := load(t, "role r { allow read on runs }\ngrant r to claim department = \"finance\"\ngrant r to claim level = \"5\"")
	for _, tc := range []struct {
		name   string
		claims map[string]any
	}{
		{"no claims", nil},
		{"unknown claim", map[string]any{"dept": "finance"}},
		{"list containing the value", map[string]any{"department": []any{"finance"}}},
		{"number", map[string]any{"level": 5.0}},
		{"bool", map[string]any{"department": true}},
		{"object", map[string]any{"department": map[string]any{"name": "finance"}}},
		{"different value", map[string]any{"department": "Finance"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.Roles(who(func(i *authz.Identity) { i.Claims = tc.claims })); len(got) != 0 {
				t.Fatalf("matched %v", got)
			}
		})
	}
}

func TestExplainNamesTheGrantAndTheRule(t *testing.T) {
	p := load(t, example)
	finance := who(func(i *authz.Identity) {
		i.Groups = []string{"platform-admins"}
		i.Claims = map[string]any{"department": "finance"}
	})

	e := p.Explain(finance, "billing:read")
	if e.Allowed || e.Outcome != authz.Denied || e.Role != "auditor" {
		t.Fatalf("got %+v", e)
	}
	if e.Rule.String() != "deny read on billing" || e.Grant.String() != `grant auditor to claim department = "finance"` {
		t.Fatalf("rule %s, grant %s", e.Rule, e.Grant)
	}
	if !strings.Contains(e.String(), "test.authz:8:32") {
		t.Fatalf("explanation has no position:\n%s", e)
	}

	e = p.Explain(who(func(i *authz.Identity) { i.Groups = []string{"eng-oncall"} }), "runs:read")
	if !e.Allowed || e.Role != "viewer" || e.Grant.Subject.Kind != authz.SubjectAnyone {
		t.Fatalf("got %+v", e)
	}

	e = p.Explain(who(func(i *authz.Identity) { i.Groups = []string{"eng-oncall"} }), "runs:write")
	if !e.Allowed || e.Role != "operator" || !slices.Equal(e.Path, []string{"operator"}) {
		t.Fatalf("got %+v", e)
	}

	e = p.Explain(authz.Identity{}, "runs:read")
	if e.Allowed || e.Outcome != authz.NoMatch || e.Rule != nil || len(e.Held) != 0 {
		t.Fatalf("got %+v", e)
	}
	if !strings.Contains(e.String(), "no grant matches") {
		t.Fatal(e.String())
	}
}

func TestExplainRolesFollowsInheritance(t *testing.T) {
	p := load(t, example)
	e := p.ExplainRoles([]string{"auditor", "ghost"}, "agents:read")
	if !e.Allowed || !slices.Equal(e.Path, []string{"auditor", "viewer"}) || e.Grant != nil {
		t.Fatalf("got %+v", e)
	}
	if !slices.Equal(e.Unknown, []string{"ghost"}) {
		t.Fatalf("unknown = %v", e.Unknown)
	}
	if e := p.ExplainRoles([]string{"viewer"}, "runs:write"); e.Outcome != authz.NoMatch {
		t.Fatalf("got %+v", e)
	}
}

// What Held keeps of a sign-in explains the same as the claims did, and
// matches the roles Roles lists.
func TestExplainHeldNamesTheGrantWithoutTheClaims(t *testing.T) {
	p := load(t, example)
	finance := who(func(i *authz.Identity) {
		i.Groups = []string{"platform-admins"}
		i.Claims = map[string]any{"department": "finance"}
	})
	held := p.Held(finance)
	var roles []string
	for _, h := range held {
		roles = append(roles, h.Role)
	}
	if !slices.Equal(roles, p.Roles(finance)) {
		t.Fatalf("held %v, roles %v", roles, p.Roles(finance))
	}
	for _, perm := range perms {
		want, got := p.Explain(finance, perm), p.ExplainHeld(held, perm)
		if got.String() != want.String() || got.Grant != want.Grant {
			t.Errorf("%s:\nheld:   %s\nclaims: %s", perm, got, want)
		}
	}
	if e := p.ExplainHeld(nil, "runs:read"); e.Allowed || e.Outcome != authz.NoMatch {
		t.Fatalf("got %+v", e)
	}
}

// Explain and Allows must never disagree, or the explanation lies.
func TestExplainAgreesWithAllows(t *testing.T) {
	p := load(t, example)
	sets := [][]string{nil, {"viewer"}, {"operator"}, {"admin"}, {"auditor"}, {"admin", "auditor"}, {"ghost", "operator"}}
	for _, roles := range sets {
		for _, perm := range perms {
			if got, want := p.ExplainRoles(roles, perm).Allowed, p.Allows(roles, perm); got != want {
				t.Errorf("%v %s: explain %v, allows %v", roles, perm, got, want)
			}
		}
	}
}

func TestWithGrantsChecksRoles(t *testing.T) {
	p := load(t, example)
	if _, err := p.WithGrants(&authz.Grant{Roles: []authz.Name{{Text: "root"}}, Subject: authz.Subject{Kind: authz.SubjectGroup, Value: "g"}}); err == nil {
		t.Fatal("a grant of an unknown role was accepted")
	}
	q, err := p.WithGrants(&authz.Grant{Roles: []authz.Name{{Text: "admin"}}, Subject: authz.Subject{Kind: authz.SubjectGroup, Value: "g"}})
	if err != nil {
		t.Fatal(err)
	}
	g := who(func(i *authz.Identity) { i.Groups = []string{"g"} })
	if !slices.Contains(q.Roles(g), "admin") || slices.Contains(p.Roles(g), "admin") {
		t.Fatal("WithGrants changed the original or did not add the grant")
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		example, "", "#", "role", "role a {", `grant a to claim "x" = "y"`, "role a extends b, c { deny * on * }",
		"\"\\", "role a { allow read, write on runs, agents }", "grant a to email domain \"x\"",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		file, err := authz.Parse("fuzz", src)
		if err != nil {
			var perr *authz.Error
			if !errors.As(err, &perr) || perr.Pos.Line < 1 || perr.Pos.Col < 1 {
				t.Fatalf("error without a position: %v", err)
			}
			return
		}
		p, err := authz.Compile(file, perms)
		if err != nil {
			return
		}
		// A policy that compiles must answer without panicking.
		id := authz.Identity{Subject: "s", Tenant: "t", Groups: []string{"g"}, Claims: map[string]any{"c": "v"}}
		roles := p.Roles(id)
		_ = p.Permissions(roles)
		_ = p.Explain(id, "runs:read").String()
	})
}
