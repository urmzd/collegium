// Package usage counts what runs have used against a workspace's limits.
//
// It is a ledger and a projection of it. Every admission, ending, warning and
// refusal is appended as an entry, and the counts a policy is judged on are
// read from those entries. Nothing is counted that is not in the ledger, so
// replaying the entries into an empty ledger gives the same answers: Rebuild
// does exactly that, and a test holds the two equal.
//
// # Scale
//
// The ledger is in memory. Admissions are kept for a little over two months,
// which is what a monthly limit needs, and warnings and refusals are kept to a
// fixed number. A count is a scan back through a tenant's admissions to the
// start of the period, so its cost grows with the runs in that period. A
// durable store belongs behind the same four writes and one read: append to a
// table, and keep period counters beside it.
package usage

import (
	"slices"
	"sync"
	"time"

	"github.com/urmzd/collegium/pkg/policy"
)

// Kind is what a ledger entry records.
type Kind string

const (
	Admitted Kind = "admitted"
	Ended    Kind = "ended"
	Warned   Kind = "warned"
	Refused  Kind = "refused"
)

// Entry is one line of the ledger.
type Entry struct {
	Kind    Kind   `json:"kind"`
	Tenant  string `json:"tenant"`
	Project string `json:"project,omitempty"`
	Agent   string `json:"agent"`
	// Client is the external client that raised the run, if one did.
	Client string    `json:"client,omitempty"`
	RunID  string    `json:"run_id,omitempty"`
	At     time.Time `json:"at"`
	// For a warning or a refusal: the policy that caused it, at the version
	// it had then, the limit it turned on, and the reason given.
	Scope   *policy.Scope `json:"scope,omitempty"`
	Version int           `json:"version,omitempty"`
	Limit   policy.Limit  `json:"limit,omitempty"`
	Reason  string        `json:"reason,omitempty"`
}

// Retention of admissions, and how many warnings and refusals are kept.
const (
	keepAdmissions = 62 * 24 * time.Hour
	keepNotices    = 5000
)

type admission struct {
	project, agent, client, runID string
	at                            time.Time
}

type tenant struct {
	admissions []admission
	// active is the runs in flight, by id.
	active  map[string]admission
	notices []Entry
}

// Ledger holds every tenant's usage.
type Ledger struct {
	mu      sync.RWMutex
	entries []Entry
	tenants map[string]*tenant
}

// NewLedger returns an empty ledger.
func NewLedger() *Ledger { return &Ledger{tenants: map[string]*tenant{}} }

func (l *Ledger) of(name string) *tenant {
	t, ok := l.tenants[name]
	if !ok {
		t = &tenant{active: map[string]admission{}}
		l.tenants[name] = t
	}
	return t
}

// Append records an entry and folds it into the counts. It is the only way the
// counts change.
func (l *Ledger) Append(e Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, e)
	l.apply(e)
	l.prune(e.At)
}

func (l *Ledger) apply(e Entry) {
	t := l.of(e.Tenant)
	switch e.Kind {
	case Admitted:
		a := admission{project: e.Project, agent: e.Agent, client: e.Client, runID: e.RunID, at: e.At}
		t.admissions = append(t.admissions, a)
		t.active[e.RunID] = a
	case Ended:
		delete(t.active, e.RunID)
	case Warned, Refused:
		t.notices = append(t.notices, e)
		if len(t.notices) > keepNotices {
			t.notices = t.notices[len(t.notices)-keepNotices:]
		}
	}
}

// prune drops what no period can still need. Entries are appended in time
// order, so the oldest are at the front.
func (l *Ledger) prune(now time.Time) {
	cutoff := now.Add(-keepAdmissions)
	if len(l.entries) == 0 || !l.entries[0].At.Before(cutoff) {
		return
	}
	l.entries = slices.DeleteFunc(l.entries, func(e Entry) bool { return e.At.Before(cutoff) })
	for _, t := range l.tenants {
		t.admissions = slices.DeleteFunc(t.admissions, func(a admission) bool { return a.at.Before(cutoff) })
	}
}

func matches(s policy.Scope, a admission) bool {
	switch s.Kind {
	case policy.Project:
		return a.project == s.Name
	case policy.Agent:
		return a.agent == s.Name
	case policy.Client:
		return a.client == s.Name
	}
	return true
}

// Usage counts what a scope has used: runs started since a time, and runs in
// flight now. Spend is zero until the runtime reports what a run cost.
func (l *Ledger) Usage(tenantName string, scope policy.Scope, since time.Time) policy.Usage {
	l.mu.RLock()
	defer l.mu.RUnlock()
	t, ok := l.tenants[tenantName]
	if !ok {
		return policy.Usage{}
	}
	var u policy.Usage
	for _, a := range slices.Backward(t.admissions) {
		if a.at.Before(since) {
			break
		}
		if matches(scope, a) {
			u.Runs++
		}
	}
	for _, a := range t.active {
		if matches(scope, a) {
			u.Concurrent++
		}
	}
	return u
}

// Notices returns a tenant's warnings and refusals, newest first.
func (l *Ledger) Notices(tenantName string, limit int) []Entry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	t, ok := l.tenants[tenantName]
	if !ok {
		return []Entry{}
	}
	out := slices.Clone(t.notices)
	slices.Reverse(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Entries returns the whole ledger, oldest first.
func (l *Ledger) Entries() []Entry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return slices.Clone(l.entries)
}

// Rebuild replays entries into a new ledger. A ledger rebuilt from another's
// entries answers every Usage question the same way.
func Rebuild(entries []Entry) *Ledger {
	l := NewLedger()
	for _, e := range entries {
		l.entries = append(l.entries, e)
		l.apply(e)
	}
	return l
}
