package authz

import (
	"fmt"
	"slices"
	"strings"
)

// Identity is what authentication established about a caller. Every field
// must come from a verified token, never from a request.
type Identity struct {
	// Subject is the provider's stable identifier for the caller.
	Subject string
	// Tenant is the scope the caller is confined to.
	Tenant string
	// Email is matched by "email domain". Leave it empty unless the provider
	// says the address is verified.
	Email string
	// Groups are matched by "group".
	Groups []string
	// Claims are matched by "claim". Only a claim whose value is a string
	// can match.
	Claims map[string]any
}

// authenticated reports whether there is a caller at all. The zero Identity
// matches no grant, not even "anyone".
func (id Identity) authenticated() bool { return id.Subject != "" && id.Tenant != "" }

// Matches reports whether the subject clause is true of the identity.
func (s Subject) Matches(id Identity) bool {
	if !id.authenticated() {
		return false
	}
	switch s.Kind {
	case SubjectAnyone:
		return true
	case SubjectGroup:
		return s.Value != "" && slices.Contains(id.Groups, s.Value)
	case SubjectID:
		return s.Value != "" && id.Subject == s.Value
	case SubjectEmailDomain:
		_, domain, ok := strings.Cut(id.Email, "@")
		// One "@" only: "a@evil.test@example.com" is not in example.com.
		return ok && s.Value != "" && !strings.Contains(domain, "@") && strings.EqualFold(domain, s.Value)
	case SubjectClaim:
		v, ok := id.Claims[s.Claim].(string)
		return ok && s.Value != "" && v == s.Value
	}
	return false
}

// Roles lists every role granted to the identity, in the order the policy
// first grants them. It is the (policy, claims) -> roles half of a decision.
func (p *Policy) Roles(id Identity) []string {
	var out []string
	for _, g := range p.file.Grants {
		if !g.Subject.Matches(id) {
			continue
		}
		for _, r := range g.Roles {
			if !slices.Contains(out, r.Text) {
				out = append(out, r.Text)
			}
		}
	}
	return out
}

// Allows reports whether the roles together hold a permission, written
// "<resource>:<action>". Some role must allow it and none may deny it. A role
// the policy does not declare contributes nothing.
func (p *Policy) Allows(roles []string, permission string) bool {
	allowed := false
	for _, name := range roles {
		r, ok := p.roles[name]
		if !ok {
			continue
		}
		if r.deny[permission] {
			return false
		}
		allowed = allowed || r.allow[permission]
	}
	return allowed
}

// Permissions lists what the roles together hold, in the order Compile was
// given. It is the (policy, roles) -> permissions half of a decision.
func (p *Policy) Permissions(roles []string) []string {
	var out []string
	for _, perm := range p.perms {
		if p.Allows(roles, perm) {
			out = append(out, perm)
		}
	}
	return out
}

// Outcome is why a decision came out the way it did.
type Outcome string

const (
	// Allowed means a rule allows the permission and none denies it.
	Allowed Outcome = "allowed"
	// Denied means a rule denies the permission. A deny beats every allow.
	Denied Outcome = "denied"
	// NoMatch means no rule mentions the permission, so the default applies.
	NoMatch Outcome = "no match"
)

// Explanation says how a decision was reached, for a person debugging access.
type Explanation struct {
	Permission string
	Allowed    bool
	Outcome    Outcome
	// Role is the held role the deciding rule reached the caller through.
	// It is empty when nothing matched.
	Role string
	// Path runs from Role to the role that declares Rule. It has one entry
	// when Role declares the rule itself.
	Path []string
	// Rule is the rule that decided. It is nil when nothing matched.
	Rule *Rule
	// Grant is the grant that gave the caller Role. It is nil when the roles
	// were supplied rather than derived from an identity.
	Grant *Grant
	// Held is every role the decision considered, with the grant behind
	// each when it is known.
	Held []HeldRole
	// Unknown lists supplied roles the policy does not declare. They grant
	// nothing.
	Unknown []string

	name string
}

// HeldRole is one role a caller holds and why.
type HeldRole struct {
	Role string
	// Grant is the first grant that gave the role, or nil if not known.
	Grant *Grant
}

// Explain decides a permission for an identity and says which grant and which
// rule produced the answer.
func (p *Policy) Explain(id Identity, permission string) Explanation {
	var held []HeldRole
	for _, g := range p.file.Grants {
		if !g.Subject.Matches(id) {
			continue
		}
		for _, r := range g.Roles {
			if !slices.ContainsFunc(held, func(h HeldRole) bool { return h.Role == r.Text }) {
				held = append(held, HeldRole{Role: r.Text, Grant: g})
			}
		}
	}
	return p.explain(held, permission)
}

// ExplainRoles decides a permission for a set of roles, when the identity
// that earned them is no longer at hand. The explanation names the rule and
// not the grant.
func (p *Policy) ExplainRoles(roles []string, permission string) Explanation {
	held := make([]HeldRole, len(roles))
	for i, r := range roles {
		held[i] = HeldRole{Role: r}
	}
	return p.explain(held, permission)
}

func (p *Policy) explain(held []HeldRole, permission string) Explanation {
	e := Explanation{Permission: permission, Outcome: NoMatch, Held: held, name: p.file.Name}
	var allow *Explanation
	for _, h := range held {
		r, ok := p.roles[h.Role]
		if !ok {
			e.Unknown = append(e.Unknown, h.Role)
			continue
		}
		for _, b := range r.rules {
			if !b.perms[permission] {
				continue
			}
			hit := e
			hit.Role, hit.Path, hit.Rule, hit.Grant = h.Role, b.path, b.rule, h.Grant
			if b.rule.Effect == Deny {
				hit.Outcome = Denied
				hit.Unknown = unknown(p, held)
				return hit
			}
			if allow == nil {
				hit.Outcome, hit.Allowed = Allowed, true
				allow = &hit
			}
		}
	}
	if allow != nil {
		allow.Unknown = unknown(p, held)
		return *allow
	}
	return e
}

func unknown(p *Policy, held []HeldRole) []string {
	var out []string
	for _, h := range held {
		if _, ok := p.roles[h.Role]; !ok {
			out = append(out, h.Role)
		}
	}
	return out
}

// String renders the explanation as a few lines an admin can read.
func (e Explanation) String() string {
	var b strings.Builder
	at := func(pos Pos) string {
		if e.name == "" {
			return pos.String()
		}
		return e.name + ":" + pos.String()
	}
	fmt.Fprintf(&b, "%s: %s\n", e.Permission, e.Outcome)
	switch e.Outcome {
	case NoMatch:
		if len(e.Held) == 0 {
			b.WriteString("  no grant matches this caller, so they hold no role\n")
		} else {
			b.WriteString("  no rule in the roles held mentions it, and the default is deny\n")
		}
	default:
		fmt.Fprintf(&b, "  rule:  %s (%s)\n", e.Rule, at(e.Rule.Pos))
		fmt.Fprintf(&b, "  role:  %s\n", strings.Join(e.Path, " extends "))
		if e.Grant != nil {
			fmt.Fprintf(&b, "  grant: %s (%s)\n", e.Grant, at(e.Grant.Pos))
		}
		if e.Outcome == Denied {
			b.WriteString("  a deny overrides every allow\n")
		}
	}
	if len(e.Held) > 0 {
		b.WriteString("  holds:")
		for i, h := range e.Held {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(" " + h.Role)
			if h.Grant != nil {
				fmt.Fprintf(&b, " (%s)", h.Grant.Subject)
			}
		}
		b.WriteString("\n")
	}
	if len(e.Unknown) > 0 {
		fmt.Fprintf(&b, "  not in the policy, so ignored: %s\n", strings.Join(e.Unknown, ", "))
	}
	return b.String()
}
