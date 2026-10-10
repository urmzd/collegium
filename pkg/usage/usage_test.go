package usage

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/urmzd/open-agent-platform/pkg/policy"
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
	if got := l.Usage("initech", all, t0); got != (policy.Usage{}) {
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
			if live != replayed {
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
