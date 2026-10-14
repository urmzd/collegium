package platform_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/urmzd/mandatum/pkg/agentrt"
	"github.com/urmzd/mandatum/pkg/agentspec"
	"github.com/urmzd/mandatum/pkg/runtime"
	"github.com/urmzd/saige/agent/agenttest"
	saigetypes "github.com/urmzd/saige/agent/types"

	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/artifacts"
	"github.com/urmzd/collegium/pkg/call"
	"github.com/urmzd/collegium/pkg/inbox"
	"github.com/urmzd/collegium/pkg/runs"
)

// gated is a scripted model that does not answer until it is let through, so
// a test can hold a run in flight for as long as it needs to.
type gated struct {
	*agenttest.ScriptedProvider
	gate <-chan struct{}
}

func (g *gated) Stream(ctx context.Context, req saigetypes.Request) (<-chan saigetypes.Delta, error) {
	select {
	case <-g.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return g.ScriptedProvider.Stream(ctx, req)
}

// cast is the agents of one test: what each one's model says, turn by turn,
// who it may call, which tools it holds, and which are held until released.
type cast struct {
	says  map[string][][]saigetypes.Delta
	spawn map[string][]string
	tools map[string][]string
	gates map[string]chan struct{}
	once  map[string]*sync.Once
	// shape sets anything else on an agent's spec: what waits for approval,
	// and who may answer.
	shape map[string]func(*agentspec.AgentSpec)
	// continues keeps one script per agent across the models built for it,
	// so a run that parks and resumes in a new attempt carries on where its
	// script left off rather than starting it again.
	continues bool
	scripts   map[string]*agenttest.ScriptedProvider
	// talks are agents whose first run says something and then never
	// finishes its model call, until the call is cancelled.
	talks map[string]talk

	mu sync.Mutex
	// instances counts the models built for each agent: one per run.
	instances map[string]int
	// cut is closed when a talking agent's held model call is cancelled.
	cut map[string]chan struct{}
	// sent is every request each agent's model was sent, flattened to text.
	sent map[string][]string
}

// talk is a model that answers its first scripted turns, then starts saying
// text and holds the call open until its context is cancelled. Only the
// agent's first run talks. Every later run is the scripted turns after them.
type talk struct {
	after int
	text  string
}

func (c *cast) release(agent string) { c.once[agent].Do(func() { close(c.gates[agent]) }) }

// interrupted reports whether a talking agent's held model call was cancelled.
func (c *cast) interrupted(agent string) bool {
	select {
	case <-c.cut[agent]:
		return true
	default:
		return false
	}
}

// requests returns what an agent's model has been sent, oldest first.
func (c *cast) requests(agent string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.sent[agent])
}

// recorded keeps what a model is sent, for a test to read.
type recorded struct {
	saigetypes.Provider
	c     *cast
	agent string
}

func (r *recorded) Stream(ctx context.Context, req saigetypes.Request) (<-chan saigetypes.Delta, error) {
	r.c.mu.Lock()
	r.c.sent[r.agent] = append(r.c.sent[r.agent], fmt.Sprintf("%+v", req.Messages))
	r.c.mu.Unlock()
	return r.Provider.Stream(ctx, req)
}

// talker is a talk's model for one run.
type talker struct {
	*agenttest.ScriptedProvider
	at    int
	text  string
	calls atomic.Int32
	cut   chan struct{}
	once  *sync.Once
}

func (t *talker) Stream(ctx context.Context, req saigetypes.Request) (<-chan saigetypes.Delta, error) {
	if int(t.calls.Add(1)) <= t.at {
		return t.ScriptedProvider.Stream(ctx, req)
	}
	out := make(chan saigetypes.Delta)
	go func() {
		defer close(out)
		for _, d := range []saigetypes.Delta{
			saigetypes.PartStart{Index: 0, Kind: saigetypes.KindText},
			saigetypes.PartDelta{Index: 0, Text: t.text},
		} {
			select {
			case out <- d:
			case <-ctx.Done():
				t.once.Do(func() { close(t.cut) })
				return
			}
		}
		<-ctx.Done()
		t.once.Do(func() { close(t.cut) })
	}()
	return out, nil
}

func tagging(agent, text string) []saigetypes.Delta {
	return agenttest.ToolCallResponse("call-"+agent, runtime.DelegateToolName(agent), map[string]any{"text": text})
}

func say(text string) []saigetypes.Delta { return agenttest.TextResponse(text) }

// stage runs a platform whose agents are a cast.
func stage(t *testing.T, c *cast, cfg platform.Config) *platform.Platform {
	t.Helper()
	c.once = map[string]*sync.Once{}
	for name := range c.gates {
		c.once[name] = &sync.Once{}
	}
	c.instances, c.cut, c.sent = map[string]int{}, map[string]chan struct{}{}, map[string][]string{}
	c.scripts = map[string]*agenttest.ScriptedProvider{}
	cutOnce := map[string]*sync.Once{}
	for name := range c.talks {
		c.cut[name], cutOnce[name] = make(chan struct{}), &sync.Once{}
	}
	cfg.Workspace = t.TempDir()
	if cfg.StopGrace == 0 {
		cfg.StopGrace = 2 * time.Second
	}
	cfg.AgentOptions = []agentrt.Option{agentrt.WithProvider(agentrt.ProviderOffline, func(spec agentrt.Spec) (saigetypes.Provider, error) {
		c.mu.Lock()
		c.instances[spec.Name]++
		first := c.instances[spec.Name] == 1
		c.mu.Unlock()
		says := c.says[spec.Name]
		if tk, talks := c.talks[spec.Name]; talks {
			if first {
				return &recorded{c: c, agent: spec.Name, Provider: &talker{
					ScriptedProvider: &agenttest.ScriptedProvider{Responses: says[:tk.after]},
					at:               tk.after, text: tk.text, cut: c.cut[spec.Name], once: cutOnce[spec.Name],
				}}, nil
			}
			says = says[tk.after:]
		}
		scripted := &agenttest.ScriptedProvider{Responses: says}
		if c.continues {
			c.mu.Lock()
			if c.scripts[spec.Name] == nil {
				c.scripts[spec.Name] = scripted
			}
			scripted = c.scripts[spec.Name]
			c.mu.Unlock()
		}
		if gate, held := c.gates[spec.Name]; held {
			return &recorded{c: c, agent: spec.Name, Provider: &gated{ScriptedProvider: scripted, gate: gate}}, nil
		}
		return &recorded{c: c, agent: spec.Name, Provider: scripted}, nil
	})}
	p, err := platform.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		// Nothing may still be held when the platform is asked to stop.
		for name := range c.gates {
			c.release(name)
		}
		cancel()
		<-done
	})
	for name := range c.says {
		if platform.IsBuiltin(name) {
			continue
		}
		spec := agentspec.AgentSpec{
			Name: name, Model: "offline", Provider: "offline", SystemPrompt: "x",
			Tools: c.tools[name], Access: agentspec.Access{Spawn: c.spawn[name]},
		}
		if shape := c.shape[name]; shape != nil {
			shape(&spec)
		}
		if _, err := p.Agents.Create(ctx, tenant, spec, "test"); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// called waits for a run of an agent under a root to reach a status.
func called(t *testing.T, p *platform.Platform, root, agent string, want runs.Status) runs.Run {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		tree, _ := p.Runs.Tree(tenant, root)
		for _, r := range tree {
			if r.Agent == agent && r.ID != root && r.Status == want {
				return r
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no run of %s reached %q under %s: %+v", agent, want, root, tree)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// An agent that tags another gets a run for it: its own id, events and record,
// attached to the caller, in the caller's conversation.
func TestATaggedAgentIsARunOfItsOwn(t *testing.T) {
	p := stage(t, &cast{
		says: map[string][][]saigetypes.Delta{
			"lead":   {tagging("helper", "Count the failing tests"), say("The helper counted three.")},
			"helper": {say("Three tests fail.")},
		},
		spawn: map[string][]string{"lead": {"helper"}},
	}, platform.Config{Limits: call.Limits{MaxDepth: 2, MaxFanOut: 4}})

	first, err := p.Converse(t.Context(), tenant, platform.Turn{ID: "api-1", Agent: "lead", Text: "How are the tests?"})
	if err != nil {
		t.Fatal(err)
	}
	lead := await(t, p, first.ID, runs.Completed)
	if lead.Answer != "The helper counted three." || lead.Calls != 1 {
		t.Fatalf("lead = %q after %d calls", lead.Answer, lead.Calls)
	}
	helper := called(t, p, lead.ID, "helper", runs.Completed)
	if helper.Parent != lead.ID || helper.Root != lead.ID || helper.Depth != 1 || helper.Lifecycle != runs.Attached ||
		helper.Thread != lead.Thread || helper.Text != "Count the failing tests" || helper.Answer != "Three tests fail." {
		t.Fatalf("helper = %+v", helper)
	}
	// It has a record of its own, and the caller's stream says which run it was.
	if own := p.Journal.Events(tenant, []string{helper.ID}); len(own) < 2 {
		t.Fatalf("the helper's run has %d events", len(own))
	}
	named := false
	for _, e := range p.Journal.Events(tenant, []string{lead.ID}) {
		named = named || (e.Kind == "action.taken" && strings.Contains(string(e.Payload), helper.ID))
	}
	if !named {
		t.Fatal("the caller's stream never said which run it started")
	}
}

// A delegated run is nobody's to take down: it outlives the run that started
// it, and its answer goes to the inbox. What it attaches dies with it alone.
func TestADelegatedRunIsIndependent(t *testing.T) {
	c := &cast{
		says: map[string][][]saigetypes.Delta{
			"lead": {
				agenttest.ToolCallResponse("c1", platform.ToolDelegate, map[string]any{"agent": "worker", "text": "Write the long report"}),
				tagging("mid", "Wait here"),
				say("unreachable"),
			},
			"worker": {say("The report is written.")},
			"mid":    {say("unreachable")},
		},
		spawn: map[string][]string{"lead": {"worker", "mid"}},
		tools: map[string][]string{"lead": {platform.ToolDelegate}},
		gates: map[string]chan struct{}{"worker": make(chan struct{}), "mid": make(chan struct{})},
	}
	p := stage(t, c, platform.Config{Limits: call.Limits{MaxDepth: 2, MaxFanOut: 4}})
	ctx := t.Context()
	first, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-1", Agent: "lead", Text: "Start"})
	if err != nil {
		t.Fatal(err)
	}
	worker := called(t, p, first.ID, "worker", runs.Running)
	mid := called(t, p, first.ID, "mid", runs.Running)
	if worker.Lifecycle != runs.Delegated || mid.Lifecycle != runs.Attached || worker.Root != first.ID || worker.Parent != first.ID {
		t.Fatalf("worker = %+v, mid = %+v", worker, mid)
	}

	if _, err := p.StopRun(ctx, admin, first.ID); err != nil {
		t.Fatal(err)
	}
	await(t, p, mid.ID, runs.Stopped)
	if r, _ := p.Runs.Get(tenant, worker.ID); r.Status != runs.Running {
		t.Fatalf("the delegated run is %s after its caller was stopped, want still running", r.Status)
	}

	c.release("worker")
	done := await(t, p, worker.ID, runs.Completed)
	if done.Answer != "The report is written." {
		t.Fatalf("worker = %q", done.Answer)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		page, _, _ := p.InboxPage(admin, inbox.Query{Kind: inbox.KindResult})
		if len(page) == 1 {
			m := page[0]
			if m.Source.Run != worker.ID || m.Source.Thread != first.ID || m.Parts[0].Text != "The report is written." || m.From.Name != "worker" {
				t.Fatalf("result message = %+v", m)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the delegated run's answer never reached the inbox: %+v", page)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// What a delegated run attaches belongs to it, not to whoever delegated.
func TestAttachedToADelegateDiesWithTheDelegate(t *testing.T) {
	c := &cast{
		says: map[string][][]saigetypes.Delta{
			"lead":   {agenttest.ToolCallResponse("c1", platform.ToolDelegate, map[string]any{"agent": "worker", "text": "Take this on"}), say("Handed over.")},
			"worker": {tagging("leaf", "Help me"), say("unreachable")},
			"leaf":   {say("unreachable")},
		},
		spawn: map[string][]string{"lead": {"worker"}, "worker": {"leaf"}},
		tools: map[string][]string{"lead": {platform.ToolDelegate}},
		gates: map[string]chan struct{}{"leaf": make(chan struct{})},
	}
	p := stage(t, c, platform.Config{Limits: call.Limits{MaxDepth: 3, MaxFanOut: 4}})
	ctx := t.Context()
	first, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-1", Agent: "lead", Text: "Start"})
	if err != nil {
		t.Fatal(err)
	}
	// The caller does not wait for what it delegated.
	lead := await(t, p, first.ID, runs.Completed)
	if lead.Answer != "Handed over." || lead.Finishing {
		t.Fatalf("lead = %+v", lead)
	}
	worker := called(t, p, first.ID, "worker", runs.Running)
	leaf := called(t, p, first.ID, "leaf", runs.Running)
	if leaf.Parent != worker.ID || leaf.Root != first.ID {
		t.Fatalf("leaf = %+v", leaf)
	}
	if _, err := p.StopRun(ctx, admin, worker.ID); err != nil {
		t.Fatal(err)
	}
	await(t, p, leaf.ID, runs.Stopped)
	if r, _ := p.Runs.Get(tenant, first.ID); r.Status != runs.Completed {
		t.Fatalf("stopping the delegate changed its caller to %s", r.Status)
	}
}

// A run that has answered is not done while a run attached to it is still
// working, and is done the moment the last of them is.
func TestACallerWaitsForWhatIsAttached(t *testing.T) {
	c := &cast{
		says: map[string][][]saigetypes.Delta{
			"lead":   {say("My part is done.")},
			"helper": {say("Mine too.")},
		},
		spawn: map[string][]string{"lead": {"helper"}},
		gates: map[string]chan struct{}{"lead": make(chan struct{}), "helper": make(chan struct{})},
	}
	p := stage(t, c, platform.Config{Limits: call.Limits{MaxDepth: 2, MaxFanOut: 4}})
	ctx := t.Context()
	first, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-1", Agent: "lead", Text: "Start"})
	if err != nil {
		t.Fatal(err)
	}
	await(t, p, first.ID, runs.Running)
	// Attached from outside the turn, the way the calls route does it.
	child, err := p.Call(ctx, tenant, first.ID, call.Request{Key: "k1", Agent: "helper", Text: "Your part", Mode: call.Subagent})
	if err != nil {
		t.Fatal(err)
	}
	c.release("lead")
	deadline := time.Now().Add(5 * time.Second)
	for {
		lead, _ := p.Runs.Get(tenant, first.ID)
		if lead.Finishing {
			if lead.Status != runs.Running || lead.Answer != "My part is done." || !strings.Contains(lead.Reason, "attached") {
				t.Fatalf("held lead = %+v", lead)
			}
			break
		}
		if lead.Status.Terminal() || time.Now().After(deadline) {
			t.Fatalf("the caller is %s (finishing %v) with a run still attached", lead.Status, lead.Finishing)
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.release("helper")
	await(t, p, child.ID, runs.Completed)
	lead := await(t, p, first.ID, runs.Completed)
	if lead.Finishing || lead.Reason != "" || lead.Answer != "My part is done." {
		t.Fatalf("lead after its attached run ended = %+v", lead)
	}
}

// Every run that waits holds a place. The last place is never given to a
// waiter, so there is always one for work that answers somebody.
func TestWaitingNeverTakesTheLastPlace(t *testing.T) {
	p := stage(t, &cast{
		says: map[string][][]saigetypes.Delta{
			"lead": {tagging("mid", "Go deeper"), say("Done at the top.")},
			"mid":  {tagging("leaf", "Deeper still"), say("I could not ask the leaf.")},
			"leaf": {say("unreachable")},
		},
		spawn: map[string][]string{"lead": {"mid"}, "mid": {"leaf"}},
	}, platform.Config{Limits: call.Limits{MaxDepth: 3, MaxFanOut: 4}, Concurrency: 2})

	first, err := p.Converse(t.Context(), tenant, platform.Turn{ID: "api-1", Agent: "lead", Text: "Start"})
	if err != nil {
		t.Fatal(err)
	}
	// With two places, one run may wait. The second is told no and carries
	// on, where giving it the last place would have hung all three forever.
	lead := await(t, p, first.ID, runs.Completed)
	mid := called(t, p, first.ID, "mid", runs.Completed)
	if lead.Answer != "Done at the top." || mid.Answer != "I could not ask the leaf." {
		t.Fatalf("lead = %q, mid = %q", lead.Answer, mid.Answer)
	}
	tree, _ := p.Runs.Tree(tenant, first.ID)
	if len(tree) != 2 {
		t.Fatalf("%d runs, want the lead and the mid only", len(tree))
	}
}

// The platform's writer writes a file and publishes it, on its own and when
// another agent tags it, and in both cases the file is the conversation's.
func TestTheWriterPublishesAFile(t *testing.T) {
	writing := [][]saigetypes.Delta{
		agenttest.ToolCallResponse("w1", platform.ToolFileWrite, map[string]any{"path": "plan.md", "content": "# Plan\n\nShip on Tuesday."}),
		agenttest.ToolCallResponse("w2", platform.ToolPublish, map[string]any{"path": "plan.md", "description": "The launch plan"}),
		say("I published plan.md."),
	}
	p := stage(t, &cast{
		says: map[string][][]saigetypes.Delta{
			platform.BuiltinWriter: writing,
			"lead":                 {tagging(platform.BuiltinWriter, "Write the launch plan"), say("The plan is published.")},
		},
		spawn: map[string][]string{"lead": {platform.BuiltinWriter}},
	}, platform.Config{Limits: call.Limits{MaxDepth: 2, MaxFanOut: 4}, BuiltinProvider: "offline", BuiltinModel: "offline"})
	ctx := t.Context()
	if err := p.EnsureBuiltins(ctx, tenant); err != nil {
		t.Fatal(err)
	}

	alone, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-1", Agent: platform.BuiltinWriter, Text: "Write the launch plan"})
	if err != nil {
		t.Fatal(err)
	}
	if got := await(t, p, alone.ID, runs.Completed); got.Answer != "I published plan.md." {
		t.Fatalf("writer = %q", got.Answer)
	}
	page, _, _ := p.Artifacts.Page(tenant, artifacts.Query{Thread: alone.Thread})
	if len(page) != 1 || page[0].Title != "plan.md" || page[0].Run != alone.ID || page[0].Agent != platform.BuiltinWriter || page[0].Version != 1 {
		t.Fatalf("published alone: %+v", page)
	}
	if _, body, err := p.OpenArtifact(ctx, admin, page[0].ID); err != nil || string(body) != "# Plan\n\nShip on Tuesday." {
		t.Fatalf("the file reads %q, %v", body, err)
	}

	// Tagged by another agent, the writer is attached to it and works in the
	// same conversation, so the file is filed there under the writer's run.
	asked, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-2", Agent: "lead", Text: "Get the plan written"})
	if err != nil {
		t.Fatal(err)
	}
	await(t, p, asked.ID, runs.Completed)
	writer := called(t, p, asked.ID, platform.BuiltinWriter, runs.Completed)
	page, _, _ = p.Artifacts.Page(tenant, artifacts.Query{Thread: asked.Thread})
	if len(page) != 1 || page[0].Run != writer.ID || page[0].Thread != asked.Thread || page[0].Agent != platform.BuiltinWriter {
		t.Fatalf("published when tagged: %+v (writer run %s, thread %s)", page, writer.ID, asked.Thread)
	}
}
