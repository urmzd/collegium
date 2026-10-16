package platform_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/urmzd/mandatum/pkg/envelope"
	"github.com/urmzd/mandatum/pkg/runtime"
	"github.com/urmzd/saige/agent/agenttest"
	saigetypes "github.com/urmzd/saige/agent/types"

	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/auth"
	"github.com/urmzd/collegium/pkg/call"
	"github.com/urmzd/collegium/pkg/inbox"
	"github.com/urmzd/collegium/pkg/policy"
	"github.com/urmzd/collegium/pkg/runs"
	"github.com/urmzd/collegium/pkg/usage"
)

// within waits for a condition, failing the test after a few seconds.
func within(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never happened: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// said reports whether a run's stream carried text containing s.
func streamed(p *platform.Platform, runID, s string) bool {
	text := ""
	for _, e := range p.Journal.Events(tenant, []string{runID}) {
		if e.Kind == envelope.KindText {
			text += string(e.Payload)
		}
	}
	return strings.Contains(text, s)
}

// endings counts a run's terminal events and its places freed in the usage
// ledger.
func endings(p *platform.Platform, runID string) (events, freed int) {
	for _, e := range p.Journal.Events(tenant, []string{runID}) {
		if e.Kind == envelope.KindCompleted || e.Kind == envelope.KindStopped || (e.Kind == envelope.KindFailed && strings.Contains(string(e.Payload), `"terminal":true`)) {
			events++
		}
	}
	for _, en := range p.Usage.Entries() {
		if en.Kind == usage.Ended && en.RunID == runID {
			freed++
		}
	}
	return events, freed
}

// Stopping a run interrupts its model call: the provider's context is
// cancelled, not merely ignored. The run ends stopped with what it had said
// and why, frees its place once, and no housekeeping runs after it.
func TestStopInterruptsTheModelCallAndKeepsThePartialAnswer(t *testing.T) {
	c := &cast{
		says:  map[string][][]saigetypes.Delta{"lead": {say("A whole answer.")}, "scribe": {say("Noted.")}},
		talks: map[string]talk{"lead": {after: 0, text: "Half an ans"}},
	}
	p := stage(t, c, platform.Config{OfflineHooks: true})
	ctx := t.Context()
	if err := p.SetSettings(ctx, tenant, platform.Settings{TitleAgent: "scribe", MemoryAgent: "scribe"}); err != nil {
		t.Fatal(err)
	}

	first, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-1", Agent: "lead", Text: "Explain"})
	if err != nil {
		t.Fatal(err)
	}
	within(t, "the partial answer streamed", func() bool { return streamed(p, first.ID, "Half an ans") })

	asked, err := p.StopRun(ctx, admin, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Asked, not yet confirmed: it is stopping and is not reported stopped.
	if !asked.Stopping && asked.Status != runs.Stopped {
		t.Fatalf("after the request the run is %s, stopping %v", asked.Status, asked.Stopping)
	}
	within(t, "the model call was cancelled", func() bool { return c.interrupted("lead") })
	got := await(t, p, first.ID, runs.Stopped)
	if got.Answer != "Half an ans" || got.Cause != "Stopped by ada" || got.Stopping {
		t.Fatalf("stopped run = answer %q, cause %q, stopping %v", got.Answer, got.Cause, got.Stopping)
	}
	stoppedEvent := false
	for _, e := range p.Journal.Events(tenant, []string{first.ID}) {
		stoppedEvent = stoppedEvent || (e.Kind == envelope.KindStopped && strings.Contains(string(e.Payload), "Half an ans"))
	}
	if !stoppedEvent {
		t.Fatal("the run's stream has no lifecycle.stopped with its partial answer")
	}
	if events, freed := endings(p, first.ID); events != 1 || freed != 1 {
		t.Fatalf("%d terminal events and %d places freed, want one of each", events, freed)
	}
	if u := p.Usage.Usage(tenant, policy.Scope{}, time.Time{}); u.Concurrent != 0 {
		t.Fatalf("%d runs still hold a place", u.Concurrent)
	}

	// A stopped turn starts no housekeeping. A completed one does, which is
	// what makes the absence mean something.
	time.Sleep(200 * time.Millisecond)
	for _, r := range p.Runs.List(tenant, 50) {
		if r.Origin == runs.OriginSystem {
			t.Fatalf("housekeeping ran after a stopped turn: %+v", r)
		}
	}
	next, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-2", Agent: "lead", Text: "Again", After: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	await(t, p, next.ID, runs.Completed)
	within(t, "housekeeping ran after a completed turn", func() bool {
		for _, r := range p.Runs.List(tenant, 50) {
			if r.Origin == runs.OriginSystem {
				return true
			}
		}
		return false
	})
}

// A reply to a stopped turn is sent what that turn had said, marked as
// stopped, the way the runtime's own thread journal would record it.
func TestAReplyToAStoppedTurnCarriesWhatItHadSaid(t *testing.T) {
	c := &cast{
		says:  map[string][][]saigetypes.Delta{"lead": {say("Carrying on.")}},
		talks: map[string]talk{"lead": {after: 0, text: "The deploy failed because"}},
	}
	p := stage(t, c, platform.Config{})
	ctx := t.Context()
	first, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-1", Agent: "lead", Text: "Why did it fail?"})
	if err != nil {
		t.Fatal(err)
	}
	within(t, "the partial answer streamed", func() bool { return streamed(p, first.ID, "failed because") })
	if _, err := p.StopRun(ctx, admin, first.ID); err != nil {
		t.Fatal(err)
	}
	await(t, p, first.ID, runs.Stopped)

	next, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-2", Agent: "lead", Text: "Go on", After: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := await(t, p, next.ID, runs.Completed); got.Answer != "Carrying on." || got.Thread != first.Thread {
		t.Fatalf("reply = %+v", got)
	}
	sent := c.requests("lead")
	last := sent[len(sent)-1]
	if !strings.Contains(last, "The deploy failed because") || !strings.Contains(last, runtime.StopMarker) || strings.Contains(last, "Stopped by") {
		t.Fatalf("the reply's model was sent %q", last)
	}
}

// A stop and a completion racing each other end the run once, as one or the
// other, and a run that completes is never reported stopped first. Asking
// many times is the same as asking once.
func TestStopIsIdempotentAndSafeAgainstACompletion(t *testing.T) {
	const rounds = 40
	c := &cast{says: map[string][][]saigetypes.Delta{}, gates: map[string]chan struct{}{}}
	for i := range rounds {
		name := fmt.Sprintf("racer%d", i)
		c.says[name] = [][]saigetypes.Delta{say("Done.")}
		c.gates[name] = make(chan struct{})
	}
	p := stage(t, c, platform.Config{})
	ctx := t.Context()
	outcomes := map[runs.Status]int{}
	for i := range rounds {
		name := fmt.Sprintf("racer%d", i)
		r, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-" + name, Agent: name, Text: "Go"})
		if err != nil {
			t.Fatal(err)
		}
		await(t, p, r.ID, runs.Running)

		// Watch every status the run passes through.
		var seen []runs.Status
		var mu sync.Mutex
		watching, stopWatching := context.WithCancel(ctx)
		watched := make(chan struct{})
		go func() {
			defer close(watched)
			for watching.Err() == nil {
				got, _ := p.Runs.Get(tenant, r.ID)
				mu.Lock()
				if len(seen) == 0 || seen[len(seen)-1] != got.Status {
					seen = append(seen, got.Status)
				}
				mu.Unlock()
				time.Sleep(time.Millisecond)
			}
		}()

		stop := func() {
			if _, err := p.StopRun(ctx, admin, r.ID); err != nil {
				t.Error(err)
			}
		}
		var wg sync.WaitGroup
		switch i % 4 {
		case 0:
			stop()
			c.release(name)
		case 1:
			c.release(name)
			stop()
		case 2:
			// Let the answer get further, by a different amount each round,
			// so the request sometimes arrives as the turn is finishing.
			c.release(name)
			time.Sleep(time.Duration(i) * time.Millisecond)
			stop()
		default:
			wg.Go(func() { c.release(name) })
			for range 3 {
				wg.Go(stop)
			}
		}
		wg.Wait()
		var end runs.Run
		within(t, "the run ended", func() bool {
			end, _ = p.Runs.Get(tenant, r.ID)
			return end.Status.Terminal()
		})
		time.Sleep(30 * time.Millisecond)
		stopWatching()
		<-watched

		again, err := p.StopRun(ctx, admin, r.ID)
		if err != nil || again.Status != end.Status || again.Cause != end.Cause || again.Answer != end.Answer {
			t.Fatalf("round %d: a stop after the end changed the run: %+v, %v", i, again, err)
		}
		switch end.Status {
		case runs.Stopped:
			// Stopped means cut short, and the agent loop is the judge: a
			// turn that finished its answer before the request took effect
			// completes instead. A request that took effect while the model
			// was still streaming keeps what had arrived, here all or none
			// of a one-chunk script.
			if end.Cause != "Stopped by ada" || (end.Answer != "" && end.Answer != "Done.") {
				t.Fatalf("round %d: stopped = %+v", i, end)
			}
		case runs.Completed:
			if end.Answer != "Done." || end.Cause != "" || end.Stopping {
				t.Fatalf("round %d: completed = %+v", i, end)
			}
		default:
			t.Fatalf("round %d: the run ended %s", i, end.Status)
		}
		mu.Lock()
		for j, s := range seen {
			if s.Terminal() && j != len(seen)-1 {
				t.Fatalf("round %d: the run went %v", i, seen)
			}
		}
		mu.Unlock()
		if events, freed := endings(p, r.ID); events != 1 || freed != 1 {
			t.Fatalf("round %d: %d terminal events and %d places freed", i, events, freed)
		}
		outcomes[end.Status]++
	}
	t.Logf("outcomes over %d rounds: %v", rounds, outcomes)
	if outcomes[runs.Stopped] == 0 || outcomes[runs.Completed] == 0 {
		t.Fatalf("the race was not exercised both ways: %v", outcomes)
	}
	if u := p.Usage.Usage(tenant, policy.Scope{}, time.Time{}); u.Concurrent != 0 {
		t.Fatalf("%d runs still hold a place", u.Concurrent)
	}
}

// Another tenant naming a run's id finds nothing to stop: the request reads as
// not found and the run carries on untouched. Two tenants that reuse one tag
// id get two runs, and stopping one leaves the other alone.
func TestAStopForAnotherTenantsRunDoesNothing(t *testing.T) {
	c := &cast{
		says:  map[string][][]saigetypes.Delta{"lead": {say("Acme's answer.")}, "helper": {say("Globex's answer.")}},
		gates: map[string]chan struct{}{"lead": make(chan struct{})},
	}
	p := stage(t, c, platform.Config{})
	agent(t, p, "globex", "helper")
	ctx := t.Context()

	mine, err := p.Invoke(ctx, envelope.Tag{ID: "same-id", Tenant: tenant, Agent: "lead", Origin: "api", Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := p.Invoke(ctx, envelope.Tag{ID: "same-id", Tenant: "globex", Agent: "helper", Origin: "api", Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if mine.ID == theirs.ID || mine.ID == "same-id" || mine.ID != runtime.RunID(tenant, "same-id") || theirs.ID != runtime.RunID("globex", "same-id") {
		t.Fatalf("run ids: acme %q, globex %q", mine.ID, theirs.ID)
	}
	await(t, p, mine.ID, runs.Running)
	// globex's run has its own model, which is not held, so it answers.
	if got, _ := p.Runs.Get("globex", theirs.ID); !got.Status.Terminal() {
		deadline := time.Now().Add(10 * time.Second)
		for !got.Status.Terminal() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
			got, _ = p.Runs.Get("globex", theirs.ID)
		}
		if got.Status != runs.Completed {
			t.Fatalf("globex's run = %s", got.Status)
		}
	}

	globex := auth.Principal{Subject: "mallory", Tenant: "globex", Kind: auth.User, Roles: []auth.Role{auth.Admin}}
	if _, err := p.StopRun(ctx, globex, mine.ID); !errors.Is(err, runs.ErrNotFound) {
		t.Fatalf("another tenant's stop = %v, want not found", err)
	}
	if got, _ := p.Runs.Get(tenant, mine.ID); got.Status != runs.Running || got.Stopping {
		t.Fatalf("acme's run after globex's stop: %s, stopping %v", got.Status, got.Stopping)
	}
	c.release("lead")
	if got := await(t, p, mine.ID, runs.Completed); got.Answer != "Acme's answer." {
		t.Fatalf("acme's run = %+v", got)
	}
	if got, _ := p.Runs.Get("globex", theirs.ID); got.Status != runs.Completed || got.Answer != "Globex's answer." {
		t.Fatalf("globex's run = %s %q", got.Status, got.Answer)
	}
}

// Stopping a run asks the runtime to stop everything attached to it, three
// levels down, and each keeps what it had said. What it delegated keeps
// running and its answer reaches the inbox.
func TestStoppingCascadesThroughAttachedRunsAtDepth(t *testing.T) {
	c := &cast{
		says: map[string][][]saigetypes.Delta{
			"lead": {
				agenttest.ToolCallResponse("c1", platform.ToolDelegate, map[string]any{"agent": "worker", "text": "Write the long report"}),
				append(say("Lead began. "), tagging("mid", "Go deeper")...),
				say("unreachable"),
			},
			"mid":    {append(say("Mid began. "), tagging("leaf", "Deeper still")...), say("unreachable")},
			"leaf":   {},
			"worker": {say("The report is written.")},
		},
		spawn: map[string][]string{"lead": {"mid", "worker"}, "mid": {"leaf"}},
		tools: map[string][]string{"lead": {platform.ToolDelegate}},
		talks: map[string]talk{"leaf": {after: 0, text: "Leaf was half"}},
		gates: map[string]chan struct{}{"worker": make(chan struct{})},
	}
	p := stage(t, c, platform.Config{Limits: call.Limits{MaxDepth: 3, MaxFanOut: 4}, Concurrency: 6})
	ctx := t.Context()
	first, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-1", Agent: "lead", Text: "Start"})
	if err != nil {
		t.Fatal(err)
	}
	mid := called(t, p, first.ID, "mid", runs.Running)
	leaf := called(t, p, first.ID, "leaf", runs.Running)
	worker := called(t, p, first.ID, "worker", runs.Running)
	if leaf.Parent != mid.ID || leaf.Depth != 2 || leaf.Root != first.ID || worker.Lifecycle != runs.Delegated {
		t.Fatalf("leaf = %+v, worker = %+v", leaf, worker)
	}
	within(t, "the leaf is talking", func() bool { _, ok := c.talking.Load("leaf"); return ok })

	if _, err := p.StopRun(ctx, admin, first.ID); err != nil {
		t.Fatal(err)
	}
	// From the moment of the request, nothing in the tree can start a call.
	if _, err := p.Call(ctx, tenant, mid.ID, call.Request{Key: "k", Agent: "leaf", Text: "x", Mode: call.Subagent}); err == nil {
		t.Fatal("a run asked to stop made a call")
	}
	within(t, "the leaf's model call was cancelled", func() bool { return c.interrupted("leaf") })
	want := map[string]struct{ answer, cause string }{
		first.ID: {"Lead began. ", "Stopped by ada"},
		mid.ID:   {"Mid began. ", "The run it was attached to was stopped (lead, " + first.ID + ")"},
		leaf.ID:  {"Leaf was half", "The run it was attached to was stopped (mid, " + mid.ID + ")"},
	}
	for id, w := range want {
		r := await(t, p, id, runs.Stopped)
		if r.Answer != w.answer || r.Cause != w.cause {
			t.Errorf("%s: answer %q cause %q, want %q and %q", r.Agent, r.Answer, r.Cause, w.answer, w.cause)
		}
	}
	if r, _ := p.Runs.Get(tenant, worker.ID); r.Status != runs.Running || r.Stopping {
		t.Fatalf("the delegated run is %s (stopping %v) after its caller was stopped", r.Status, r.Stopping)
	}
	c.release("worker")
	if done := await(t, p, worker.ID, runs.Completed); done.Answer != "The report is written." {
		t.Fatalf("worker = %q", done.Answer)
	}
	within(t, "the delegated run's answer reached the inbox", func() bool {
		page, _, _ := p.InboxPage(admin, inbox.Query{Kind: inbox.KindResult})
		return len(page) == 1 && page[0].Source.Run == worker.ID
	})
	for _, id := range []string{first.ID, mid.ID, leaf.ID, worker.ID} {
		if events, freed := endings(p, id); events != 1 || freed != 1 {
			t.Fatalf("%s: %d terminal events and %d places freed", id, events, freed)
		}
	}
}

// A runtime that cannot interrupt a run fails closed: the request is refused
// with ErrStopUnavailable, the run is still marked as asked so it starts
// nothing more, and it ends as its own events say rather than being reported
// stopped.
func TestAStopTheRuntimeCannotCarryFailsClosed(t *testing.T) {
	c := &cast{
		says:  map[string][][]saigetypes.Delta{"lead": {say("Finished anyway.")}, "helper": {say("x")}},
		spawn: map[string][]string{"lead": {"helper"}},
		gates: map[string]chan struct{}{"lead": make(chan struct{})},
	}
	p := stage(t, c, platform.Config{
		Limits: call.Limits{MaxDepth: 2, MaxFanOut: 4},
		Stops:  func(context.Context, runtime.StopRequest) error { return runtime.ErrStopUnavailable },
	})
	ctx := t.Context()
	first, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-1", Agent: "lead", Text: "Start"})
	if err != nil {
		t.Fatal(err)
	}
	await(t, p, first.ID, runs.Running)
	got, err := p.StopRun(ctx, admin, first.ID)
	if !errors.Is(err, platform.ErrStopUnavailable) || !got.Stopping || got.Status.Terminal() {
		t.Fatalf("stop = %+v, %v", got, err)
	}
	if _, err := p.Call(ctx, tenant, first.ID, call.Request{Key: "k", Agent: "helper", Text: "x", Mode: call.Subagent}); err == nil {
		t.Fatal("a run asked to stop made a call")
	}
	c.release("lead")
	if end := await(t, p, first.ID, runs.Completed); end.Stopping || end.Answer != "Finished anyway." {
		t.Fatalf("run = %+v", end)
	}
}

// A run that has answered and is only held open by a run attached to it was
// not cut short. Asking it to stop stops what is attached, and it then
// completes with the answer it gave, never reported stopped first.
func TestStoppingARunThatHasAnsweredCompletesIt(t *testing.T) {
	c := &cast{
		says: map[string][][]saigetypes.Delta{
			"lead":   {say("My part is done.")},
			"helper": {say("unreachable")},
		},
		spawn: map[string][]string{"lead": {"helper"}},
		talks: map[string]talk{"helper": {after: 0, text: "Helping"}},
		gates: map[string]chan struct{}{"lead": make(chan struct{})},
	}
	p := stage(t, c, platform.Config{Limits: call.Limits{MaxDepth: 2, MaxFanOut: 4}})
	ctx := t.Context()
	first, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-1", Agent: "lead", Text: "Start"})
	if err != nil {
		t.Fatal(err)
	}
	await(t, p, first.ID, runs.Running)
	child, err := p.Call(ctx, tenant, first.ID, call.Request{Key: "k1", Agent: "helper", Text: "Your part", Mode: call.Subagent})
	if err != nil {
		t.Fatal(err)
	}
	c.release("lead")
	within(t, "the lead has answered and is held open", func() bool {
		r, _ := p.Runs.Get(tenant, first.ID)
		return r.Finishing
	})
	within(t, "the helper is talking", func() bool { _, ok := c.talking.Load("helper"); return ok })

	got, err := p.StopRun(ctx, admin, first.ID)
	if err != nil || got.Status == runs.Stopped {
		t.Fatalf("stop = %+v, %v", got, err)
	}
	if h := await(t, p, child.ID, runs.Stopped); h.Answer != "Helping" {
		t.Fatalf("helper = %+v", h)
	}
	lead := await(t, p, first.ID, runs.Completed)
	if lead.Answer != "My part is done." || lead.Cause != "" || lead.Stopping {
		t.Fatalf("lead = %+v", lead)
	}
	if events, freed := endings(p, first.ID); events != 1 || freed != 1 {
		t.Fatalf("%d terminal events, %d places freed", events, freed)
	}
}
