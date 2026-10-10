package platform_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/urmzd/opentag/pkg/agentspec"
	"github.com/urmzd/opentag/pkg/connectors/cron"
	"github.com/urmzd/opentag/pkg/envelope"
	"github.com/urmzd/opentag/pkg/topic"

	"github.com/urmzd/open-agent-platform/internal/platform"
	"github.com/urmzd/open-agent-platform/pkg/alerts"
	"github.com/urmzd/open-agent-platform/pkg/call"
	"github.com/urmzd/open-agent-platform/pkg/runs"
)

const tenant = "acme"

// start runs a platform with two offline agents: lead may call helper, and
// helper may call no one.
func start(t *testing.T, lim call.Limits) *platform.Platform {
	t.Helper()
	p, err := platform.New(platform.Config{Workspace: t.TempDir(), Limits: lim})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("platform stopped with %v", err)
		}
	})

	for _, spec := range []agentspec.AgentSpec{
		{Name: "lead", Model: "offline", Provider: "offline", SystemPrompt: "Lead.", Access: agentspec.Access{Spawn: []string{"helper"}}},
		{Name: "helper", Model: "offline", Provider: "offline", SystemPrompt: "Help."},
	} {
		if _, err := p.Agents.Create(ctx, tenant, spec, "test"); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func tag(id, agent string) envelope.Tag {
	return envelope.Tag{ID: id, Tenant: tenant, Agent: agent, Origin: "api", Text: "hello"}
}

func await(t *testing.T, p *platform.Platform, runID string, want runs.Status) runs.Run {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, err := p.Runs.Get(tenant, runID)
		if err == nil && r.Status == want {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s: status %q (err %v), want %q", runID, r.Status, err, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestARunCompletesAndStreams(t *testing.T) {
	p := start(t, call.Limits{})
	ctx := t.Context()

	lead, err := topic.Agent("lead")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := p.Subscribe(ctx, tenant, envelope.Subscription{Topic: lead})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()

	r, err := p.Invoke(ctx, tag("run-1", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Root != r.ID || r.Depth != 0 || r.Parent != "" {
		t.Fatalf("a run raised from outside should be a root: %+v", r)
	}
	got := await(t, p, r.ID, runs.Completed)
	if got.Answer == "" {
		t.Fatal("completed run has no answer")
	}

	var sawText, sawDone bool
	for !sawDone {
		recvCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		e, err := stream.Recv(recvCtx)
		cancel()
		if err != nil {
			t.Fatalf("stream ended early: %v", err)
		}
		sawText = sawText || e.Kind == envelope.KindText
		sawDone = e.Kind == envelope.KindCompleted
	}
	if !sawText {
		t.Error("stream carried no text")
	}
}

func TestAnotherTenantSeesNothing(t *testing.T) {
	p := start(t, call.Limits{})
	ctx := t.Context()

	stream, err := p.Subscribe(ctx, "globex", envelope.Subscription{Topic: topic.All()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()

	r, err := p.Invoke(ctx, tag("run-1", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	await(t, p, r.ID, runs.Completed)

	if _, err := p.Runs.Get("globex", r.ID); !errors.Is(err, runs.ErrNotFound) {
		t.Fatalf("other tenant read the run: %v", err)
	}
	if got := p.Runs.List("globex", 0); len(got) != 0 {
		t.Fatalf("other tenant listed %d runs", len(got))
	}
	recvCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if e, err := stream.Recv(recvCtx); err == nil {
		t.Fatalf("other tenant received %s for run %s", e.Kind, e.RunID)
	}
}

func TestCallBuildsATree(t *testing.T) {
	p := start(t, call.Limits{MaxDepth: 2, MaxFanOut: 2})
	ctx := t.Context()

	// A parked run would be the natural caller. Until parking is wired, a run
	// that has not finished is found by calling before the worker gets to it,
	// so the root here is raised and called from in the same breath.
	root, err := p.Invoke(ctx, tag("run-1", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	req := call.Request{Key: "k1", Agent: "helper", Text: "help", Mode: call.Delegate}
	child, err := p.Call(ctx, tenant, root.ID, req)
	if errors.Is(err, call.ErrInvalid) {
		t.Skip("the root finished before the call was made")
	}
	if err != nil {
		t.Fatal(err)
	}
	if child.Parent != root.ID || child.Root != root.ID || child.Depth != 1 || child.Mode != call.Delegate {
		t.Fatalf("child edge is wrong: %+v", child)
	}
	if child.Origin != call.Origin || !child.Actor.Bot {
		t.Fatalf("child should be raised by the calling agent: %+v", child)
	}

	// The same call again joins the same run and is not counted twice.
	again, err := p.Call(ctx, tenant, root.ID, req)
	if err == nil {
		if again.ID != child.ID {
			t.Fatalf("replayed call started a second run: %s != %s", again.ID, child.ID)
		}
		if got, _ := p.Runs.Get(tenant, root.ID); got.Calls != 1 {
			t.Fatalf("calls = %d, want 1", got.Calls)
		}
	}

	await(t, p, child.ID, runs.Completed)
	tree, err := p.Runs.Tree(tenant, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != 2 || tree[0].ID != root.ID || tree[1].ID != child.ID {
		t.Fatalf("tree = %+v", tree)
	}
}

func TestCallIsRefused(t *testing.T) {
	ctx := t.Context()
	t.Run("target not in the spawn allowlist", func(t *testing.T) {
		p := start(t, call.Limits{MaxDepth: 2, MaxFanOut: 2})
		root, err := p.Invoke(ctx, tag("run-1", "helper"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Call(ctx, tenant, root.ID, call.Request{Key: "k", Agent: "lead", Mode: call.Delegate})
		if !errors.Is(err, platform.ErrForbidden) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("zero limits", func(t *testing.T) {
		p := start(t, call.Limits{})
		root, err := p.Invoke(ctx, tag("run-1", "lead"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Call(ctx, tenant, root.ID, call.Request{Key: "k", Agent: "helper", Mode: call.Delegate})
		if !errors.Is(err, call.ErrDepth) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("another tenant's run", func(t *testing.T) {
		p := start(t, call.Limits{MaxDepth: 2, MaxFanOut: 2})
		root, err := p.Invoke(ctx, tag("run-1", "lead"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Call(ctx, "globex", root.ID, call.Request{Key: "k", Agent: "helper", Mode: call.Delegate})
		if !errors.Is(err, runs.ErrNotFound) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestTriggerTagCannotClaimAgentOrigin(t *testing.T) {
	p := start(t, call.Limits{MaxDepth: 2, MaxFanOut: 2})
	forged := tag("run-1", "lead")
	forged.Origin = call.Origin
	if _, err := p.Invoke(t.Context(), forged); !errors.Is(err, call.ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
}

// A run whose every attempt fails must end as failed and raise its alert, even
// though opentag publishes no final event once the retries run out.
func TestARunThatExhaustsItsRetriesEnds(t *testing.T) {
	// Nothing listens here, so every attempt fails the same way.
	t.Setenv("OLLAMA_HOST", "http://127.0.0.1:1")
	p := start(t, call.Limits{})
	ctx := t.Context()
	broken := agentspec.AgentSpec{Name: "broken", Model: "none", Provider: "ollama", SystemPrompt: "x"}
	if _, err := p.Agents.Create(ctx, tenant, broken, "test"); err != nil {
		t.Fatal(err)
	}
	if err := p.Alerts.AddRule(tenant, alerts.Rule{Name: "any", Kind: alerts.RunFailed, Severity: alerts.Critical}); err != nil {
		t.Fatal(err)
	}
	r, err := p.Invoke(ctx, tag("run-1", "broken"))
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(60 * time.Second)
	for {
		got, _ := p.Runs.Get(tenant, r.ID)
		if got.Status == runs.Failed {
			if got.Error == "" {
				t.Fatal("a failed run carries no error")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run is still %q after its retries: it would look alive forever", got.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	raised := p.Alerts.Alerts(tenant)
	if len(raised) != 1 || raised[0].RunID != r.ID {
		t.Fatalf("alerts = %+v, want exactly one for the run", raised)
	}
}

// After a first turn, the title agent names the conversation, and the memory
// agent's answer becomes a note the answering agent carries forward.
func TestHousekeepingTitlesAndRemembers(t *testing.T) {
	p, err := platform.New(platform.Config{Workspace: t.TempDir(), OfflineHooks: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	for _, name := range []string{"lead", "scribe"} {
		spec := agentspec.AgentSpec{Name: name, Model: "offline", Provider: "offline", SystemPrompt: "x"}
		if _, err := p.Agents.Create(ctx, tenant, spec, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.SetSettings(ctx, tenant, platform.Settings{TitleAgent: "scribe", MemoryAgent: "scribe"}); err != nil {
		t.Fatal(err)
	}
	if err := p.SetSettings(ctx, tenant, platform.Settings{TitleAgent: "ghost"}); err == nil {
		t.Fatal("a title agent that does not exist was accepted")
	}

	first, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-1", Agent: "lead", Text: "Plan the launch"})
	if err != nil {
		t.Fatal(err)
	}
	await(t, p, first.ID, runs.Completed)

	deadline := time.Now().Add(10 * time.Second)
	for {
		var named bool
		for _, th := range p.Runs.Threads(tenant, 0) {
			named = named || (th.ID == first.Thread && th.Named && th.Title != "Plan the launch")
		}
		notes := p.Memory.List(tenant, "lead")
		if named && len(notes) > 0 {
			// What the memory agent wrote is a proposal, not yet a memory.
			if notes[0].Source != "consolidated" || notes[0].Status != "pending" || len(notes) > 3 {
				t.Fatalf("notes = %+v", notes)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("housekeeping did not finish: named=%v notes=%v", named, notes)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The housekeeping agent itself gets no title and no memory: a system run
	// must not start more system runs.
	if got := p.Memory.List(tenant, "scribe"); len(got) != 0 {
		t.Fatalf("the memory agent was given memory of its own housekeeping: %v", got)
	}
	system := 0
	for _, r := range p.Runs.List(tenant, 0) {
		if r.Origin == runs.OriginSystem {
			system++
		}
	}
	if system != 2 {
		t.Fatalf("%d system runs, want one title and one memory", system)
	}

	// Until a person approves it, the next turn is sent none of it.
	unapproved, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-2", Agent: "lead", Text: "And then?", After: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := unapproved.Tag().Text; strings.Contains(got, "What you remember") {
		t.Fatalf("an unapproved proposal reached the agent: %q", got)
	}
	var ids []string
	for _, n := range p.Memory.List(tenant, "lead") {
		ids = append(ids, n.ID)
	}
	if n, err := p.Memory.Approve(tenant, "lead", ids); err != nil || n == 0 {
		t.Fatalf("approved %d, %v", n, err)
	}
	second, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-3", Agent: "lead", Text: "And then?", After: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Tag().Text; !strings.Contains(got, "What you remember from earlier conversations:") {
		t.Fatalf("the prompt carries no memory: %q", got)
	}
	if second.Text != "And then?" {
		t.Fatalf("the record shows %q, want what the person typed", second.Text)
	}
}

// Housekeeping does not run on the offline provider unless a test asks for it.
func TestHousekeepingSkipsTheOfflineProvider(t *testing.T) {
	p := start(t, call.Limits{})
	ctx := t.Context()
	if err := p.SetSettings(ctx, tenant, platform.Settings{TitleAgent: "helper", MemoryAgent: "helper"}); err != nil {
		t.Fatal(err)
	}
	r, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-1", Agent: "lead", Text: "Plan the launch"})
	if err != nil {
		t.Fatal(err)
	}
	await(t, p, r.ID, runs.Completed)
	time.Sleep(300 * time.Millisecond)
	for _, run := range p.Runs.List(tenant, 0) {
		if run.Origin == runs.OriginSystem {
			t.Fatal("a housekeeping run started on the offline provider")
		}
	}
}

func TestSchedules(t *testing.T) {
	var s platform.Schedules
	every := cron.Schedule{Name: "nightly", Expression: "0 2 * * *", Agent: "lead", Text: "report", Tenant: tenant}
	if err := s.Add(every); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(every); !errors.Is(err, platform.ErrScheduleExists) {
		t.Fatalf("duplicate: got %v", err)
	}
	bad := every
	bad.Name, bad.Expression = "bad", "not cron"
	if err := s.Add(bad); err == nil {
		t.Fatal("an invalid expression was stored")
	}
	if got := s.List("globex"); len(got) != 0 {
		t.Fatalf("other tenant listed %d schedules", len(got))
	}
	if err := s.Remove("globex", "nightly"); !errors.Is(err, platform.ErrScheduleNotFound) {
		t.Fatalf("other tenant removed the schedule: %v", err)
	}
	if err := s.Remove(tenant, "nightly"); err != nil {
		t.Fatal(err)
	}
	if got := s.List(tenant); len(got) != 0 {
		t.Fatalf("still listed: %v", got)
	}
}
