package platform

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/urmzd/mandatum/pkg/envelope"

	"github.com/urmzd/collegium/pkg/alerts"
	"github.com/urmzd/collegium/pkg/auth"
	"github.com/urmzd/collegium/pkg/inbox"
	"github.com/urmzd/collegium/pkg/memory"
	"github.com/urmzd/collegium/pkg/policy"
	"github.com/urmzd/collegium/pkg/runs"
)

// The inbox is one state seen from two sides. An alert and the message about
// it are the same thing: acknowledging either settles both. So every change to
// a source that has a message goes through a method here, which keeps them in
// step, and nothing writes to the inbox store from anywhere else.
//
// Resolving a part is doing the thing it stands for. The right that is checked
// is the right to do that thing, not the right to read an inbox: approving
// memories needs agents:write, raising a limit needs billing:write.

// Inbox errors a caller can match.
var (
	// ErrAction reports an action a part does not take.
	ErrAction = errors.New("platform: no such action")
)

// Actions a part can take.
const (
	ActAcknowledge = "acknowledge"
	ActRetry       = "retry"
	ActKeep        = "keep"
	ActDiscard     = "discard"
	ActRaiseLimit  = "raise_limit"
)

// Resolution is what a person decided about a part.
type Resolution struct {
	Action string
	// IDs are the proposed memories to keep.
	IDs  []string
	Note string
	// Limits are the new numbers when raising a limit.
	Limits inbox.Limits
}

func alertKey(id string) string { return "alert:" + id }
func runKey(id string) string   { return "run:" + id }

func severityOf(s alerts.Severity) inbox.Severity {
	switch s {
	case alerts.Critical:
		return inbox.Critical
	case alerts.Warning:
		return inbox.Warning
	}
	return inbox.Info
}

// reads is the right needed to see a kind of message.
func reads(k inbox.Kind) auth.Permission {
	switch k {
	case inbox.KindMemory:
		return auth.AgentsRead
	case inbox.KindFailure, inbox.KindApproval, inbox.KindClarification, inbox.KindResult:
		return auth.RunsRead
	}
	return auth.AlertsRead
}

// writes is the right needed to set a kind of message aside.
func writes(k inbox.Kind) auth.Permission {
	switch k {
	case inbox.KindMemory:
		return auth.AgentsWrite
	case inbox.KindFailure, inbox.KindApproval, inbox.KindClarification, inbox.KindResult:
		return auth.RunsWrite
	}
	return auth.AlertsWrite
}

// needs is the right an action on a part takes.
func needs(t inbox.PartType, action string) (auth.Permission, bool) {
	switch {
	case t == inbox.PartAlert && action == ActAcknowledge:
		return auth.AlertsWrite, true
	case t == inbox.PartFailure && (action == ActRetry || action == ActAcknowledge):
		return auth.RunsWrite, true
	case t == inbox.PartMemory && (action == ActKeep || action == ActDiscard):
		return auth.AgentsWrite, true
	case t == inbox.PartBudget && action == ActRaiseLimit:
		return auth.BillingWrite, true
	case t == inbox.PartBudget && action == ActAcknowledge:
		return auth.AlertsWrite, true
	}
	return "", false
}

var offered = map[inbox.PartType][]string{
	inbox.PartAlert:   {ActAcknowledge},
	inbox.PartFailure: {ActRetry, ActAcknowledge},
	inbox.PartMemory:  {ActKeep, ActDiscard},
	inbox.PartBudget:  {ActRaiseLimit, ActAcknowledge},
}

// seenBy fills in what a reader may do with each part.
func seenBy(m inbox.Message, who auth.Principal) inbox.Message {
	for i := range m.Parts {
		p := &m.Parts[i]
		p.Actions = []string{}
		if p.State != inbox.PartOpen || m.State == inbox.Resolved || m.State == inbox.Dismissed {
			continue
		}
		for _, a := range offered[p.Type] {
			if a == ActRetry && (p.Failure == nil || !p.Failure.Retryable) {
				continue
			}
			if perm, ok := needs(p.Type, a); ok && who.Can(perm) {
				p.Actions = append(p.Actions, a)
			}
		}
	}
	return m
}

func (p *Platform) inboxQuery(who auth.Principal, q inbox.Query) inbox.Query {
	q.Reader, q.Now = who.Subject, p.now()
	q.Visible = func(k inbox.Kind) bool { return who.Can(reads(k)) }
	return q
}

// InboxSees reports whether a caller may see messages of a kind.
func InboxSees(who auth.Principal, k inbox.Kind) bool { return who.Can(reads(k)) }

// InboxPage returns one page of a person's inbox.
func (p *Platform) InboxPage(who auth.Principal, q inbox.Query) ([]inbox.Message, string, error) {
	msgs, next, err := p.Inbox.Page(who.Tenant, p.inboxQuery(who, q))
	for i := range msgs {
		msgs[i] = seenBy(msgs[i], who)
	}
	return msgs, next, err
}

// InboxCount returns how many messages are open for a person, and how many of
// those they have not read.
func (p *Platform) InboxCount(who auth.Principal) (open, unread int) {
	return p.Inbox.Count(who.Tenant, p.inboxQuery(who, inbox.Query{}))
}

// InboxGet returns one message as a person sees it.
func (p *Platform) InboxGet(who auth.Principal, id string) (inbox.Message, error) {
	m, err := p.Inbox.Get(who.Tenant, id, who.Subject)
	if err != nil {
		return inbox.Message{}, err
	}
	// What a caller may not see does not exist for them.
	if !who.Can(reads(m.Kind)) {
		return inbox.Message{}, fmt.Errorf("%w: message %q", inbox.ErrNotFound, id)
	}
	if m.State == inbox.Snoozed && !p.now().Before(m.SnoozedUntil) {
		m.State = inbox.Open
	}
	return seenBy(m, who), nil
}

// InboxRead marks a message read or unread for the caller alone.
func (p *Platform) InboxRead(who auth.Principal, id string, read bool) (inbox.Message, error) {
	if _, err := p.InboxGet(who, id); err != nil {
		return inbox.Message{}, err
	}
	if _, err := p.Inbox.Read(who.Tenant, id, who.Subject, read, p.now()); err != nil {
		return inbox.Message{}, err
	}
	return p.InboxGet(who, id)
}

// InboxSnooze hides a message from everyone until a time.
func (p *Platform) InboxSnooze(who auth.Principal, id string, until time.Time) (inbox.Message, error) {
	m, err := p.InboxGet(who, id)
	if err != nil {
		return inbox.Message{}, err
	}
	if !who.Can(writes(m.Kind)) {
		return inbox.Message{}, fmt.Errorf("%w: snoozing this needs %s", ErrForbidden, writes(m.Kind))
	}
	if _, _, err := p.Inbox.Snooze(who.Tenant, id, who.Subject, until, p.now()); err != nil {
		return inbox.Message{}, err
	}
	p.audit(who, m, "", "snooze", until.Format(time.RFC3339))
	return p.InboxGet(who, id)
}

// InboxDismiss sets a message aside. A message about an alert cannot be set
// aside while the alert stays open, so dismissing it acknowledges the alert.
func (p *Platform) InboxDismiss(who auth.Principal, id string) (inbox.Message, error) {
	m, err := p.InboxGet(who, id)
	if err != nil {
		return inbox.Message{}, err
	}
	if m.State == inbox.Resolved || m.State == inbox.Dismissed {
		return m, nil
	}
	if !who.Can(writes(m.Kind)) {
		return inbox.Message{}, fmt.Errorf("%w: dismissing this needs %s", ErrForbidden, writes(m.Kind))
	}
	if m.Source.Alert != "" {
		if _, err := p.AckAlert(who.Tenant, m.Source.Alert, who.Subject); err != nil {
			return inbox.Message{}, err
		}
	} else if _, _, err := p.Inbox.Dismiss(who.Tenant, id, who.Subject, p.now()); err != nil {
		return inbox.Message{}, err
	}
	p.audit(who, m, "", "dismiss", "")
	return p.InboxGet(who, id)
}

// InboxResolve carries out what a person decided about one part of a message,
// and records it. Resolving a part that is already resolved does nothing and
// returns the message with the first outcome.
func (p *Platform) InboxResolve(ctx context.Context, who auth.Principal, id, partID string, r Resolution) (inbox.Message, error) {
	m, err := p.InboxGet(who, id)
	if err != nil {
		return inbox.Message{}, err
	}
	i := slices.IndexFunc(m.Parts, func(x inbox.Part) bool { return x.ID == partID })
	if i < 0 {
		return inbox.Message{}, fmt.Errorf("%w: part %q", inbox.ErrNotFound, partID)
	}
	part := m.Parts[i]
	perm, known := needs(part.Type, r.Action)
	if !known {
		return inbox.Message{}, fmt.Errorf("%w: a %s part does not take %q", ErrAction, part.Type, r.Action)
	}
	// The right is checked before saying whether there is anything left to
	// do, so a caller without it learns nothing from trying.
	if !who.Can(perm) {
		return inbox.Message{}, fmt.Errorf("%w: %s needs %s", ErrForbidden, r.Action, perm)
	}
	if part.State != inbox.PartOpen || m.State == inbox.Resolved || m.State == inbox.Dismissed {
		return m, nil
	}

	note := r.Note
	switch {
	case r.Action == ActAcknowledge:
		// Settled below, with everything else the message holds.
	case part.Type == inbox.PartFailure:
		run, err := p.retry(ctx, who, part.Failure.Run)
		if err != nil {
			return inbox.Message{}, err
		}
		note = "Started again as " + run.ID
	case part.Type == inbox.PartMemory:
		kept, err := p.decideMemory(who.Tenant, *part.Memory, r)
		if err != nil {
			return inbox.Message{}, err
		}
		note = fmt.Sprintf("Kept %d of %d", kept, len(part.Memory.Notes))
	case part.Type == inbox.PartBudget:
		saved, err := p.raiseLimit(who, *part.Budget, r.Limits)
		if err != nil {
			return inbox.Message{}, err
		}
		note = fmt.Sprintf("Now version %d", saved.Version)
	}

	if _, _, err := p.Inbox.Resolve(who.Tenant, id, partID, inbox.Outcome{Action: r.Action, By: who.Subject, At: p.now(), Note: note}); err != nil {
		return inbox.Message{}, err
	}
	p.audit(who, m, partID, r.Action, note)
	// Dealing with any part of a message about an alert deals with the alert.
	if m.Source.Alert != "" {
		if _, err := p.AckAlert(who.Tenant, m.Source.Alert, who.Subject); err != nil {
			return inbox.Message{}, err
		}
	} else {
		p.settle(who.Tenant, id, who.Subject)
	}
	return p.InboxGet(who, id)
}

// audit writes one line for every decision made in the inbox. The inbox's own
// record holds the same facts; this puts them where an operator reads.
func (p *Platform) audit(who auth.Principal, m inbox.Message, part, action, note string) {
	p.log.Info("platform: inbox", "tenant", who.Tenant, "by", who.Subject, "message", m.ID, "kind", m.Kind,
		"part", part, "action", action, "note", note, "alert", m.Source.Alert, "run", m.Source.Run)
}

// settle closes whatever is still open in a message once it has been dealt with.
func (p *Platform) settle(tenant, id, by string) {
	m, err := p.Inbox.Get(tenant, id, by)
	if err != nil {
		return
	}
	for _, part := range m.Parts {
		if part.State == inbox.PartOpen {
			_, _, _ = p.Inbox.Resolve(tenant, id, part.ID, inbox.Outcome{Action: ActAcknowledge, By: by, At: p.now()})
		}
	}
}

// AckAlert acknowledges an alert and settles the message about it. It is the
// only way an alert is acknowledged, from the inbox or from anywhere else.
func (p *Platform) AckAlert(tenant, id, by string) (alerts.Alert, error) {
	a, err := p.Alerts.Ack(tenant, id, by, p.now())
	if err != nil {
		return alerts.Alert{}, err
	}
	if m, ok := p.Inbox.ByKey(tenant, alertKey(id)); ok {
		// The first to acknowledge is who the alert says, whoever asked now.
		p.settle(tenant, m.ID, a.AckedBy)
	}
	return a, nil
}

// inboxAlert raises the message about an alert. extra parts follow the alert's.
func (p *Platform) inboxAlert(tenant string, a alerts.Alert, extra ...inbox.Part) {
	kind, from := inbox.KindAlert, inbox.From{Kind: "rule", Name: a.Rule}
	if a.Kind == alerts.Budget {
		kind, from = inbox.KindBudget, inbox.From{Kind: "system", Name: "Limits"}
	}
	m := inbox.Message{
		Kind: kind, Subject: a.Title, From: from, Severity: severityOf(a.Severity),
		Source: inbox.Source{Alert: a.ID, Rule: a.Rule, Run: a.RunID, Agent: a.Agent},
		Parts: []inbox.Part{{Type: inbox.PartAlert, Alert: &inbox.AlertPart{
			ID: a.ID, Rule: a.Rule, Kind: string(a.Kind), Severity: string(a.Severity), Title: a.Title, Body: a.Body,
		}}},
	}
	if a.RunID != "" {
		if run, err := p.Runs.Get(tenant, a.RunID); err == nil {
			m.Source.Thread, m.Source.Project = run.Thread, run.Project
			if run.Status == runs.Failed {
				m.Parts = append(m.Parts, failurePart(run))
			}
		}
	}
	m.Parts = append(m.Parts, extra...)
	p.Inbox.Raise(tenant, alertKey(a.ID), m, a.At)
}

func retryable(run runs.Run) bool { return run.Origin == runs.OriginAPI && run.Parent == "" }

func failurePart(run runs.Run) inbox.Part {
	return inbox.Part{Type: inbox.PartFailure, Failure: &inbox.FailurePart{
		Run: run.ID, Agent: run.Agent, Error: run.Error, Retryable: retryable(run),
	}}
}

// inboxFailure raises the message about a failed run that no alert rule spoke
// for. A run a rule did catch is already in the inbox as that alert.
func (p *Platform) inboxFailure(run runs.Run) {
	if run.Status != runs.Failed || run.Origin == runs.OriginSystem || run.Origin == OriginEval {
		return
	}
	p.Inbox.Raise(run.Tenant, runKey(run.ID), inbox.Message{
		Kind: inbox.KindFailure, Subject: run.Agent + " failed", From: inbox.From{Kind: "agent", Name: run.Agent},
		Severity: inbox.Warning,
		Source:   inbox.Source{Run: run.ID, Thread: run.Thread, Agent: run.Agent, Project: run.Project},
		Parts:    []inbox.Part{failurePart(run)},
	}, run.UpdatedAt)
}

// inboxMemory raises the message asking which proposed memories to keep.
func (p *Platform) inboxMemory(run runs.Run, owner string, notes []memory.Note) {
	if len(notes) == 0 {
		return
	}
	part := inbox.MemoryPart{Owner: owner}
	about := owner
	if project, ok := memory.OwnerProject(owner); ok {
		part.Project, about = project, "the project "+project
	} else {
		part.Agent = owner
	}
	for _, n := range notes {
		part.Notes = append(part.Notes, inbox.NoteRef{ID: n.ID, Text: n.Text})
	}
	noun := "memories"
	if len(notes) == 1 {
		noun = "memory"
	}
	p.Inbox.Raise(run.Tenant, "memory:"+run.ID, inbox.Message{
		Kind: inbox.KindMemory, Subject: fmt.Sprintf("%d proposed %s for %s", len(notes), noun, about),
		From: inbox.From{Kind: "agent", Name: run.Agent}, Severity: inbox.Info,
		Source: inbox.Source{Run: run.ID, Agent: part.Agent, Project: part.Project},
		Parts:  []inbox.Part{{Type: inbox.PartMemory, Memory: &part}},
	}, p.now())
}

// retry asks a failed turn again, on the same branch it was first asked on.
func (p *Platform) retry(ctx context.Context, who auth.Principal, runID string) (runs.Run, error) {
	run, err := p.Runs.Get(who.Tenant, runID)
	if err != nil {
		return runs.Run{}, err
	}
	if !retryable(run) {
		return runs.Run{}, fmt.Errorf("%w: only a turn a person asked can be started again", ErrAction)
	}
	t := Turn{
		// One retry per failed run: asking twice joins the first.
		ID: "api-retry-" + run.ID, Agent: run.Agent, Text: run.Text, After: run.Prev,
		Actor: envelope.Actor{ID: who.Subject, Display: who.Name, Bot: who.Kind == auth.Machine},
	}
	if run.Prev == "" {
		t.Project = run.Project
	}
	return p.Converse(ctx, who.Tenant, t)
}

// decideMemory keeps the chosen proposals and discards the rest.
func (p *Platform) decideMemory(tenant string, part inbox.MemoryPart, r Resolution) (int, error) {
	keep := map[string]bool{}
	if r.Action == ActKeep {
		for _, id := range r.IDs {
			if !slices.ContainsFunc(part.Notes, func(n inbox.NoteRef) bool { return n.ID == id }) {
				return 0, fmt.Errorf("%w: %q is not one of this message's proposals", ErrAction, id)
			}
			keep[id] = true
		}
		if len(keep) == 0 {
			return 0, fmt.Errorf("%w: choose at least one to keep, or discard them", ErrAction)
		}
	}
	kept, err := p.Memory.Approve(tenant, part.Owner, r.IDs)
	if r.Action != ActKeep {
		kept, err = 0, nil
	}
	if err != nil {
		return 0, err
	}
	for _, n := range part.Notes {
		if !keep[n.ID] {
			// One that is already gone was dealt with somewhere else.
			_ = p.Memory.Remove(tenant, part.Owner, n.ID)
		}
	}
	return kept, nil
}

// raiseLimit saves new numbers for the policy a budget part is about.
func (p *Platform) raiseLimit(who auth.Principal, part inbox.BudgetPart, to inbox.Limits) (policy.Policy, error) {
	scope := policy.Scope{Kind: policy.Kind(part.ScopeKind), Name: part.ScopeName}
	all := p.Policies.List(who.Tenant)
	i := slices.IndexFunc(all, func(x policy.Policy) bool { return x.Scope == scope })
	if i < 0 {
		return policy.Policy{}, fmt.Errorf("%w: %s has no limits any more", ErrAction, scope)
	}
	pol := all[i]
	if to == (inbox.Limits{}) {
		return policy.Policy{}, fmt.Errorf("%w: say what the limit should be", ErrAction)
	}
	if to.MaxRuns > 0 {
		pol.MaxRuns = to.MaxRuns
	}
	if to.MaxConcurrent > 0 {
		pol.MaxConcurrent = to.MaxConcurrent
	}
	if to.SpendLimit > 0 {
		pol.SpendLimit = to.SpendLimit
	}
	if to.MaxTokens > 0 {
		pol.MaxTokens = to.MaxTokens
	}
	return p.Policies.Put(who.Tenant, pol, who.Subject, p.now())
}

// ApproveMemory keeps proposals, from anywhere but the inbox, and brings the
// inbox into step.
func (p *Platform) ApproveMemory(tenant, owner string, ids []string) (int, error) {
	n, err := p.Memory.Approve(tenant, owner, ids)
	p.syncMemory(tenant, owner)
	return n, err
}

// RemoveMemory deletes a note or a proposal, and brings the inbox into step.
func (p *Platform) RemoveMemory(tenant, owner, id string) error {
	err := p.Memory.Remove(tenant, owner, id)
	p.syncMemory(tenant, owner)
	return err
}

// syncMemory closes messages whose proposals have all been dealt with elsewhere.
func (p *Platform) syncMemory(tenant, owner string) {
	var pending []string
	for _, n := range p.Memory.List(tenant, owner) {
		if n.Status == memory.Pending {
			pending = append(pending, n.ID)
		}
	}
	for _, m := range p.Inbox.OpenOf(tenant, inbox.KindMemory) {
		for _, part := range m.Parts {
			if part.Memory == nil || part.Memory.Owner != owner || part.State != inbox.PartOpen {
				continue
			}
			if !slices.ContainsFunc(part.Memory.Notes, func(n inbox.NoteRef) bool { return slices.Contains(pending, n.ID) }) {
				_, _, _ = p.Inbox.Withdraw(tenant, m.ID, "Dealt with on the "+strings.TrimPrefix(owner, "project:")+" memory page", p.now())
			}
		}
	}
}
