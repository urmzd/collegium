// Package policy decides whether a run may start, given the limits a workspace
// has set and what has already been used.
//
// A policy belongs to a scope: the whole tenant, one project, one agent, or one
// client (a Slack or GitHub installation that raises runs). A run is checked against every policy whose scope it falls in, and the
// strictest answer wins, so a narrower policy can tighten what applies but can
// never loosen it.
//
// # One pure decision
//
// Decide takes the policies, the usage counted against each, and the request,
// and returns a Decision. It reads no clock and does no I/O, so every rule here
// is tested as a table, and the same inputs always give the same answer. The
// caller supplies usage for each policy's own period, which PeriodStart makes a
// pure function of the time.
//
// # What is metered
//
// Runs started in the period and runs in flight are counted exactly. Spend is
// part of the model and of Decide, but the runtime does not report what a run
// cost, so the usage it is given is always zero today: a spend limit is
// recorded and versioned, and not yet enforced.
package policy

import (
	"errors"
	"fmt"
	"time"
)

// Errors.
var (
	ErrInvalid  = errors.New("policy: invalid")
	ErrNotFound = errors.New("policy: not found")
)

// Kind is what a policy covers.
type Kind string

const (
	Tenant  Kind = "tenant"
	Project Kind = "project"
	Agent   Kind = "agent"
	// Client is one external client of the workspace. Its name is the
	// client's id, and a run falls under it when that client raised it.
	Client Kind = "client"
)

// MaxClientsCeiling is the most clients a tenant policy may allow.
const MaxClientsCeiling = 100

// Scope names what a policy covers. A tenant scope has no name.
type Scope struct {
	Kind Kind   `json:"kind"`
	Name string `json:"name,omitempty"`
}

func (s Scope) String() string {
	if s.Kind == Tenant {
		return "the workspace"
	}
	return string(s.Kind) + " " + s.Name
}

// Period is how often a policy's counts start again.
type Period string

const (
	Day   Period = "day"
	Month Period = "month"
)

// PeriodStart returns when the period containing now began, in UTC. A day
// starts at midnight and a month on its first day.
func PeriodStart(p Period, now time.Time) time.Time {
	now = now.UTC()
	if p == Month {
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// PeriodEnd returns when the period containing now ends.
func PeriodEnd(p Period, now time.Time) time.Time {
	start := PeriodStart(p, now)
	if p == Month {
		return start.AddDate(0, 1, 0)
	}
	return start.AddDate(0, 0, 1)
}

// Action is what happens to a run that would pass a limit.
type Action string

const (
	// Block refuses the run.
	Block Action = "block"
	// Approve holds the run for a person. Decide supports it. The platform
	// cannot park a run on a person yet, so it is not accepted in a stored
	// policy.
	Approve Action = "approve"
)

// DefaultWarnAt is the share of a limit, in percent, at which usage warns.
const DefaultWarnAt = 80

// Policy is the limits for one scope. A zero limit means this scope sets none,
// and whatever a wider scope set still applies.
type Policy struct {
	Scope  Scope  `json:"scope"`
	Period Period `json:"period"`
	// MaxRuns is the most runs that may start in the period.
	MaxRuns int `json:"max_runs,omitempty"`
	// MaxConcurrent is the most runs that may be in flight at once.
	MaxConcurrent int `json:"max_concurrent,omitempty"`
	// SpendLimit is the most that may be spent in the period, in micro-USD.
	SpendLimit int64 `json:"spend_limit,omitempty"`
	// MaxLease is the largest budget one run may be granted, in micro-USD.
	MaxLease int64 `json:"max_lease,omitempty"`
	// MaxStorage is the most bytes of published files the scope may hold. It
	// is a standing total, not a rate, so the period does not apply to it.
	MaxStorage int64 `json:"max_storage,omitempty"`
	// MaxClients is the most external clients the workspace may have, and
	// MaxClientsPerSurface the most on any one surface. They are standing
	// totals and belong to the tenant's policy alone.
	MaxClients           int `json:"max_clients,omitempty"`
	MaxClientsPerSurface int `json:"max_clients_per_surface,omitempty"`
	// WarnAt is the share of a limit, in percent, at which usage warns. Zero
	// means DefaultWarnAt.
	WarnAt int    `json:"warn_at,omitempty"`
	Action Action `json:"action"`

	// Version counts changes to this scope's policy, from 1.
	Version   int       `json:"version"`
	UpdatedBy string    `json:"updated_by,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (p Policy) warnAt() int {
	if p.WarnAt <= 0 {
		return DefaultWarnAt
	}
	return p.WarnAt
}

// Validate checks a policy on its own.
func (p Policy) Validate() error {
	switch {
	case p.Scope.Kind != Tenant && p.Scope.Kind != Project && p.Scope.Kind != Agent && p.Scope.Kind != Client:
		return fmt.Errorf("%w: a policy covers the tenant, a project, an agent or a client", ErrInvalid)
	case p.Scope.Kind == Tenant && p.Scope.Name != "":
		return fmt.Errorf("%w: the tenant policy has no name", ErrInvalid)
	case p.Scope.Kind != Tenant && p.Scope.Name == "":
		return fmt.Errorf("%w: a %s policy names the %s it covers", ErrInvalid, p.Scope.Kind, p.Scope.Kind)
	case p.Period != Day && p.Period != Month:
		return fmt.Errorf("%w: the period is day or month", ErrInvalid)
	case p.MaxRuns < 0 || p.MaxConcurrent < 0 || p.SpendLimit < 0 || p.MaxLease < 0 || p.MaxStorage < 0 ||
		p.MaxClients < 0 || p.MaxClientsPerSurface < 0:
		return fmt.Errorf("%w: a limit cannot be negative", ErrInvalid)
	case p.Scope.Kind != Tenant && (p.MaxClients > 0 || p.MaxClientsPerSurface > 0):
		return fmt.Errorf("%w: only the tenant's policy limits how many clients there are", ErrInvalid)
	case p.Scope.Kind == Client && p.MaxStorage > 0:
		return fmt.Errorf("%w: storage is not counted per client", ErrInvalid)
	case p.MaxClients > MaxClientsCeiling || p.MaxClientsPerSurface > MaxClientsCeiling:
		return fmt.Errorf("%w: a workspace may have at most %d clients", ErrInvalid, MaxClientsCeiling)
	case p.MaxRuns == 0 && p.MaxConcurrent == 0 && p.SpendLimit == 0 && p.MaxLease == 0 && p.MaxStorage == 0 &&
		p.MaxClients == 0 && p.MaxClientsPerSurface == 0:
		return fmt.Errorf("%w: a policy sets at least one limit", ErrInvalid)
	case p.WarnAt < 0 || p.WarnAt > 100:
		return fmt.Errorf("%w: the warning threshold is a percentage from 1 to 100", ErrInvalid)
	case p.Action == Approve:
		return fmt.Errorf("%w: holding a run for approval is not built yet, so the action must be block", ErrInvalid)
	case p.Action != Block:
		return fmt.Errorf("%w: the action at the limit is block", ErrInvalid)
	}
	return nil
}

// Within checks that a narrower policy does not claim more than a wider one
// allows. Every applicable policy is enforced whatever a narrower one says, so
// a looser child could never take effect: it is refused so the stored limits
// say what will actually happen.
func (p Policy) Within(parent Policy) error {
	// Storage is a total, so it compares whatever the periods are.
	if parent.MaxStorage > 0 && p.MaxStorage > parent.MaxStorage {
		return fmt.Errorf("%w: %s allows more storage than %s", ErrInvalid, p.Scope, parent.Scope)
	}
	if p.Period != parent.Period {
		// Different periods cannot be compared number for number.
		return nil
	}
	over := func(child, limit int64) bool { return limit > 0 && child > limit }
	switch {
	case over(int64(p.MaxRuns), int64(parent.MaxRuns)):
		return fmt.Errorf("%w: %s allows %d runs, more than the %d %s allows", ErrInvalid, p.Scope, p.MaxRuns, parent.MaxRuns, parent.Scope)
	case over(int64(p.MaxConcurrent), int64(parent.MaxConcurrent)):
		return fmt.Errorf("%w: %s allows %d at once, more than the %d %s allows", ErrInvalid, p.Scope, p.MaxConcurrent, parent.MaxConcurrent, parent.Scope)
	case over(p.SpendLimit, parent.SpendLimit):
		return fmt.Errorf("%w: %s has a higher spend limit than %s", ErrInvalid, p.Scope, parent.Scope)
	case over(p.MaxLease, parent.MaxLease):
		return fmt.Errorf("%w: %s allows a larger budget per run than %s", ErrInvalid, p.Scope, parent.Scope)
	}
	return nil
}

// Usage is what has been used against one policy, in that policy's period.
type Usage struct {
	// Runs is how many runs started in the period.
	Runs int `json:"runs"`
	// Concurrent is how many runs are in flight now.
	Concurrent int `json:"concurrent"`
	// Spend is what has been spent in the period, in micro-USD.
	Spend int64 `json:"spend"`
}

// Applied is a policy and the usage counted against it.
type Applied struct {
	Policy Policy
	Usage  Usage
}

// Request is one run asking to start.
type Request struct {
	// Lease is the budget the run asks for, in micro-USD. Zero asks for none.
	Lease int64
}

// Verdict is the answer to a request, from least to most restrictive.
type Verdict string

const (
	Allow         Verdict = "allow"
	Warn          Verdict = "warn"
	NeedsApproval Verdict = "needs_approval"
	Deny          Verdict = "deny"
)

func (v Verdict) rank() int {
	switch v {
	case Deny:
		return 3
	case NeedsApproval:
		return 2
	case Warn:
		return 1
	}
	return 0
}

// Limit names which limit a decision turned on.
type Limit string

const (
	LimitRuns       Limit = "runs"
	LimitConcurrent Limit = "concurrent"
	LimitSpend      Limit = "spend"
	LimitLease      Limit = "lease"
)

// Decision is whether a run may start, and why.
type Decision struct {
	Verdict Verdict `json:"verdict"`
	// Reason is a sentence a person can read. Empty when allowed outright.
	Reason string `json:"reason,omitempty"`
	// Policy is the one that decided it. Nil when no limit came into play.
	Policy *Policy `json:"policy,omitempty"`
	// Limit is the limit that decided it.
	Limit Limit `json:"limit,omitempty"`
	// Remaining is what is left under that limit before this run: runs, slots
	// or micro-USD.
	Remaining int64 `json:"remaining"`
}

// Decide answers a request against every policy that applies to it. The
// strictest answer wins. Among equals the first policy given wins, so callers
// pass the most specific scope first and a denial names the nearest limit.
func Decide(applied []Applied, req Request) Decision {
	best := Decision{Verdict: Allow}
	for _, a := range applied {
		if d := decideOne(a, req); d.Verdict.rank() > best.Verdict.rank() {
			best = d
		}
	}
	return best
}

func decideOne(a Applied, req Request) Decision {
	p := a.Policy
	best := Decision{Verdict: Allow}
	consider := func(limit Limit, used, ask, max int64, what string) {
		if max <= 0 {
			return
		}
		d := Decision{Policy: &p, Limit: limit, Remaining: max0(max - used)}
		switch {
		case used+ask > max:
			d.Verdict = Deny
			if p.Action == Approve {
				d.Verdict = NeedsApproval
			}
			d.Reason = fmt.Sprintf("%s has reached its limit of %s", p.Scope, what)
		case (used+ask)*100 >= max*int64(p.warnAt()):
			d.Verdict = Warn
			d.Reason = fmt.Sprintf("%s is at %d%% of its limit of %s", p.Scope, (used+ask)*100/max, what)
		default:
			return
		}
		if d.Verdict.rank() > best.Verdict.rank() {
			best = d
		}
	}
	per := "a " + string(p.Period)
	consider(LimitRuns, int64(a.Usage.Runs), 1, int64(p.MaxRuns), counted(p.MaxRuns)+" "+per)
	consider(LimitConcurrent, int64(a.Usage.Concurrent), 1, int64(p.MaxConcurrent), counted(p.MaxConcurrent)+" at once")
	consider(LimitSpend, a.Usage.Spend, req.Lease, p.SpendLimit, "spend "+per)
	// A lease is judged on its own size, not against what was used.
	if p.MaxLease > 0 && req.Lease > p.MaxLease {
		d := Decision{
			Verdict: Deny, Policy: &p, Limit: LimitLease, Remaining: p.MaxLease,
			Reason: fmt.Sprintf("%s allows no run a budget this large", p.Scope),
		}
		if d.Verdict.rank() > best.Verdict.rank() {
			best = d
		}
	}
	return best
}

// counted writes a number of runs: "1 run", "10 runs".
func counted(n int) string {
	if n == 1 {
		return "1 run"
	}
	return fmt.Sprintf("%d runs", n)
}

func max0(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}
