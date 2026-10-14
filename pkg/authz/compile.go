package authz

import (
	"fmt"
	"slices"
	"strings"
)

// Policy is a checked policy, ready to answer decisions. It is immutable and
// safe for concurrent use.
type Policy struct {
	file *File
	// perms is every permission that exists, in the order Compile was given.
	perms []string
	roles map[string]*role
	// order is the roles in source order.
	order []string
}

// role is a role with its inheritance resolved.
type role struct {
	decl *RoleDecl
	// rules are the role's own rules and those of every role it extends,
	// each with the chain of roles that leads to it.
	rules []boundRule
	allow map[string]bool
	deny  map[string]bool
}

type boundRule struct {
	rule *Rule
	// path runs from the role holding the rule to the role declaring it.
	// It has one entry when the role declares the rule itself.
	path []string
	// perms are the permissions the rule names.
	perms map[string]bool
}

// Limits on a policy. Each keeps compiling and deciding cheap, and a real
// policy is far inside all of them. A policy over a limit does not compile.
const (
	MaxRoles  = 256
	MaxRules  = 4096
	MaxGrants = 4096
	// MaxDepth is the longest chain of extends.
	MaxDepth = 32
)

// Compile checks a parsed policy against the permissions that exist, each
// written "<resource>:<action>". It reports the first problem as an *Error.
//
// It rejects an empty policy, a policy over a limit, a role declared twice, a role that extends or a
// grant that names a role that does not exist, an inheritance cycle, and an
// action or resource that is in no permission.
func Compile(f *File, permissions []string) (*Policy, error) {
	if f == nil {
		return nil, &Error{Pos: Pos{1, 1}, Msg: "no policy"}
	}
	fail := func(pos Pos, format string, args ...any) error {
		return &Error{Name: f.Name, Pos: pos, Msg: fmt.Sprintf(format, args...)}
	}
	if len(f.Roles) == 0 {
		// A policy with no roles allows nothing. That is a legal outcome
		// of default deny, but as a whole file it is far more likely to
		// be a truncated or wrong one.
		return nil, fail(Pos{1, 1}, "policy is empty: it declares no roles, so nobody could do anything")
	}

	rules := 0
	for _, d := range f.Roles {
		rules += len(d.Rules)
	}
	switch {
	case len(f.Roles) > MaxRoles:
		return nil, fail(f.Roles[MaxRoles].Pos, "too many roles: at most %d", MaxRoles)
	case rules > MaxRules:
		return nil, fail(Pos{1, 1}, "too many rules: %d, at most %d", rules, MaxRules)
	case len(f.Grants) > MaxGrants:
		return nil, fail(f.Grants[MaxGrants].Pos, "too many grants: at most %d", MaxGrants)
	}

	actions, resources := map[string]bool{}, map[string]bool{}
	known := map[string]bool{}
	for _, perm := range permissions {
		res, act, ok := strings.Cut(perm, ":")
		if !ok || res == "" || act == "" {
			return nil, fmt.Errorf("authz: permission %q is not <resource>:<action>", perm)
		}
		known[perm], resources[res], actions[act] = true, true, true
	}

	p := &Policy{file: f, perms: slices.Clone(permissions), roles: map[string]*role{}}
	for _, d := range f.Roles {
		if prev, dup := p.roles[d.Name.Text]; dup {
			return nil, fail(d.Name.Pos, "role %q is already declared at %s", d.Name.Text, prev.decl.Name.Pos)
		}
		p.roles[d.Name.Text] = &role{decl: d}
		p.order = append(p.order, d.Name.Text)
	}
	for _, d := range f.Roles {
		for _, e := range d.Extends {
			if _, ok := p.roles[e.Text]; !ok {
				return nil, fail(e.Pos, "role %q extends unknown role %q, expected one of: %s", d.Name.Text, e.Text, strings.Join(p.order, ", "))
			}
		}
		for _, r := range d.Rules {
			for _, a := range r.Actions {
				if !actions[a.Text] {
					return nil, fail(a.Pos, "unknown action %q, expected one of: %s", a.Text, sortedKeys(actions))
				}
			}
			for _, res := range r.Resources {
				if !resources[res.Text] {
					return nil, fail(res.Pos, "unknown resource %q, expected one of: %s", res.Text, sortedKeys(resources))
				}
			}
		}
	}
	for _, g := range f.Grants {
		for _, r := range g.Roles {
			if _, ok := p.roles[r.Text]; !ok {
				return nil, fail(r.Pos, "grant names unknown role %q, expected one of: %s", r.Text, strings.Join(p.order, ", "))
			}
		}
	}

	// Resolve inheritance depth first. state is 1 while a role is being
	// resolved, so meeting it again is a cycle, and 2 once it is done.
	state := map[string]int{}
	// depth is how many roles the longest chain from a role holds, itself
	// included.
	depth := map[string]int{}
	var resolve func(name string, at Pos, trail []string) error
	resolve = func(name string, at Pos, trail []string) error {
		r := p.roles[name]
		switch state[name] {
		case 2:
			return nil
		case 1:
			start := slices.Index(trail, name)
			return fail(at, "inheritance cycle: %s", strings.Join(append(slices.Clone(trail[start:]), name), " extends "))
		}
		state[name], depth[name] = 1, 1
		trail = append(trail, name)
		for _, rule := range r.decl.Rules {
			r.rules = append(r.rules, boundRule{rule: rule, path: []string{name}, perms: expand(rule, permissions, known)})
		}
		seen := map[*Rule]bool{}
		for _, e := range r.decl.Extends {
			if err := resolve(e.Text, e.Pos, trail); err != nil {
				return err
			}
			if d := depth[e.Text] + 1; d > depth[name] {
				if d > MaxDepth {
					return fail(e.Pos, "inheritance is deeper than %d roles at %q", MaxDepth, name)
				}
				depth[name] = d
			}
			for _, inherited := range p.roles[e.Text].rules {
				// A role reached by two paths contributes its rules once.
				if seen[inherited.rule] {
					continue
				}
				seen[inherited.rule] = true
				r.rules = append(r.rules, boundRule{
					rule:  inherited.rule,
					path:  append([]string{name}, inherited.path...),
					perms: inherited.perms,
				})
			}
		}
		r.allow, r.deny = map[string]bool{}, map[string]bool{}
		for _, b := range r.rules {
			into := r.allow
			if b.rule.Effect == Deny {
				into = r.deny
			}
			for perm := range b.perms {
				into[perm] = true
			}
		}
		state[name] = 2
		return nil
	}
	for _, name := range p.order {
		if err := resolve(name, p.roles[name].decl.Name.Pos, nil); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// MustCompile is Parse and Compile for a policy known to be valid, such as an
// embedded one covered by a test. It panics on an error.
func MustCompile(name, src string, permissions []string) *Policy {
	p, err := Load(name, src, permissions)
	if err != nil {
		panic("authz: " + err.Error())
	}
	return p
}

// Load is Parse followed by Compile.
func Load(name, src string, permissions []string) (*Policy, error) {
	f, err := Parse(name, src)
	if err != nil {
		return nil, err
	}
	return Compile(f, permissions)
}

// expand lists the permissions a rule names. A pair of a named action and a
// named resource that is not a permission is skipped: the names were each
// checked, and "*" must never invent a permission that does not exist.
func expand(r *Rule, permissions []string, known map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, perm := range permissions {
		res, act, _ := strings.Cut(perm, ":")
		if !known[perm] || !matches(r.Actions, act) || !matches(r.Resources, res) {
			continue
		}
		out[perm] = true
	}
	return out
}

func matches(ns []Name, s string) bool {
	if len(ns) == 0 {
		return true
	}
	for _, n := range ns {
		if n.Text == s {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return strings.Join(keys, ", ")
}

// WithGrants returns a copy of the policy with more grants appended, checked
// the same way as grants written in the source. It is how configuration older
// than the language, such as a table of groups, becomes part of the policy.
func (p *Policy) WithGrants(grants ...*Grant) (*Policy, error) {
	f := &File{Name: p.file.Name, Roles: p.file.Roles, Grants: slices.Clone(p.file.Grants)}
	f.Grants = append(f.Grants, grants...)
	return Compile(f, p.perms)
}

// RoleNames lists the declared roles in source order.
func (p *Policy) RoleNames() []string { return slices.Clone(p.order) }

// HasRole reports whether the policy declares a role.
func (p *Policy) HasRole(name string) bool {
	_, ok := p.roles[name]
	return ok
}

// Grants lists the policy's grants in source order.
func (p *Policy) Grants() []*Grant { return slices.Clone(p.file.Grants) }
