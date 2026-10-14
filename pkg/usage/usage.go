// Package usage counts what runs have used against a workspace's limits.
//
// It is a ledger and a projection of it. Every admission, ending, warning,
// refusal and model call is appended as an entry, and the counts a policy is
// judged on are read from those entries. Nothing is counted that is not in the ledger, so
// replaying the entries into an empty ledger gives the same answers: Rebuild
// does exactly that, and a test holds the two equal.
//
// # Scale
//
// The ledger is in memory. Admissions are kept for a little over two months,
// which is what a monthly limit needs, and warnings and refusals are kept to a
// fixed number. A count is a scan back through a tenant's admissions to the
// start of the period, so its cost grows with the runs in that period. A
// durable store belongs behind the same writes and one read: append to a
// table, and keep period counters beside it.
//
// # Spend, counted once
//
// What a run spends is counted from two sources, and never twice:
//
//   - Spent: one model call, as the runtime reported it on the run's stream
//     (usage.call). Its key is the runtime's AccountingID, which no other call
//     of any attempt shares, so a call delivered twice is counted once.
//   - Settled: the run's own total once it has ended, read from the runtime's
//     record (its terminal event, its output, or its ledger records for a run
//     that failed). Its key is "run:" and the run id. It adds only what the
//     calls already counted for that run do not cover: a call whose event was
//     missed. A terminal event delivered twice, or a reconciler pass after the
//     event, settles nothing more.
//
// A run's own total excludes the runs it called, which settle their own: so
// each model call in a tree of runs is counted once, in the run that made it.
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
	// Spent is one model call a run made.
	Spent Kind = "spent"
	// Settled is a run's own total at its end.
	Settled Kind = "settled"
	// Halted is a run in flight stopped because its scope reached a limit.
	Halted Kind = "halted"
)

// Spend is what some model calls used. Micros is US dollars only: money in
// any other currency is kept apart in Other and never converted, and a call
// with no price is counted in Unpriced with no money at all.
type Spend struct {
	Micros     int64         `json:"micros,omitempty"`
	ModelCalls int           `json:"model_calls,omitempty"`
	Tokens     int64         `json:"tokens,omitempty"`
	Unpriced   int           `json:"unpriced_calls,omitempty"`
	Other      []policy.Cost `json:"other_costs,omitempty"`
}

// USD is the one currency a spend limit is held in.
const USD = "USD"

// add returns s and o summed, money per currency.
func (s Spend) add(o Spend) Spend {
	s.Micros += o.Micros
	s.ModelCalls += o.ModelCalls
	s.Tokens += o.Tokens
	s.Unpriced += o.Unpriced
	s.Other = addCosts(s.Other, o.Other, 1)
	return s
}

// beyond is what s has that o does not: field by field, never below zero. It
// is what a run's settled total adds to the calls already counted for it.
func (s Spend) beyond(o Spend) Spend {
	pos := func(a, b int64) int64 { return max(a-b, 0) }
	out := Spend{
		Micros: pos(s.Micros, o.Micros), Tokens: pos(s.Tokens, o.Tokens),
		ModelCalls: int(pos(int64(s.ModelCalls), int64(o.ModelCalls))),
		Unpriced:   int(pos(int64(s.Unpriced), int64(o.Unpriced))),
	}
	for _, c := range s.Other {
		had := int64(0)
		for _, d := range o.Other {
			if d.Currency == c.Currency {
				had = d.Micros
			}
		}
		if left := pos(c.Micros, had); left > 0 {
			out.Other = append(out.Other, policy.Cost{Micros: left, Currency: c.Currency})
		}
	}
	return out
}

// IsZero reports whether nothing was used.
func (s Spend) IsZero() bool {
	return s.Micros == 0 && s.ModelCalls == 0 && s.Tokens == 0 && s.Unpriced == 0 && len(s.Other) == 0
}

func addCosts(to, from []policy.Cost, sign int64) []policy.Cost {
	out := append([]policy.Cost(nil), to...)
	for _, c := range from {
		i := 0
		for i < len(out) && out[i].Currency != c.Currency {
			i++
		}
		if i == len(out) {
			out = append(out, policy.Cost{Currency: c.Currency})
		}
		out[i].Micros += sign * c.Micros
	}
	return out
}

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
	// Key makes a Spent or Settled entry count once: the runtime's
	// AccountingID for a call, "run:<id>" for a run's settled total.
	Key string `json:"key,omitempty"`
	// Spend is what a Spent entry's call used, or a Settled entry's run used
	// of its own in all.
	Spend *Spend `json:"spend,omitempty"`
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

// spent is what was counted for one run at one time.
type spent struct {
	admission
	key   string
	spend Spend
}

type tenant struct {
	admissions []admission
	// active is the runs in flight, by id.
	active  map[string]admission
	notices []Entry
	// spends is what calls and settled runs added, oldest first.
	spends []spent
	// keys is every Spent and Settled key still held, so each counts once.
	keys map[string]bool
	// live is what has been counted for each run that has not settled.
	live map[string]Spend
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
		t = &tenant{active: map[string]admission{}, keys: map[string]bool{}, live: map[string]Spend{}}
		l.tenants[name] = t
	}
	return t
}

// Append records an entry and folds it into the counts. It is the only way the
// counts change. A Spent or Settled entry whose key was already counted is
// not recorded, and Append reports false.
func (l *Ledger) Append(e Entry) bool {
	_, ok := l.Record(e)
	return ok
}

// Record appends an entry, as Append does, and returns what it added to the
// counts: for a Spent entry its call, for a Settled entry only what the run's
// calls already counted did not cover. Anything else adds no spend.
func (l *Ledger) Record(e Entry) (Spend, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if (e.Kind == Spent || e.Kind == Settled) && (e.Key == "" || e.Spend == nil || l.of(e.Tenant).keys[e.Key]) {
		return Spend{}, false
	}
	l.entries = append(l.entries, e)
	added := l.apply(e)
	l.prune(e.At)
	return added, true
}

func (l *Ledger) apply(e Entry) Spend {
	t := l.of(e.Tenant)
	var added Spend
	switch e.Kind {
	case Admitted:
		a := admission{project: e.Project, agent: e.Agent, client: e.Client, runID: e.RunID, at: e.At}
		t.admissions = append(t.admissions, a)
		t.active[e.RunID] = a
	case Ended:
		delete(t.active, e.RunID)
	case Warned, Refused, Halted:
		t.notices = append(t.notices, e)
		if len(t.notices) > keepNotices {
			t.notices = t.notices[len(t.notices)-keepNotices:]
		}
	case Spent:
		added = *e.Spend
		t.live[e.RunID] = t.live[e.RunID].add(added)
	case Settled:
		added = e.Spend.beyond(t.live[e.RunID])
		delete(t.live, e.RunID)
	}
	if e.Kind == Spent || e.Kind == Settled {
		t.keys[e.Key] = true
		// Kept even when it adds nothing, so its key is held as long as
		// a counted one would be.
		at := admission{project: e.Project, agent: e.Agent, client: e.Client, runID: e.RunID, at: e.At}
		t.spends = append(t.spends, spent{admission: at, key: e.Key, spend: added})
	}
	return added
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
		t.spends = slices.DeleteFunc(t.spends, func(s spent) bool {
			if s.at.Before(cutoff) {
				delete(t.keys, s.key)
				delete(t.live, s.runID)
				return true
			}
			return false
		})
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

// Usage counts what a scope has used: runs started since a time, runs in
// flight now, and what model calls since that time spent.
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
	var sum Spend
	for _, s := range slices.Backward(t.spends) {
		if s.at.Before(since) {
			break
		}
		if matches(scope, s.admission) {
			sum = sum.add(s.spend)
		}
	}
	u.Spend, u.Tokens, u.ModelCalls, u.UnpricedCalls, u.OtherCosts = sum.Micros, sum.Tokens, sum.ModelCalls, sum.Unpriced, sum.Other
	return u
}

// RunSpend is what has been counted for one run so far: its calls, and its
// settlement once it has ended.
func (l *Ledger) RunSpend(tenantName, runID string) Spend {
	l.mu.RLock()
	defer l.mu.RUnlock()
	t, ok := l.tenants[tenantName]
	if !ok {
		return Spend{}
	}
	var sum Spend
	for _, s := range t.spends {
		if s.runID == runID {
			sum = sum.add(s.spend)
		}
	}
	return sum
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
		_ = l.apply(e)
	}
	return l
}
