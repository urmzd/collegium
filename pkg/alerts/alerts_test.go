package alerts

import (
	"errors"
	"testing"
	"time"

	"github.com/urmzd/open-agent-platform/pkg/runs"
)

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func ended(agent string, status runs.Status, took time.Duration) runs.Run {
	return runs.Run{ID: "r-" + agent, Tenant: "acme", Agent: agent, Status: status, Error: "boom", CreatedAt: t0, UpdatedAt: t0.Add(took)}
}

func TestNoRulesNoAlerts(t *testing.T) {
	e := NewEngine()
	if got := e.Evaluate(ended("lead", runs.Failed, time.Second), nil); len(got) != 0 {
		t.Fatalf("raised %v with no rules", got)
	}
}

func TestRunFailed(t *testing.T) {
	e := NewEngine()
	if err := e.AddRule("acme", Rule{Name: "any", Kind: RunFailed, Severity: Critical}); err != nil {
		t.Fatal(err)
	}
	if got := e.Evaluate(ended("lead", runs.Completed, time.Second), nil); len(got) != 0 {
		t.Fatalf("a completed run raised %v", got)
	}
	got := e.Evaluate(ended("lead", runs.Failed, time.Second), nil)
	if len(got) != 1 || got[0].Agent != "lead" || got[0].Body != "boom" || !got[0].Open() {
		t.Fatalf("got %+v", got)
	}
}

func TestRuleScopedToAnAgent(t *testing.T) {
	e := NewEngine()
	_ = e.AddRule("acme", Rule{Name: "lead-only", Kind: RunFailed, Agent: "lead", Severity: Warning})
	if got := e.Evaluate(ended("helper", runs.Failed, time.Second), nil); len(got) != 0 {
		t.Fatalf("raised for another agent: %v", got)
	}
}

func TestRulesBelongToOneTenant(t *testing.T) {
	e := NewEngine()
	_ = e.AddRule("globex", Rule{Name: "any", Kind: RunFailed, Severity: Critical})
	if got := e.Evaluate(ended("lead", runs.Failed, time.Second), nil); len(got) != 0 {
		t.Fatalf("another tenant's rule fired: %v", got)
	}
	if len(e.Alerts("globex")) != 0 || len(e.Rules("acme")) != 0 {
		t.Fatal("tenants share state")
	}
}

func TestSlowRun(t *testing.T) {
	e := NewEngine()
	_ = e.AddRule("acme", Rule{Name: "slow", Kind: SlowRun, Threshold: 5, Severity: Warning})
	if got := e.Evaluate(ended("lead", runs.Completed, 4*time.Second), nil); len(got) != 0 {
		t.Fatalf("a fast run raised %v", got)
	}
	if got := e.Evaluate(ended("lead", runs.Completed, 6*time.Second), nil); len(got) != 1 {
		t.Fatalf("a slow run raised %v", got)
	}
}

func TestFailureRate(t *testing.T) {
	e := NewEngine()
	_ = e.AddRule("acme", Rule{Name: "rate", Kind: FailureRate, Threshold: 50, WindowSeconds: 600, MinRuns: 4, Severity: Critical})
	counts := func(total, failed int) Recent {
		return func(string, string, time.Time) (int, int) { return total, failed }
	}
	if got := e.Evaluate(ended("lead", runs.Failed, time.Second), counts(1, 1)); len(got) != 0 {
		t.Fatalf("one failure of one run paged: %v", got)
	}
	if got := e.Evaluate(ended("lead", runs.Failed, time.Second), counts(10, 2)); len(got) != 0 {
		t.Fatalf("20%% fired a 50%% rule: %v", got)
	}
	if got := e.Evaluate(ended("lead", runs.Failed, time.Second), counts(10, 6)); len(got) != 1 {
		t.Fatalf("60%% did not fire: %v", got)
	}
	// While that alert is unread, the rule stays quiet.
	if got := e.Evaluate(ended("lead", runs.Failed, time.Second), counts(10, 7)); len(got) != 0 {
		t.Fatalf("fired again while open: %v", got)
	}
	open := e.Alerts("acme")[0]
	if _, err := e.Ack("acme", open.ID, "ada", t0); err != nil {
		t.Fatal(err)
	}
	if got := e.Evaluate(ended("lead", runs.Failed, time.Second), counts(10, 7)); len(got) != 1 {
		t.Fatalf("did not fire after the ack: %v", got)
	}
}

func TestAck(t *testing.T) {
	e := NewEngine()
	_ = e.AddRule("acme", Rule{Name: "any", Kind: RunFailed, Severity: Critical})
	a := e.Evaluate(ended("lead", runs.Failed, time.Second), nil)[0]
	if _, err := e.Ack("globex", a.ID, "eve", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant acknowledged it: %v", err)
	}
	first, _ := e.Ack("acme", a.ID, "ada", t0)
	second, _ := e.Ack("acme", a.ID, "bob", t0.Add(time.Hour))
	if first.AckedBy != "ada" || second.AckedBy != "ada" {
		t.Fatalf("the first acknowledgement was overwritten: %+v", second)
	}
}

func TestInvalidRules(t *testing.T) {
	e := NewEngine()
	for name, r := range map[string]Rule{
		"no name":          {Kind: RunFailed, Severity: Warning},
		"no severity":      {Name: "x", Kind: RunFailed},
		"unknown kind":     {Name: "x", Kind: "vibes", Severity: Warning},
		"rate over 100":    {Name: "x", Kind: FailureRate, Threshold: 150, WindowSeconds: 60, MinRuns: 1, Severity: Warning},
		"rate, no window":  {Name: "x", Kind: FailureRate, Threshold: 50, MinRuns: 1, Severity: Warning},
		"rate, no minimum": {Name: "x", Kind: FailureRate, Threshold: 50, WindowSeconds: 60, Severity: Warning},
		"slow, no limit":   {Name: "x", Kind: SlowRun, Severity: Warning},
	} {
		if err := e.AddRule("acme", r); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	_ = e.AddRule("acme", Rule{Name: "x", Kind: RunFailed, Severity: Warning})
	if err := e.AddRule("acme", Rule{Name: "x", Kind: RunFailed, Severity: Warning}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
}
