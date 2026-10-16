package auth

import (
	_ "embed"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/urmzd/collegium/pkg/authz"
)

// auth depends on authz and not the other way round. authz is a language and
// knows nothing about this platform; this package owns the permissions, so it
// hands their list to the compiler and the list is written down once.

//go:embed default.authz
var defaultSource string

var defaultPolicy = &Policy{c: authz.MustCompile("default.authz", defaultSource, permissionNames())}

func permissionNames() []string {
	out := make([]string, 0, len(Permissions()))
	for _, p := range Permissions() {
		out = append(out, string(p))
	}
	return out
}

// Policy decides what a person's roles allow, and which roles their verified
// claims earn. See docs/authorization.md for the language.
type Policy struct {
	c *authz.Policy
}

// DefaultPolicy is the built-in policy: viewer, operator and admin, and no
// grants. It is what every Principal is decided by unless given another.
func DefaultPolicy() *Policy { return defaultPolicy }

// ParsePolicy reads and checks a policy against this platform's permissions.
// name is used in error messages.
func ParsePolicy(name, src string) (*Policy, error) {
	c, err := authz.Load(name, src, permissionNames())
	if err != nil {
		return nil, fmt.Errorf("auth: policy: %w", err)
	}
	return &Policy{c: c}, nil
}

// LoadPolicyFile reads a policy from a file. Any problem is an error, so a
// server given a bad policy does not start.
func LoadPolicyFile(path string) (*Policy, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("auth: policy: %w", err)
	}
	return ParsePolicy(path, string(src))
}

// WithGroupRoles returns the policy with a grant for each group, which is
// how a GroupRoles table from before the policy language keeps working. A
// role the policy does not declare is an error.
func (p *Policy) WithGroupRoles(groups map[string]Role) (*Policy, error) {
	if len(groups) == 0 {
		return p, nil
	}
	var grants []*authz.Grant
	// Sorted, so the grants and any explanation are the same every start.
	for _, g := range slices.Sorted(maps.Keys(groups)) {
		role := groups[g]
		if g == "" {
			return nil, fmt.Errorf("auth: group roles: an empty group name would match nothing; remove it")
		}
		if !p.c.HasRole(string(role)) {
			return nil, fmt.Errorf("auth: group %q maps to role %q, which the policy does not declare (it has %s)", g, role, strings.Join(p.c.RoleNames(), ", "))
		}
		grants = append(grants, &authz.Grant{
			Roles:   []authz.Name{{Text: string(role)}},
			Subject: authz.Subject{Kind: authz.SubjectGroup, Value: g},
		})
	}
	c, err := p.c.WithGrants(grants...)
	if err != nil {
		return nil, fmt.Errorf("auth: group roles: %w", err)
	}
	return &Policy{c: c}, nil
}

// Compiled is the underlying policy, for explaining a decision from claims.
func (p *Policy) Compiled() *authz.Policy { return p.c }

// HasRole reports whether the policy declares a role.
func (p *Policy) HasRole(r Role) bool { return p.c.HasRole(string(r)) }

// Roles lists the roles an identity's verified claims earn.
func (p *Policy) Roles(id authz.Identity) []Role {
	names := p.c.Roles(id)
	out := make([]Role, len(names))
	for i, n := range names {
		out[i] = Role(n)
	}
	return out
}

// Permissions lists what a set of roles holds.
func (p *Policy) Permissions(roles []Role) []Permission {
	var out []Permission
	for _, n := range p.c.Permissions(roleNames(roles)) {
		out = append(out, Permission(n))
	}
	return out
}

func (p *Policy) allows(roles []Role, perm Permission) bool {
	return p.c.Allows(roleNames(roles), string(perm))
}

func roleNames(roles []Role) []string {
	out := make([]string, len(roles))
	for i, r := range roles {
		out[i] = string(r)
	}
	return out
}

// Decision is the answer to Can, with the reason.
type Decision struct {
	Permission Permission
	Allowed    bool
	// Scope is true when a machine's token scope decided. A machine is
	// never decided by the policy.
	Scope bool
	// Policy is how the roles decided, for a person or a caller with no
	// scope for the permission.
	Policy authz.Explanation
}

func (d Decision) String() string {
	if d.Scope {
		return fmt.Sprintf("%s: allowed\n  the token carries the scope %s%s\n", d.Permission, ScopePrefix, d.Permission)
	}
	return d.Policy.String()
}

// Explain answers Can and says why. A Principal keeps its roles and not the
// claims that earned them. One signed in through a Verifier also keeps the
// grant behind each role, and the explanation names it; otherwise it names
// the rule and not the grant, and Compiled().Explain over the claims does.
func (p *Policy) Explain(who Principal, perm Permission) Decision {
	d := Decision{Permission: perm}
	if who.Subject == "" || who.Tenant == "" {
		d.Policy = p.c.ExplainRoles(nil, string(perm))
		return d
	}
	if slices.Contains(who.Scopes, perm) {
		d.Allowed, d.Scope = true, true
		return d
	}
	if who.policy == p && len(who.held) > 0 {
		d.Policy = p.c.ExplainHeld(who.held, string(perm))
	} else {
		d.Policy = p.c.ExplainRoles(roleNames(who.Roles), string(perm))
	}
	d.Allowed = d.Policy.Allowed
	return d
}
