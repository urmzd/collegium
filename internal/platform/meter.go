package platform

import (
	"context"
	"encoding/json"
	"fmt"

	drun "github.com/urmzd/duraturo/pkg/run"
	"github.com/urmzd/mandatum/pkg/agentrt/payload"
	"github.com/urmzd/mandatum/pkg/envelope"
	"github.com/urmzd/mandatum/pkg/runtime"

	"github.com/urmzd/collegium/pkg/alerts"
	"github.com/urmzd/collegium/pkg/inbox"
	"github.com/urmzd/collegium/pkg/policy"
	"github.com/urmzd/collegium/pkg/runs"
	"github.com/urmzd/collegium/pkg/usage"
)

// What runs spend, and what happens when they spend too much.
//
// The runtime reports every model call on the run's stream as usage.call, the
// moment saige reports it, with a key no other call shares. The platform's
// observer sees every event, so it sees these: each one is counted in the
// usage ledger under the run's place (its root's project and client, its own
// agent, the workspace), once by its key, and then the run and its scopes are
// judged.
//
//   - A scope at its warning threshold raises one budget alert and one inbox
//     message, which stay until someone settles them.
//   - A scope at its spend or token limit stops the run that just spent,
//     through the runtime, with a reason naming the limit and the policy
//     version. Every other run in the scope is stopped at its own next call.
//   - A run with a lease is stopped once it has spent the lease.
//
// When a run ends, its own total as the runtime recorded it is settled in the
// ledger, which adds only calls whose events were missed, and the run's
// spend is settled with whatever called it.
//
// Money is held against a limit only when it is in US dollars. A call saige
// could not price, and money in any other currency, is never counted as zero
// spend: it is counted apart (unpriced calls, other costs), every scope with
// a spend limit it falls under gets one alert saying its spend is a lower
// bound, and its tokens count against MaxTokens, which is the limit that
// holds whatever the price.

// spendOf is what one model call used, in the ledger's terms.
func spendOf(c payload.UsageCall) usage.Spend {
	s := usage.Spend{ModelCalls: 1, Tokens: int64(c.PromptTokens + c.CompletionTokens)}
	switch {
	case c.Cost != nil && c.Cost.Currency == usage.USD:
		s.Micros = c.Cost.Micros
	case c.Cost != nil:
		s.Other = []policy.Cost{{Micros: c.Cost.Micros, Currency: c.Cost.Currency}}
	case !c.ResponseCacheHit:
		s.Unpriced = 1
	}
	return s
}

// totalOf is what a sum of calls used, in the ledger's terms.
func totalOf(u payload.Usage) usage.Spend {
	s := usage.Spend{ModelCalls: u.ModelCalls, Tokens: int64(u.PromptTokens + u.CompletionTokens), Unpriced: u.UnpricedCalls}
	costs := u.OtherCosts
	if u.Cost != nil {
		costs = append([]payload.Cost{*u.Cost}, costs...)
	}
	for _, c := range costs {
		if c.Currency == usage.USD {
			s.Micros += c.Micros
			continue
		}
		s.Other = append(s.Other, policy.Cost{Micros: c.Micros, Currency: c.Currency})
	}
	return s
}

// money writes micro-USD as dollars and cents, without a float.
func money(micros int64) string {
	return fmt.Sprintf("$%d.%02d", micros/1_000_000, micros%1_000_000/10_000)
}

// observeUsage handles a usage event the observer saw.
func (p *Platform) observeUsage(ctx context.Context, e envelope.Event) {
	run, ok := p.Runs.ObserveUsage(e)
	if !ok || e.Kind != envelope.KindUsageCall {
		return
	}
	c, err := payload.Decode[payload.UsageCall](e)
	if err != nil {
		p.log.Warn("platform: a usage report could not be read", "run", e.RunID, "error", err)
		return
	}
	key := c.AccountingID
	if key == "" {
		// A call with no key of its own is told apart by where it was
		// published, which a redelivery repeats.
		key = fmt.Sprintf("%s#%d", e.RunID, e.Seq)
	}
	added, counted := p.Usage.Record(usage.Entry{
		Kind: usage.Spent, Key: key, Tenant: run.Tenant, Project: run.Project, Agent: run.Agent, Client: run.Client,
		RunID: run.ID, At: e.At, Spend: new(spendOf(c)),
	})
	if !counted {
		return
	}
	if added.Micros > 0 {
		run, _ = p.Runs.Spend(run.ID, added.Micros)
	}
	p.enforce(ctx, run, added)
}

// enforce judges a run that has just spent, and the scopes it spends in.
func (p *Platform) enforce(ctx context.Context, run runs.Run, call usage.Spend) {
	// Housekeeping is never refused, so it is not stopped either. What it
	// spends is still counted.
	if run.Origin == runs.OriginSystem || run.Status.Terminal() {
		return
	}
	now := p.now()
	applied := p.applied(run.Tenant, run.Project, run.Agent, run.Client, now)
	if call.Unpriced > 0 || len(call.Other) > 0 {
		for _, a := range applied {
			if a.Policy.SpendLimit > 0 {
				p.unmeteredAlert(run.Tenant, a)
			}
		}
	}
	if run.Stopping {
		return
	}
	if run.Lease > 0 && run.Spent >= run.Lease {
		p.halt(ctx, run, fmt.Sprintf("Stopped at its budget: it was given %s and has spent %s", money(run.Lease), money(run.Spent)))
		return
	}
	d := policy.Exceeded(applied)
	entry := usage.Entry{Tenant: run.Tenant, Project: run.Project, Agent: run.Agent, Client: run.Client, RunID: run.ID, At: now}
	if d.Policy != nil {
		entry.Scope, entry.Version, entry.Limit, entry.Reason = &d.Policy.Scope, d.Policy.Version, d.Limit, d.Reason
	}
	switch d.Verdict {
	case policy.Deny:
		entry.Kind = usage.Halted
		p.Usage.Append(entry)
		p.budgetAlert(run.Tenant, d, alerts.Critical, "Runs are being stopped: ", "stopped")
		p.halt(ctx, run, fmt.Sprintf("Stopped at a limit: %s (%s policy, version %d)", d.Reason, d.Policy.Scope, d.Policy.Version))
	case policy.Warn:
		// One warning per scope and limit while it is open, however many
		// calls cross the threshold.
		if p.budgetAlert(run.Tenant, d, alerts.Warning, "", string(d.Verdict)) {
			entry.Kind = usage.Warned
			p.Usage.Append(entry)
		}
	}
}

// halt stops a run at a limit, through the runtime, once. It does not wait:
// a stop reads and writes the run ledger, and the observer that called it
// must keep reading events, the run's own ending among them.
func (p *Platform) halt(ctx context.Context, run runs.Run, reason string) {
	if _, already := p.halting.LoadOrStore(run.ID, true); already {
		return
	}
	p.log.Warn("platform: run stopped at a limit", "tenant", run.Tenant, "run", run.ID, "agent", run.Agent, "reason", reason)
	go func() {
		if err := p.requestStop(ctx, run.Tenant, run.ID, reason, StoppedByLimit); err != nil {
			p.log.Error("platform: a run at its limit could not be stopped", "tenant", run.Tenant, "run", run.ID, "error", err)
		}
	}()
}

// StoppedByLimit is who a run stopped at a limit was stopped by.
const StoppedByLimit = "limits"

// unmeteredAlert says, once per scope while it is open, that some of what the
// scope spent has no price in US dollars, so its spend limit cannot count it.
func (p *Platform) unmeteredAlert(tenant string, a policy.Applied) {
	pol := a.Policy
	key := fmt.Sprintf("budget: %s, spend, unpriced", pol.Scope)
	title := fmt.Sprintf("Spend for %s is a lower bound: some model calls have no price in US dollars", pol.Scope)
	body := fmt.Sprintf("%d calls this %s had no price, and %d other currencies were not converted. "+
		"Its spend limit counts only what was priced in US dollars. A token limit bounds every call.",
		a.Usage.UnpricedCalls, pol.Period, len(a.Usage.OtherCosts))
	al := alerts.Alert{Kind: alerts.Budget, Severity: alerts.Warning, Title: title, Body: body, At: p.now()}
	if pol.Scope.Kind == policy.Agent {
		al.Agent = pol.Scope.Name
	}
	if raised, ok := p.Alerts.Raise(tenant, key, al); ok {
		p.log.Warn("platform: alert", "rule", key, "severity", al.Severity, "title", title)
		p.inboxAlert(tenant, raised, inbox.Part{Type: inbox.PartText, State: inbox.PartInfo, Text: body})
	}
}

// settleUsage records what a run that has just ended used: the runtime's own
// figure on the run, its own calls settled in the usage ledger, and its spend
// settled with the run that called it. It is called once per run, from ended,
// and is safe to call again: the ledger settles a run once.
func (p *Platform) settleUsage(ctx context.Context, run runs.Run) {
	if u, ok := p.recordedUsage(ctx, run); ok {
		p.Runs.SetUsage(run.ID, u)
		// Only the run's own calls: the runs it called settle their own,
		// so each call in a tree is counted once.
		added, _ := p.Usage.Record(usage.Entry{
			Kind: usage.Settled, Key: "run:" + run.ID, Tenant: run.Tenant, Project: run.Project, Agent: run.Agent,
			Client: run.Client, RunID: run.ID, At: p.now(), Spend: new(totalOf(u.Own)),
		})
		if added.Micros > 0 {
			p.Runs.Spend(run.ID, added.Micros)
		}
	}
	p.halting.Delete(run.ID)
	if run.Parent != "" {
		if now, err := p.Runs.Get(run.Tenant, run.ID); err == nil {
			p.Runs.SettleCall(now)
		}
	}
}

// recordedUsage is the runtime's figure for a run that has ended: from its
// terminal event when that carried one, otherwise from the run ledger, its
// output for a run that succeeded there and its usage records for one that
// failed. It reports false when there is none, for a run that called no model.
func (p *Platform) recordedUsage(ctx context.Context, run runs.Run) (payload.RunUsage, bool) {
	if run.UsageRecorded() {
		return *run.Usage, true
	}
	rec, records, err := p.ledger.Load(ctx, run.ID)
	if err != nil {
		p.log.Warn("platform: a run's usage could not be read from the ledger", "run", run.ID, "error", err)
		return payload.RunUsage{}, false
	}
	if rec.Status == drun.StatusSucceeded {
		var out runtime.Output
		if json.Unmarshal(rec.Output, &out) == nil && out.Usage != nil {
			return *out.Usage, true
		}
	}
	return runtime.RecordedUsage(records)
}
