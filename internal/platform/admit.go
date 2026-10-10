package platform

import (
	"fmt"

	"github.com/urmzd/opentag/pkg/envelope"
	"github.com/urmzd/opentag/pkg/runtime"

	"github.com/urmzd/open-agent-platform/pkg/alerts"
	"github.com/urmzd/open-agent-platform/pkg/call"
	"github.com/urmzd/open-agent-platform/pkg/policy"
	"github.com/urmzd/open-agent-platform/pkg/runs"
	"github.com/urmzd/open-agent-platform/pkg/usage"
)

// Denied reports a run a billing policy refused. It carries the decision, so a
// caller can say which limit, whose, and how much was left.
type Denied struct {
	Decision policy.Decision
}

func (e *Denied) Error() string { return "platform: " + e.Decision.Reason }

// admit decides whether a run may start, and counts it if so. It is called
// with p.admission held, which is what makes the check and the count one step:
// two runs cannot both fit under a limit that only one of them fits under.
//
// Every run passes through here, whatever raised it: a person, a schedule, an
// eval, or another agent. Only the platform's own housekeeping is exempt, since
// refusing it would leave a conversation unnamed for a reason nobody chose.
func (p *Platform) admit(tag envelope.Tag, edge call.Edge) error {
	if tag.Origin == runs.OriginSystem {
		return nil
	}
	// A run called by an agent belongs to its root's project.
	project := runs.ProjectOf(tag)
	if edge.Parent != "" {
		if root, err := p.Runs.Get(tag.Tenant, runtime.RunID(edge.Root)); err == nil {
			project = root.Project
		}
	}
	now := p.now()
	var applied []policy.Applied
	for _, pol := range p.Policies.For(tag.Tenant, project, tag.Agent) {
		since := policy.PeriodStart(pol.Period, now)
		applied = append(applied, policy.Applied{Policy: pol, Usage: p.Usage.Usage(tag.Tenant, pol.Scope, since)})
	}
	d := policy.Decide(applied, policy.Request{Lease: edge.Lease})
	entry := usage.Entry{Tenant: tag.Tenant, Project: project, Agent: tag.Agent, RunID: runtime.RunID(tag.ID), At: now}
	if d.Policy != nil {
		entry.Scope, entry.Version, entry.Limit, entry.Reason = &d.Policy.Scope, d.Policy.Version, d.Limit, d.Reason
	}

	switch d.Verdict {
	case policy.Deny, policy.NeedsApproval:
		entry.Kind = usage.Refused
		p.Usage.Append(entry)
		p.budgetAlert(tag.Tenant, d, alerts.Critical, "Runs are being refused: ")
		p.log.Warn("platform: run refused by policy", "tenant", tag.Tenant, "agent", tag.Agent, "project", project, "reason", d.Reason)
		return &Denied{Decision: d}
	case policy.Warn:
		warned := entry
		warned.Kind = usage.Warned
		p.Usage.Append(warned)
		p.budgetAlert(tag.Tenant, d, alerts.Warning, "")
	}
	entry.Kind, entry.Scope, entry.Version, entry.Limit, entry.Reason = usage.Admitted, nil, 0, "", ""
	p.Usage.Append(entry)
	return nil
}

// budgetAlert raises one alert per scope and limit while it stays open.
func (p *Platform) budgetAlert(tenant string, d policy.Decision, severity alerts.Severity, lead string) {
	// Nearing a limit and being refused at it are different things to tell
	// someone, so each has its own alert.
	key := fmt.Sprintf("budget: %s, %s, %s", d.Policy.Scope, d.Limit, d.Verdict)
	a := alerts.Alert{Kind: alerts.Budget, Severity: severity, Title: lead + d.Reason, At: p.now()}
	if d.Policy.Scope.Kind == policy.Agent {
		a.Agent = d.Policy.Scope.Name
	}
	if _, raised := p.Alerts.Raise(tenant, key, a); raised {
		p.log.Warn("platform: alert", "rule", key, "severity", severity, "title", a.Title)
	}
}
