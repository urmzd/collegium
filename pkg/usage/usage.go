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
// The ledger is in memory. Admissions and spend are kept for a little over two
// months, which is what a monthly limit needs, and warnings and refusals are
// kept to a fixed number. Counts are kept per scope and UTC day as entries are
// applied, so a count over a period sums at most 62 days of totals, and only
// a period that starts inside a day reads that day's entries. Where an entry
// falls is decided by its own time, never by the order it was appended in, so
// an entry stamped earlier than the one before it is still counted in its own
// day. A durable store belongs behind the same writes and one read: append to
// a table, and keep the day counters beside it.
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
//     that failed). Its key is "run:" and the run id.
//
// A run is counted as the larger, field by field, of its calls seen so far and
// its settled total. So a settlement adds only the calls whose events had not
// been seen, and a call seen after its run settled adds only what goes beyond
// the settled total: the settled total already counted it. A terminal event
// delivered twice, a reconciler pass, or a late call event counts nothing
// twice, in either order.
//
// A run's own total excludes the runs it called, which settle their own: so
// each model call in a tree of runs is counted once, in the run that made it.
package usage

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/urmzd/mandatum/pkg/agentrt/payload"

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

// OfCall is what one model call the runtime reported used, in the ledger's
// terms: dollars in Micros, any other currency in Other, and no price at all
// in Unpriced. A call saige answered from its response cache cost nothing and
// is not unpriced.
func OfCall(c payload.UsageCall) Spend {
	s := Spend{ModelCalls: 1, Tokens: int64(c.PromptTokens + c.CompletionTokens)}
	switch {
	case c.Cost != nil && c.Cost.Currency == USD:
		s.Micros = c.Cost.Micros
	case c.Cost != nil:
		s.Other = []policy.Cost{{Micros: c.Cost.Micros, Currency: c.Cost.Currency}}
	case !c.ResponseCacheHit:
		s.Unpriced = 1
	}
	return s
}

// Of is what a sum of calls used, in the ledger's terms. Dollars are read
// wherever the runtime put them: in Cost when the first priced call was in
// dollars, in OtherCosts when it was not.
func Of(u payload.Usage) Spend {
	s := Spend{ModelCalls: u.ModelCalls, Tokens: int64(u.PromptTokens + u.CompletionTokens), Unpriced: u.UnpricedCalls}
	costs := u.OtherCosts
	if u.Cost != nil {
		costs = append([]payload.Cost{*u.Cost}, costs...)
	}
	for _, c := range costs {
		if c.Currency == USD {
			s.Micros += c.Micros
			continue
		}
		s.Other = append(s.Other, policy.Cost{Micros: c.Micros, Currency: c.Currency})
	}
	return s
}

// ceil is the larger of s and o, field by field and currency by currency.
func (s Spend) ceil(o Spend) Spend {
	return s.add(o.beyond(s))
}

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
	Client string `json:"client,omitempty"`
	RunID  string `json:"run_id,omitempty"`
	// At is when the entry was appended, by the platform's clock, and decides
	// which period it counts in.
	At time.Time `json:"at"`
	// EventAt is when the runtime said the call happened, for a Spent entry.
	// It is kept for the record and counts nothing.
	EventAt time.Time `json:"event_at,omitzero"`
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

// Retention of admissions and spend, and how many warnings and refusals are
// kept.
const (
	keepAdmissions = 62 * 24 * time.Hour
	keepNotices    = 5000
)

type admission struct {
	project, agent, client, runID string
	at                            time.Time
}

// scopes is the keys of every scope an admission falls in.
func (a admission) scopes() []string {
	out := []string{"t"}
	if a.project != "" {
		out = append(out, "p\x00"+a.project)
	}
	if a.agent != "" {
		out = append(out, "a\x00"+a.agent)
	}
	if a.client != "" {
		out = append(out, "c\x00"+a.client)
	}
	return out
}

func scopeKey(s policy.Scope) string {
	switch s.Kind {
	case policy.Project:
		return "p\x00" + s.Name
	case policy.Agent:
		return "a\x00" + s.Name
	case policy.Client:
		return "c\x00" + s.Name
	}
	return "t"
}

// spent is what was counted for one run at one time.
type spent struct {
	admission
	key   string
	spend Spend
}

// day is one UTC day of a tenant's admissions and spend, with running totals
// per scope.
type day struct {
	start      time.Time
	admissions []admission
	spends     []spent
	runs       map[string]int
	spend      map[string]Spend
}

// runSpend is what has been seen of one run: its calls, and its settled total
// once it has one. It outlives the settlement, so a late call is judged
// against it.
type runSpend struct {
	live, settled Spend
	last          time.Time
}

func (r *runSpend) counted() Spend { return r.live.ceil(r.settled) }

type tenant struct {
	days map[int64]*day
	// active is the runs in flight, by id.
	active  map[string]admission
	notices []Entry
	// keys is every Spent and Settled key still held, so each counts once.
	keys map[string]bool
	runs map[string]*runSpend
}

// Ledger holds every tenant's usage.
type Ledger struct {
	mu      sync.RWMutex
	entries []Entry
	tenants map[string]*tenant
	// oldest is the start of the oldest day any tenant holds.
	oldest time.Time
}

// NewLedger returns an empty ledger.
func NewLedger() *Ledger { return &Ledger{tenants: map[string]*tenant{}} }

func (l *Ledger) of(name string) *tenant {
	t, ok := l.tenants[name]
	if !ok {
		t = &tenant{days: map[int64]*day{}, active: map[string]admission{}, keys: map[string]bool{}, runs: map[string]*runSpend{}}
		l.tenants[name] = t
	}
	return t
}

func (l *Ledger) dayOf(t *tenant, at time.Time) *day {
	start := policy.PeriodStart(policy.Day, at)
	d, ok := t.days[start.Unix()]
	if !ok {
		d = &day{start: start, runs: map[string]int{}, spend: map[string]Spend{}}
		t.days[start.Unix()] = d
		if l.oldest.IsZero() || start.Before(l.oldest) {
			l.oldest = start
		}
	}
	return d
}

// Append records an entry and folds it into the counts. It is the only way the
// counts change. A Spent or Settled entry whose key was already counted is
// not recorded, and Append reports false.
func (l *Ledger) Append(e Entry) bool {
	_, ok := l.Record(e)
	return ok
}

// Record appends an entry, as Append does, and returns what it added to the
// counts: for a Spent entry its call, unless its run had already settled a
// total that covers it, and for a Settled entry only what the run's calls
// already counted did not cover. Anything else adds no spend.
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
	a := admission{project: e.Project, agent: e.Agent, client: e.Client, runID: e.RunID, at: e.At}
	var added Spend
	switch e.Kind {
	case Admitted:
		d := l.dayOf(t, e.At)
		d.admissions = append(d.admissions, a)
		for _, k := range a.scopes() {
			d.runs[k]++
		}
		t.active[e.RunID] = a
	case Ended:
		delete(t.active, e.RunID)
	case Warned, Refused, Halted:
		t.notices = append(t.notices, e)
		if len(t.notices) > keepNotices {
			t.notices = t.notices[len(t.notices)-keepNotices:]
		}
	case Spent, Settled:
		r, ok := t.runs[e.RunID]
		if !ok {
			r = &runSpend{}
			t.runs[e.RunID] = r
		}
		before := r.counted()
		if e.Kind == Spent {
			r.live = r.live.add(*e.Spend)
		} else {
			r.settled = *e.Spend
		}
		if e.At.After(r.last) {
			r.last = e.At
		}
		added = r.counted().beyond(before)
		t.keys[e.Key] = true
		// Kept even when it adds nothing, so its key is held as long as a
		// counted one would be.
		d := l.dayOf(t, e.At)
		d.spends = append(d.spends, spent{admission: a, key: e.Key, spend: added})
		for _, k := range a.scopes() {
			d.spend[k] = d.spend[k].add(added)
		}
	}
	return added
}

// prune drops the days no period can still need.
func (l *Ledger) prune(now time.Time) {
	cutoff := policy.PeriodStart(policy.Day, now.Add(-keepAdmissions))
	if l.oldest.IsZero() || !l.oldest.Before(cutoff) {
		return
	}
	l.entries = slices.DeleteFunc(l.entries, func(e Entry) bool { return e.At.Before(cutoff) })
	l.oldest = time.Time{}
	for _, t := range l.tenants {
		for k, d := range t.days {
			if d.start.Before(cutoff) {
				for _, s := range d.spends {
					delete(t.keys, s.key)
				}
				delete(t.days, k)
				continue
			}
			if l.oldest.IsZero() || d.start.Before(l.oldest) {
				l.oldest = d.start
			}
		}
		for id, r := range t.runs {
			if r.last.Before(cutoff) {
				delete(t.runs, id)
			}
		}
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
// flight now, and what model calls since that time spent. A day that starts
// at or after since is read from its totals; only a day since falls inside is
// read entry by entry.
func (l *Ledger) Usage(tenantName string, scope policy.Scope, since time.Time) policy.Usage {
	l.mu.RLock()
	defer l.mu.RUnlock()
	t, ok := l.tenants[tenantName]
	if !ok {
		return policy.Usage{}
	}
	var u policy.Usage
	var sum Spend
	key := scopeKey(scope)
	for _, d := range t.days {
		if !d.start.AddDate(0, 0, 1).After(since) {
			continue
		}
		if !d.start.Before(since) {
			u.Runs += d.runs[key]
			sum = sum.add(d.spend[key])
			continue
		}
		for _, a := range d.admissions {
			if !a.at.Before(since) && matches(scope, a) {
				u.Runs++
			}
		}
		for _, s := range d.spends {
			if !s.at.Before(since) && matches(scope, s.admission) {
				sum = sum.add(s.spend)
			}
		}
	}
	for _, a := range t.active {
		if matches(scope, a) {
			u.Concurrent++
		}
	}
	u.Spend, u.Tokens, u.ModelCalls, u.UnpricedCalls, u.OtherCosts = sum.Micros, sum.Tokens, sum.ModelCalls, sum.Unpriced, sorted(sum.Other)
	return u
}

// sorted orders costs by currency, so a sum read from a map is the same every
// time.
func sorted(costs []policy.Cost) []policy.Cost {
	costs = slices.DeleteFunc(costs, func(c policy.Cost) bool { return c.Micros == 0 })
	if len(costs) == 0 {
		return nil
	}
	slices.SortFunc(costs, func(a, b policy.Cost) int { return strings.Compare(a.Currency, b.Currency) })
	return costs
}

// RunSpend is what has been counted for one run so far: the larger of its
// calls and its settled total.
func (l *Ledger) RunSpend(tenantName, runID string) Spend {
	l.mu.RLock()
	defer l.mu.RUnlock()
	t, ok := l.tenants[tenantName]
	if !ok {
		return Spend{}
	}
	r, ok := t.runs[runID]
	if !ok {
		return Spend{}
	}
	out := r.counted()
	out.Other = sorted(out.Other)
	return out
}

// Notices returns a tenant's warnings, refusals and halts, newest first.
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

// Entries returns the whole ledger, in the order it was appended.
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
