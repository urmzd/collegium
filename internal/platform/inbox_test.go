package platform_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/urmzd/mandatum/pkg/agentrt"
	"github.com/urmzd/mandatum/pkg/agentspec"
	saigetypes "github.com/urmzd/saige/agent/types"

	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/alerts"
	"github.com/urmzd/collegium/pkg/auth"
	"github.com/urmzd/collegium/pkg/inbox"
	"github.com/urmzd/collegium/pkg/memory"
	"github.com/urmzd/collegium/pkg/runs"
)

var (
	admin  = auth.Principal{Subject: "ada", Tenant: tenant, Kind: auth.User, Roles: []auth.Role{auth.Admin}}
	viewer = auth.Principal{Subject: "vi", Tenant: tenant, Kind: auth.User, Roles: []auth.Role{auth.Viewer}}
	// An operator may start runs and acknowledge alerts, not edit agents.
	operator = auth.Principal{Subject: "opal", Tenant: tenant, Kind: auth.User, Roles: []auth.Role{auth.Operator}}
)

// proposals runs one turn with a memory agent and waits for what it proposed
// to reach the inbox.
func proposals(t *testing.T) (*platform.Platform, inbox.Message) {
	t.Helper()
	p, err := platform.New(platform.Config{Workspace: t.TempDir(), OfflineHooks: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("platform shutdown: %v", err)
		}
	})
	for _, name := range []string{"lead", "scribe"} {
		spec := agentspec.AgentSpec{Name: name, Model: "offline", Provider: "offline", SystemPrompt: "x"}
		if _, err := p.Agents.Create(ctx, tenant, spec, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.SetSettings(ctx, tenant, platform.Settings{MemoryAgent: "scribe"}); err != nil {
		t.Fatal(err)
	}
	first, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-1", Agent: "lead", Text: "Plan the launch"})
	if err != nil {
		t.Fatal(err)
	}
	await(t, p, first.ID, runs.Completed)
	deadline := time.Now().Add(10 * time.Second)
	for {
		page, _, err := p.InboxPage(admin, inbox.Query{Kind: inbox.KindMemory})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 1 {
			return p, page[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no memory message reached the inbox: %+v", page)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// What the memory agent proposes is one message. Keeping some of it in the
// inbox approves those and discards the rest, and needs the right to edit the
// agent, not just to read the inbox.
func TestInboxDecidesProposedMemory(t *testing.T) {
	p, msg := proposals(t)
	ctx := t.Context()
	part := msg.Parts[0]
	if part.Type != inbox.PartMemory || part.Memory.Agent != "lead" || len(part.Memory.Notes) == 0 || len(part.Actions) != 2 {
		t.Fatalf("memory part = %+v", part)
	}
	keep := part.Memory.Notes[0].ID

	for name, who := range map[string]auth.Principal{"a viewer": viewer, "an operator": operator} {
		seen, err := p.InboxGet(who, msg.ID)
		if err != nil || len(seen.Parts[0].Actions) != 0 {
			t.Fatalf("%s is offered %v (%v)", name, seen.Parts[0].Actions, err)
		}
		if _, err := p.InboxResolve(ctx, who, msg.ID, part.ID, platform.Resolution{Action: platform.ActKeep, IDs: []string{keep}}); !errors.Is(err, platform.ErrForbidden) {
			t.Fatalf("%s keeping a memory = %v, want forbidden", name, err)
		}
		if _, err := p.InboxDismiss(who, msg.ID); !errors.Is(err, platform.ErrForbidden) {
			t.Fatalf("%s dismissing = %v, want forbidden", name, err)
		}
	}
	for name, r := range map[string]platform.Resolution{
		"keeping nothing":             {Action: platform.ActKeep},
		"keeping a note from nowhere": {Action: platform.ActKeep, IDs: []string{"n-404"}},
		"retrying a memory":           {Action: platform.ActRetry},
	} {
		if _, err := p.InboxResolve(ctx, admin, msg.ID, part.ID, r); !errors.Is(err, platform.ErrAction) {
			t.Fatalf("%s = %v, want a refused action", name, err)
		}
	}
	for _, n := range p.Memory.List(tenant, "lead") {
		if n.Status != memory.Pending {
			t.Fatalf("a refused attempt changed a note: %+v", n)
		}
	}

	done, err := p.InboxResolve(ctx, admin, msg.ID, part.ID, platform.Resolution{Action: platform.ActKeep, IDs: []string{keep}})
	if err != nil || done.State != inbox.Resolved || done.Parts[0].Outcome.By != "ada" || len(done.Parts[0].Actions) != 0 {
		t.Fatalf("keep = %+v, %v", done, err)
	}
	notes := p.Memory.List(tenant, "lead")
	if len(notes) != 1 || notes[0].ID != keep || notes[0].Status != memory.Approved {
		t.Fatalf("after keeping one, the agent holds %+v", notes)
	}

	// A second decision does nothing and answers with the first.
	again, err := p.InboxResolve(ctx, admin, msg.ID, part.ID, platform.Resolution{Action: platform.ActDiscard})
	if err != nil || again.Parts[0].Outcome.Action != platform.ActKeep {
		t.Fatalf("a second decision = %+v, %v", again.Parts[0].Outcome, err)
	}
	if got := p.Memory.List(tenant, "lead"); len(got) != 1 {
		t.Fatalf("a second decision changed the notes: %+v", got)
	}
}

// Proposals dealt with on the agent's own page close their message.
func TestMemoryDecidedElsewhereClosesItsMessage(t *testing.T) {
	p, msg := proposals(t)
	notes := msg.Parts[0].Memory.Notes
	if _, err := p.ApproveMemory(tenant, "lead", []string{notes[0].ID}); err != nil {
		t.Fatal(err)
	}
	if len(notes) > 1 {
		if got, _ := p.InboxGet(admin, msg.ID); got.State != inbox.Open {
			t.Fatalf("the message closed with proposals still waiting: %s", got.State)
		}
		for _, n := range notes[1:] {
			if err := p.RemoveMemory(tenant, "lead", n.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := p.InboxGet(admin, msg.ID)
	if err != nil || got.State != inbox.Dismissed {
		t.Fatalf("after every proposal was dealt with the message is %s (%v)", got.State, err)
	}
	if open, _ := p.InboxCount(admin); open != 0 {
		t.Fatalf("%d still open", open)
	}
}

// A failed run is one message whether or not a rule was watching for it, and
// an alert and its message are settled together from either side.
// refusing is a model that fails every call at once.
type refusing struct{ saigetypes.Provider }

func (refusing) Stream(context.Context, saigetypes.Request) (<-chan saigetypes.Delta, error) {
	return nil, errors.New("the model is unavailable")
}

func TestInboxHoldsFailures(t *testing.T) {
	// A run fails only after every attempt has. A real provider that cannot
	// be reached is retried inside each attempt as well, which made three
	// failing runs take seconds each and, under load, longer than the test
	// waited. The test is about the inbox: the model refuses at once, and
	// the attempts are not spaced out.
	p := startWith(t, platform.Config{
		Backoff: func(int) time.Duration { return time.Millisecond },
		AgentOptions: []agentrt.Option{agentrt.WithProvider(agentrt.ProviderOffline, func(agentrt.Spec) (saigetypes.Provider, error) {
			return refusing{}, nil
		})},
	})
	ctx := t.Context()
	broken := agentspec.AgentSpec{Name: "broken", Model: "offline", Provider: "offline", SystemPrompt: "x"}
	if _, err := p.Agents.Create(ctx, tenant, broken, "test"); err != nil {
		t.Fatal(err)
	}
	fail := func(id string) runs.Run {
		t.Helper()
		r, err := p.Invoke(ctx, tag(id, "broken"))
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(60 * time.Second)
		for {
			got, _ := p.Runs.Get(tenant, r.ID)
			if got.Status == runs.Failed {
				return got
			}
			if time.Now().After(deadline) {
				t.Fatalf("run is still %q", got.Status)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// With no rule watching, the failure speaks for itself.
	bare := fail("run-1")
	page, _, _ := p.InboxPage(admin, inbox.Query{})
	if len(page) != 1 || page[0].Kind != inbox.KindFailure || page[0].Source.Run != bare.ID || page[0].Parts[0].Failure.Error == "" {
		t.Fatalf("inbox = %+v", page)
	}
	if _, err := p.InboxResolve(ctx, viewer, page[0].ID, "p1", platform.Resolution{Action: platform.ActAcknowledge}); !errors.Is(err, platform.ErrForbidden) {
		t.Fatalf("a viewer acknowledging a failure = %v", err)
	}
	if got, err := p.InboxResolve(ctx, operator, page[0].ID, "p1", platform.Resolution{Action: platform.ActAcknowledge}); err != nil || got.State != inbox.Resolved {
		t.Fatalf("acknowledge = %s, %v", got.State, err)
	}

	// With a rule, the alert is the message and the failure is a part of it.
	if err := p.Alerts.AddRule(tenant, alerts.Rule{Name: "any", Kind: alerts.RunFailed, Severity: alerts.Critical}); err != nil {
		t.Fatal(err)
	}
	fail("run-2")
	page, _, _ = p.InboxPage(admin, inbox.Query{})
	if len(page) != 1 || page[0].Kind != inbox.KindAlert || len(page[0].Parts) != 2 || page[0].From.Name != "any" {
		t.Fatalf("with a rule the inbox is %+v", page)
	}
	raised := p.Alerts.Alerts(tenant)
	if len(raised) != 1 || !raised[0].Open() {
		t.Fatalf("alerts = %+v", raised)
	}
	// Acknowledged from the alert's side: the message is resolved, by the
	// same person, and the alert is acknowledged once.
	if _, err := p.AckAlert(tenant, raised[0].ID, "opal"); err != nil {
		t.Fatal(err)
	}
	got, _ := p.InboxGet(admin, page[0].ID)
	if got.State != inbox.Resolved || got.Parts[0].Outcome.By != "opal" || got.Parts[1].Outcome.By != "opal" {
		t.Fatalf("after the alert was acknowledged its message is %+v", got)
	}

	// And from the inbox's side: dismissing the message acknowledges the alert.
	fail("run-3")
	page, _, _ = p.InboxPage(admin, inbox.Query{})
	if len(page) != 1 {
		t.Fatalf("inbox = %+v", page)
	}
	if got, err := p.InboxDismiss(operator, page[0].ID); err != nil || got.State != inbox.Resolved {
		t.Fatalf("dismiss = %s, %v", got.State, err)
	}
	for _, a := range p.Alerts.Alerts(tenant) {
		if a.Open() {
			t.Fatalf("an alert is open with its message settled: %+v", a)
		}
		if a.RunID == "run-3" && a.AckedBy != "opal" {
			t.Fatalf("the alert was acknowledged by %q", a.AckedBy)
		}
	}
	if open, _ := p.InboxCount(admin); open != 0 {
		t.Fatalf("%d still open", open)
	}
}
