package policy

import (
	"fmt"
	"slices"
	"sync"
	"time"
)

// Change is one entry in a tenant's policy history: who changed which scope's
// policy, when, and what it was before and after. A delete has no After, and a
// first write has no Before.
type Change struct {
	Scope  Scope     `json:"scope"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
	Before *Policy   `json:"before,omitempty"`
	After  *Policy   `json:"after,omitempty"`
}

// maxHistory bounds a tenant's history. The oldest changes are dropped first.
const maxHistory = 1000

type tenant struct {
	policies []Policy
	history  []Change
	// versions outlive a delete, so a policy set again continues its count
	// and a version always names one state.
	versions map[Scope]int
}

// Store holds every tenant's policies and the history of their changes. It is
// in memory and is lost on restart.
type Store struct {
	mu      sync.RWMutex
	tenants map[string]*tenant
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{tenants: map[string]*tenant{}} }

func (s *Store) of(name string) *tenant {
	t, ok := s.tenants[name]
	if !ok {
		t = &tenant{versions: map[Scope]int{}}
		s.tenants[name] = t
	}
	return t
}

// Put sets a scope's policy and records the change. A narrower policy must fit
// within the tenant's, and tightening the tenant's must not leave a narrower
// one claiming more than it.
func (s *Store) Put(tenantName string, p Policy, by string, at time.Time) (Policy, error) {
	if err := p.Validate(); err != nil {
		return Policy{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.of(tenantName)
	for _, other := range t.policies {
		switch {
		case p.Scope.Kind != Tenant && other.Scope.Kind == Tenant:
			if err := p.Within(other); err != nil {
				return Policy{}, err
			}
		case p.Scope.Kind == Tenant && other.Scope.Kind != Tenant:
			if err := other.Within(p); err != nil {
				return Policy{}, fmt.Errorf("%w. Lower that one first", err)
			}
		}
	}
	t.versions[p.Scope]++
	p.Version, p.UpdatedBy, p.UpdatedAt = t.versions[p.Scope], by, at
	change := Change{Scope: p.Scope, By: by, At: at, After: &p}
	if i := slices.IndexFunc(t.policies, func(x Policy) bool { return x.Scope == p.Scope }); i >= 0 {
		before := t.policies[i]
		change.Before = &before
		t.policies[i] = p
	} else {
		t.policies = append(t.policies, p)
	}
	t.record(change)
	return p, nil
}

func (t *tenant) record(c Change) {
	t.history = append(t.history, c)
	if len(t.history) > maxHistory {
		t.history = t.history[len(t.history)-maxHistory:]
	}
}

// Delete removes a scope's policy and records the change.
func (s *Store) Delete(tenantName string, scope Scope, by string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.of(tenantName)
	i := slices.IndexFunc(t.policies, func(x Policy) bool { return x.Scope == scope })
	if i < 0 {
		return fmt.Errorf("%w: no policy for %s", ErrNotFound, scope)
	}
	before := t.policies[i]
	t.policies = slices.Delete(t.policies, i, i+1)
	t.record(Change{Scope: scope, By: by, At: at, Before: &before})
	return nil
}

// List returns a tenant's policies: the tenant's first, then by kind and name.
func (s *Store) List(tenantName string) []Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tenants[tenantName]
	if !ok {
		return []Policy{}
	}
	out := slices.Clone(t.policies)
	order := map[Kind]int{Tenant: 0, Project: 1, Agent: 2, Client: 3}
	slices.SortFunc(out, func(a, b Policy) int {
		if d := order[a.Scope.Kind] - order[b.Scope.Kind]; d != 0 {
			return d
		}
		if a.Scope.Name < b.Scope.Name {
			return -1
		}
		if a.Scope.Name > b.Scope.Name {
			return 1
		}
		return 0
	})
	return out
}

// For returns the policies a run falls under, most specific first: its agent's,
// the client's that raised it, its project's, then the tenant's. An empty name
// means the run has no such scope.
func (s *Store) For(tenantName, project, agent, client string) []Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tenants[tenantName]
	if !ok {
		return nil
	}
	var out []Policy
	for _, want := range []Scope{{Agent, agent}, {Client, client}, {Project, project}, {Kind: Tenant}} {
		if want.Kind != Tenant && want.Name == "" {
			continue
		}
		if i := slices.IndexFunc(t.policies, func(x Policy) bool { return x.Scope == want }); i >= 0 {
			out = append(out, t.policies[i])
		}
	}
	return out
}

// History returns a tenant's policy changes, newest first.
func (s *Store) History(tenantName string) []Change {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tenants[tenantName]
	if !ok {
		return []Change{}
	}
	out := slices.Clone(t.history)
	slices.Reverse(out)
	return out
}

// At returns the policy a scope had at a moment, so "what was the limit when
// this run was refused" has an answer for as long as the history reaches.
func (s *Store) At(tenantName string, scope Scope, when time.Time) (Policy, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tenants[tenantName]
	if !ok {
		return Policy{}, false
	}
	var found *Policy
	for _, c := range t.history {
		if c.Scope != scope || c.At.After(when) {
			continue
		}
		found = c.After
	}
	if found == nil {
		return Policy{}, false
	}
	return *found, true
}
