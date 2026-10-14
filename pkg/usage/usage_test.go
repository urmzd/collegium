package usage

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/urmzd/collegium/pkg/policy"
)

var t0 = time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)

var (
	all    = policy.Scope{Kind: policy.Tenant}
	launch = policy.Scope{Kind: policy.Project, Name: "Launch"}
	lead   = policy.Scope{Kind: policy.Agent, Name: "lead"}
)

func admit(l *Ledger, tenant, project, agent, id string, at time.Time) {
	l.Append(Entry{Kind: Admitted, Tenant: tenant, Project: project, Agent: agent, RunID: id, At: at})
}

func TestUsageByScopeAndPeriod(t *testing.T) {
	l := NewLedger()
	admit(l, "acme", "Launch", "lead", "r1", t0.Add(-time.Hour)) // yesterday
	admit(l, "acme", "Launch", "lead", "r2", t0)                 // the first instant of today
	admit(l, "acme", "", "helper", "r3", t0.Add(time.Hour))
	admit(l, "globex", "Launch", "lead", "r9", t0.Add(time.Hour))
	l.Append(Entry{Kind: Ended, Tenant: "acme", RunID: "r1", At: t0.Add(2 * time.Hour)})

	for name, tc := range map[string]struct {
		scope      policy.Scope
		since      time.Time
		runs, live int
	}{
		"tenant, today":      {all, t0, 2, 2},
		"tenant, this month": {all, policy.PeriodStart(policy.Month, t0), 3, 2},
		"project, today":     {launch, t0, 1, 1},
		"agent, this month":  {lead, policy.PeriodStart(policy.Month, t0), 2, 1},
	} {
		got := l.Usage("acme", tc.scope, tc.since)
		if got.Runs != tc.runs || got.Concurrent != tc.live {
			t.Errorf("%s: %d runs, %d in flight, want %d and %d", name, got.Runs, got.Concurrent, tc.runs, tc.live)
		}
	}
	if got := l.Usage("initech", all, t0); !reflect.DeepEqual(got, policy.Usage{}) {
		t.Errorf("a tenant with no runs used %+v", got)
	}
}

// The counts are a projection of the entries: replaying the entries into an
// empty ledger must answer every question the same way.
func TestRebuildMatchesTheLiveLedger(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	l := NewLedger()
	projects, agents := []string{"", "Launch", "Billing"}, []string{"lead", "helper", "oncall"}
	var open []string
	at := t0
	for i := range 2000 {
		at = at.Add(time.Duration(rng.IntN(600)) * time.Second)
		switch {
		case len(open) > 0 && rng.IntN(3) == 0:
			j := rng.IntN(len(open))
			l.Append(Entry{Kind: Ended, Tenant: "acme", RunID: open[j], At: at})
			open = append(open[:j], open[j+1:]...)
		case rng.IntN(20) == 0:
			l.Append(Entry{Kind: Refused, Tenant: "acme", Agent: "lead", At: at, Scope: &launch, Version: 1, Limit: policy.LimitRuns})
		case len(open) > 0 && rng.IntN(3) == 0:
			// A call, sometimes delivered twice, sometimes unpriced or in
			// another currency, and sometimes a run's settled total.
			run := open[rng.IntN(len(open))]
			sp := &Spend{Micros: int64(rng.IntN(5000)), ModelCalls: 1, Tokens: int64(rng.IntN(900))}
			switch rng.IntN(5) {
			case 0:
				sp.Micros, sp.Unpriced = 0, 1
			case 1:
				sp.Micros, sp.Other = 0, []policy.Cost{{Micros: 70, Currency: "EUR"}}
			}
			kind, key := Spent, fmt.Sprintf("%s/a1/%d", run, rng.IntN(4))
			if rng.IntN(6) == 0 {
				kind, key = Settled, "run:"+run
				sp.Micros += 10_000
			}
			l.Append(Entry{Kind: kind, Tenant: "acme", Project: projects[i%3], Agent: agents[i%3], RunID: run, Key: key, Spend: sp, At: at})
		default:
			id := fmt.Sprintf("r%d", i)
			admit(l, "acme", projects[rng.IntN(3)], agents[rng.IntN(3)], id, at)
			open = append(open, id)
		}
	}

	rebuilt := Rebuild(l.Entries())
	scopes := []policy.Scope{all, launch, {Kind: policy.Project, Name: "Billing"}, lead, {Kind: policy.Agent, Name: "oncall"}}
	for _, scope := range scopes {
		for _, since := range []time.Time{t0, policy.PeriodStart(policy.Day, at), policy.PeriodStart(policy.Month, at), at} {
			live, replayed := l.Usage("acme", scope, since), rebuilt.Usage("acme", scope, since)
			if !reflect.DeepEqual(live, replayed) {
				t.Fatalf("%v since %s: live %+v, rebuilt %+v", scope, since.Format(time.RFC3339), live, replayed)
			}
		}
	}
	if len(rebuilt.Notices("acme", 0)) != len(l.Notices("acme", 0)) {
		t.Fatal("the rebuilt ledger lost warnings or refusals")
	}
}

func TestOldAdmissionsAreDropped(t *testing.T) {
	l := NewLedger()
	admit(l, "acme", "", "lead", "old", t0)
	admit(l, "acme", "", "lead", "new", t0.Add(keepAdmissions+time.Hour))
	if got := l.Usage("acme", all, time.Time{}); got.Runs != 1 {
		t.Fatalf("%d runs kept, want only the recent one", got.Runs)
	}
	if len(l.Entries()) != 1 {
		t.Fatalf("%d entries kept", len(l.Entries()))
	}
}

// A client's usage is the runs it raised: in flight and in the period, and
// nobody else's.
func TestUsageByClient(t *testing.T) {
	l := NewLedger()
	raise := func(client, id string) {
		l.Append(Entry{Kind: Admitted, Tenant: "acme", Agent: "lead", Client: client, RunID: id, At: t0})
	}
	raise("docs-app", "r1")
	raise("docs-app", "r2")
	raise("ops-app", "r3")
	raise("", "r4") // from the console
	l.Append(Entry{Kind: Admitted, Tenant: "globex", Agent: "lead", Client: "docs-app", RunID: "r9", At: t0})
	l.Append(Entry{Kind: Ended, Tenant: "acme", RunID: "r1", At: t0.Add(time.Minute)})

	docs := policy.Scope{Kind: policy.Client, Name: "docs-app"}
	if got := l.Usage("acme", docs, t0); got.Runs != 2 || got.Concurrent != 1 {
		t.Fatalf("docs-app: %+v, want 2 runs and 1 in flight", got)
	}
	if got := l.Usage("acme", policy.Scope{Kind: policy.Client, Name: "nobody"}, t0); got.Runs != 0 || got.Concurrent != 0 {
		t.Fatalf("a client that raised nothing: %+v", got)
	}
	if got := l.Usage("acme", all, t0); got.Runs != 4 {
		t.Fatalf("the tenant: %+v", got)
	}
	// Rebuilding from the entries gives the same answer.
	if got := Rebuild(l.Entries()).Usage("acme", docs, t0); got.Runs != 2 || got.Concurrent != 1 {
		t.Fatalf("rebuilt docs-app: %+v", got)
	}
}

func call(l *Ledger, tenant, run, key string, at time.Time, sp Spend) bool {
	return l.Append(Entry{Kind: Spent, Tenant: tenant, Project: "Launch", Agent: "lead", RunID: run, Key: key, Spend: &sp, At: at})
}

// A model call is counted once however often it is delivered, and a run's
// settled total adds only what its calls did not: a redelivered terminal
// event, or a reconciler pass after it, adds nothing.
func TestARunsSpendIsCountedOnceDespiteRedeliveryAndSettlement(t *testing.T) {
	l := NewLedger()
	one := Spend{Micros: 300, ModelCalls: 1, Tokens: 1000}
	if !call(l, "acme", "r1", "r1/a1/1", t0, one) || call(l, "acme", "r1", "r1/a1/1", t0, one) {
		t.Fatal("the first delivery of a call counts and the second does not")
	}
	call(l, "acme", "r1", "r1/a1/2", t0, one)
	// The run ended having made three calls: one of them was never seen.
	settle := func() (Spend, bool) {
		return l.Record(Entry{Kind: Settled, Tenant: "acme", Project: "Launch", Agent: "lead", RunID: "r1", Key: "run:r1",
			Spend: &Spend{Micros: 900, ModelCalls: 3, Tokens: 3000}, At: t0.Add(time.Minute)})
	}
	added, ok := settle()
	if !ok || added.Micros != 300 || added.ModelCalls != 1 || added.Tokens != 1000 {
		t.Fatalf("settling added %+v (%v), want the one call that was missed", added, ok)
	}
	if _, again := settle(); again {
		t.Fatal("a second settlement of the same run was counted")
	}
	got := l.Usage("acme", launch, t0)
	if got.Spend != 900 || got.ModelCalls != 3 || got.Tokens != 3000 {
		t.Fatalf("usage = %+v, want the run's three calls once", got)
	}
	if run := l.RunSpend("acme", "r1"); run.Micros != 900 {
		t.Fatalf("the run's own spend is %+v", run)
	}
	// Another tenant reusing the key is its own call, and is not seen here.
	if !call(l, "globex", "r1", "r1/a1/1", t0, one) {
		t.Fatal("another tenant's call with the same key was refused")
	}
	if got := l.Usage("acme", all, t0); got.Spend != 900 {
		t.Fatalf("another tenant's spend leaked: %+v", got)
	}
	if !reflect.DeepEqual(Rebuild(l.Entries()).Usage("acme", all, t0), l.Usage("acme", all, t0)) {
		t.Fatal("the rebuilt ledger counts spend differently")
	}
}

// A call with no price, or priced in another currency, is never counted as
// dollars: it is reported apart, and its tokens still count.
func TestUnpricedAndOtherCurrencyCallsAreNotDollars(t *testing.T) {
	l := NewLedger()
	call(l, "acme", "r1", "a", t0, Spend{Micros: 500, ModelCalls: 1, Tokens: 10})
	call(l, "acme", "r1", "b", t0, Spend{ModelCalls: 1, Tokens: 20, Unpriced: 1})
	call(l, "acme", "r1", "c", t0, Spend{ModelCalls: 1, Tokens: 30, Other: []policy.Cost{{Micros: 800, Currency: "EUR"}}})
	call(l, "acme", "r1", "d", t0, Spend{ModelCalls: 1, Tokens: 40, Other: []policy.Cost{{Micros: 200, Currency: "EUR"}}})
	got := l.Usage("acme", all, t0)
	if got.Spend != 500 || got.Tokens != 100 || got.ModelCalls != 4 || got.UnpricedCalls != 1 || !got.Unmetered() ||
		!reflect.DeepEqual(got.OtherCosts, []policy.Cost{{Micros: 1000, Currency: "EUR"}}) {
		t.Fatalf("usage = %+v", got)
	}
}
