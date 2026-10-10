// Package alerts turns what runs do into things a person should look at.
//
// A rule is a condition on a run that has just ended. When it holds, the engine
// raises an alert, which stays open until someone acknowledges it. A tenant
// with no rules gets no alerts.
//
// Rules and alerts are in memory and are lost on restart.
package alerts

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/urmzd/open-agent-platform/pkg/evals"
	"github.com/urmzd/open-agent-platform/pkg/runs"
)

// Errors.
var (
	ErrInvalid  = errors.New("alerts: invalid")
	ErrExists   = errors.New("alerts: rule already exists")
	ErrNotFound = errors.New("alerts: not found")
)

// maxAlerts bounds what a tenant keeps. The oldest are dropped first.
const maxAlerts = 1000

// Kind is the condition a rule checks.
type Kind string

const (
	// RunFailed fires once for every run that fails.
	RunFailed Kind = "run_failed"
	// FailureRate fires when the share of failed runs in a window passes a
	// threshold. It stays quiet while its last alert is still open.
	FailureRate Kind = "failure_rate"
	// SlowRun fires when a run took longer than a threshold to end.
	SlowRun Kind = "slow_run"
	// EvalBelow fires when an eval run's pass rate ends under a threshold.
	EvalBelow Kind = "eval_below"
)

// Severity ranks an alert.
type Severity string

const (
	Warning  Severity = "warning"
	Critical Severity = "critical"
)

// Rule is one condition, scoped to an agent or to every agent.
type Rule struct {
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	// Agent limits the rule to one agent. Empty means every agent.
	Agent    string   `json:"agent,omitempty"`
	Severity Severity `json:"severity"`
	// Threshold is a percentage for FailureRate and EvalBelow, and seconds
	// for SlowRun.
	Threshold float64 `json:"threshold,omitempty"`
	// WindowSeconds is how far back FailureRate looks.
	WindowSeconds int `json:"window_seconds,omitempty"`
	// MinRuns is how many runs FailureRate needs before it can fire, so one
	// failure out of one run is not a 100% failure rate worth paging on.
	MinRuns   int    `json:"min_runs,omitempty"`
	CreatedBy string `json:"created_by,omitempty"`
}

func (r Rule) validate() error {
	switch {
	case strings.TrimSpace(r.Name) == "":
		return fmt.Errorf("%w: a rule needs a name", ErrInvalid)
	case r.Severity != Warning && r.Severity != Critical:
		return fmt.Errorf("%w: severity must be warning or critical", ErrInvalid)
	}
	switch r.Kind {
	case RunFailed:
	case FailureRate:
		if r.Threshold <= 0 || r.Threshold > 100 {
			return fmt.Errorf("%w: a failure rate threshold is a percentage from 1 to 100", ErrInvalid)
		}
		if r.WindowSeconds <= 0 {
			return fmt.Errorf("%w: a failure rate rule needs a window", ErrInvalid)
		}
		if r.MinRuns < 1 {
			return fmt.Errorf("%w: a failure rate rule needs a minimum number of runs", ErrInvalid)
		}
	case SlowRun:
		if r.Threshold <= 0 {
			return fmt.Errorf("%w: a slow run threshold is a number of seconds above zero", ErrInvalid)
		}
	case EvalBelow:
		if r.Threshold <= 0 || r.Threshold > 100 {
			return fmt.Errorf("%w: an eval threshold is a pass rate from 1 to 100", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unknown rule kind %q", ErrInvalid, r.Kind)
	}
	return nil
}

// Alert is one firing of a rule.
type Alert struct {
	ID       string   `json:"id"`
	Rule     string   `json:"rule"`
	Kind     Kind     `json:"kind"`
	Severity Severity `json:"severity"`
	Agent    string   `json:"agent,omitempty"`
	RunID    string   `json:"run_id,omitempty"`
	// Suite names the eval suite behind an EvalBelow alert.
	Suite string    `json:"suite,omitempty"`
	Title string    `json:"title"`
	Body  string    `json:"body,omitempty"`
	At    time.Time `json:"at"`
	// AckedBy and AckedAt are set once a person has seen it.
	AckedBy string    `json:"acked_by,omitempty"`
	AckedAt time.Time `json:"acked_at,omitzero"`
}

// Open reports whether nobody has acknowledged the alert.
func (a Alert) Open() bool { return a.AckedBy == "" }

type tenant struct {
	rules  []Rule
	alerts []Alert
	next   int
}

// Engine holds every tenant's rules and alerts.
type Engine struct {
	mu      sync.Mutex
	tenants map[string]*tenant
}

// NewEngine returns an engine with no rules.
func NewEngine() *Engine { return &Engine{tenants: map[string]*tenant{}} }

func (e *Engine) of(name string) *tenant {
	t, ok := e.tenants[name]
	if !ok {
		t = &tenant{}
		e.tenants[name] = t
	}
	return t
}

// AddRule stores a rule.
func (e *Engine) AddRule(tenantName string, r Rule) error {
	if err := r.validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	t := e.of(tenantName)
	if slices.ContainsFunc(t.rules, func(have Rule) bool { return have.Name == r.Name }) {
		return fmt.Errorf("%w: %q", ErrExists, r.Name)
	}
	t.rules = append(t.rules, r)
	return nil
}

// RemoveRule deletes a rule. Alerts it already raised stay.
func (e *Engine) RemoveRule(tenantName, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	t := e.of(tenantName)
	i := slices.IndexFunc(t.rules, func(r Rule) bool { return r.Name == name })
	if i < 0 {
		return fmt.Errorf("%w: rule %q", ErrNotFound, name)
	}
	t.rules = slices.Delete(t.rules, i, i+1)
	return nil
}

// Rules returns a tenant's rules.
func (e *Engine) Rules(tenantName string) []Rule {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.of(tenantName).rules)
}

// Alerts returns a tenant's alerts, newest first.
func (e *Engine) Alerts(tenantName string) []Alert {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := slices.Clone(e.of(tenantName).alerts)
	slices.Reverse(out)
	return out
}

// Ack marks an alert as seen by a person. Acknowledging twice keeps the first.
func (e *Engine) Ack(tenantName, id, by string, at time.Time) (Alert, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t := e.of(tenantName)
	for i := range t.alerts {
		if t.alerts[i].ID == id {
			if t.alerts[i].Open() {
				t.alerts[i].AckedBy, t.alerts[i].AckedAt = by, at
			}
			return t.alerts[i], nil
		}
	}
	return Alert{}, fmt.Errorf("%w: alert %q", ErrNotFound, id)
}

// Recent reports how many of an agent's runs started since a time, and how many
// of those failed. An empty agent means every agent.
type Recent func(tenant, agent string, since time.Time) (total, failed int)

// Evaluate checks a run that has just ended against its tenant's rules and
// returns the alerts it raised.
func (e *Engine) Evaluate(run runs.Run, recent Recent) []Alert {
	e.mu.Lock()
	defer e.mu.Unlock()
	t := e.of(run.Tenant)
	var raised []Alert
	raise := func(r Rule, title, body string) {
		t.next++
		a := Alert{
			ID: "al-" + strconv.Itoa(t.next), Rule: r.Name, Kind: r.Kind, Severity: r.Severity,
			Agent: run.Agent, RunID: run.ID, Title: title, Body: body, At: run.UpdatedAt,
		}
		t.alerts = append(t.alerts, a)
		if len(t.alerts) > maxAlerts {
			t.alerts = t.alerts[len(t.alerts)-maxAlerts:]
		}
		raised = append(raised, a)
	}

	for _, r := range t.rules {
		if r.Agent != "" && r.Agent != run.Agent {
			continue
		}
		switch r.Kind {
		case RunFailed:
			if run.Status == runs.Failed {
				raise(r, run.Agent+" failed", run.Error)
			}
		case SlowRun:
			took := run.UpdatedAt.Sub(run.CreatedAt)
			if took.Seconds() > r.Threshold {
				raise(r, fmt.Sprintf("%s took %s", run.Agent, took.Round(time.Millisecond)),
					fmt.Sprintf("The limit is %gs.", r.Threshold))
			}
		case FailureRate:
			if run.Status != runs.Failed || recent == nil {
				continue
			}
			// One open alert per rule and scope is enough. A second one
			// while the first is unread says nothing new.
			if slices.ContainsFunc(t.alerts, func(a Alert) bool {
				return a.Open() && a.Rule == r.Name && (r.Agent == "" || a.Agent == run.Agent)
			}) {
				continue
			}
			since := run.UpdatedAt.Add(-time.Duration(r.WindowSeconds) * time.Second)
			total, failed := recent(run.Tenant, r.Agent, since)
			if total < r.MinRuns {
				continue
			}
			if rate := 100 * float64(failed) / float64(total); rate >= r.Threshold {
				scope := "All agents"
				if r.Agent != "" {
					scope = r.Agent
				}
				raise(r, fmt.Sprintf("%s: %.0f%% of runs are failing", scope, rate),
					fmt.Sprintf("%d of %d runs failed in the last %s.", failed, total,
						(time.Duration(r.WindowSeconds)*time.Second).String()))
			}
		}
	}
	return raised
}

// EvaluateEval checks a finished eval run against its tenant's rules. A rule
// scoped to an agent applies to suites that test that agent, and a rule with no
// agent applies to every suite.
func (e *Engine) EvaluateEval(tenantName string, run evals.Run) []Alert {
	if run.Status == evals.Running {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	t := e.of(tenantName)
	var raised []Alert
	for _, r := range t.rules {
		if r.Kind != EvalBelow {
			continue
		}
		agent := ""
		if run.Target == evals.Agent {
			agent = run.Subject
		}
		if r.Agent != "" && r.Agent != agent {
			continue
		}
		// A run that could not be carried out scored nothing, which is
		// under any threshold.
		if run.Status != evals.Errored && run.PassRate >= r.Threshold {
			continue
		}
		t.next++
		a := Alert{
			ID: "al-" + strconv.Itoa(t.next), Rule: r.Name, Kind: r.Kind, Severity: r.Severity,
			Agent: agent, Suite: run.Suite, At: run.EndedAt,
			Title: fmt.Sprintf("Eval %s passed %.0f%% of its cases", run.Suite, run.PassRate),
			Body:  fmt.Sprintf("%d of %d cases passed. The rule expects at least %.0f%%.", run.Passed, run.Total, r.Threshold),
		}
		t.alerts = append(t.alerts, a)
		if len(t.alerts) > maxAlerts {
			t.alerts = t.alerts[len(t.alerts)-maxAlerts:]
		}
		raised = append(raised, a)
	}
	return raised
}
