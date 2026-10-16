package platform_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/urmzd/mandatum/pkg/agentrt"
	"github.com/urmzd/mandatum/pkg/agentrt/payload"
	"github.com/urmzd/mandatum/pkg/agentspec"
	"github.com/urmzd/mandatum/pkg/envelope"
	"github.com/urmzd/saige/agent/agenttest"
	saigetypes "github.com/urmzd/saige/agent/types"

	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/call"
	"github.com/urmzd/collegium/pkg/inbox"
	"github.com/urmzd/collegium/pkg/policy"
	"github.com/urmzd/collegium/pkg/runs"
	"github.com/urmzd/collegium/pkg/usage"
)

// The price card every priced test model declares, as saige's catalog would
// for a real model: $3 a million tokens in, $15 out. A call of 1,000 tokens
// in and 100 out costs 3,000 + 1,500 = 4,500 micro-USD.
var card = saigetypes.Pricing{InputPerMTok: 3, OutputPerMTok: 15, AsOf: "2026-07-24"}

const window = 200_000

// cost is what a call of in and out tokens costs on card, in micro-USD.
func cost(in, out int64) int64 { return in*3 + out*15 }

// priced is a model that declares a price card and a context window, which is
// how mandatum learns what a call cost.
type priced struct {
	saigetypes.Provider
	pricing saigetypes.Pricing
}

func (p priced) Capabilities() saigetypes.ModelCapabilities {
	return saigetypes.ModelCapabilities{Model: "claude-test", ContextWindow: window, Known: true, Pricing: p.pricing}
}

// spends is a scripted response that reports what its call used.
func spends(deltas []saigetypes.Delta, in, out int) []saigetypes.Delta {
	return append(append([]saigetypes.Delta(nil), deltas...), saigetypes.UsageDelta{PromptTokens: in, CompletionTokens: out})
}

// probe lists the conversation's files: a harmless tool call, after which the
// loop asks the model again, which is all a test of spending needs.
func probe(id string) []saigetypes.Delta {
	return agenttest.ToolCallResponse(id, platform.ToolFileList, map[string]any{})
}

// probes is the tool probe needs.
var probes = []string{platform.ToolFileList}

// heldAfter is a model that answers its scripted turns and then holds its next
// call open, saying nothing, until it is cancelled: a run still working when
// a limit stops it.
type heldAfter struct {
	*agenttest.ScriptedProvider
	n     int
	calls int
	mu    sync.Mutex
}

func (h *heldAfter) Stream(ctx context.Context, req saigetypes.Request) (<-chan saigetypes.Delta, error) {
	h.mu.Lock()
	h.calls++
	n := h.calls
	h.mu.Unlock()
	if n <= h.n {
		return h.ScriptedProvider.Stream(ctx, req)
	}
	out := make(chan saigetypes.Delta)
	go func() {
		defer close(out)
		<-ctx.Done()
	}()
	return out, nil
}

// agentModel is one agent of a metering test: who it may call, and the model
// each of its runs gets, by the run's number from 1.
type agentModel struct {
	spawn []string
	tools []string
	model func(n int) saigetypes.Provider
}

// metered runs a platform whose agents are given models by number.
func metered(t *testing.T, cfg platform.Config, agents map[string]agentModel) *platform.Platform {
	t.Helper()
	var mu sync.Mutex
	made := map[string]int{}
	cfg.Workspace = t.TempDir()
	if cfg.StopGrace == 0 {
		cfg.StopGrace = time.Second
	}
	cfg.AgentOptions = []agentrt.Option{agentrt.WithProvider(agentrt.ProviderOffline, func(spec agentrt.Spec) (saigetypes.Provider, error) {
		mu.Lock()
		made[spec.Name]++
		n := made[spec.Name]
		mu.Unlock()
		return agents[spec.Name].model(n), nil
	})}
	p, err := platform.New(cfg)
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
	for _, tenantName := range []string{tenant, "globex"} {
		for name, a := range agents {
			spec := agentspec.AgentSpec{
				Name: name, Model: "offline", Provider: "offline", SystemPrompt: "x",
				Tools: a.tools, Access: agentspec.Access{Spawn: a.spawn},
			}
			if _, err := p.Agents.Create(ctx, tenantName, spec, "test"); err != nil {
				t.Fatal(err)
			}
		}
	}
	return p
}

// script is a priced model saying the given responses.
func script(responses ...[]saigetypes.Delta) saigetypes.Provider {
	return priced{Provider: &agenttest.ScriptedProvider{Responses: responses}, pricing: card}
}

func spendOf(p *platform.Platform, tenantName string) policy.Usage {
	return p.Usage.Usage(tenantName, policy.Scope{Kind: policy.Tenant}, time.Time{})
}

func settled(p *platform.Platform, runID string) int {
	n := 0
	for _, e := range p.Usage.Entries() {
		if e.Kind == usage.Settled && e.RunID == runID {
			n++
		}
	}
	return n
}

// A run's usage is the runtime's: its tokens, cost and context window are on
// its record and in the usage ledger, once. Redelivering every event the run
// published, acting on its ending again, and reconciling it with the ledger
// change nothing.
func TestARunsUsageIsRecordedOnceDespiteRedeliveredTerminalEventsAndReconcilerPasses(t *testing.T) {
	p := metered(t, platform.Config{}, map[string]agentModel{
		"lead": {model: func(int) saigetypes.Provider { return script(spends(say("Done."), 1000, 100)) }},
	})
	ctx := t.Context()
	r, err := p.Invoke(ctx, tag("evt-1", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	await(t, p, r.ID, runs.Completed)
	within(t, "the run's usage is settled", func() bool { return settled(p, r.ID) == 1 })
	done, _ := p.Runs.Get(tenant, r.ID)
	u := done.Usage
	if u == nil || !done.UsageRecorded() {
		t.Fatalf("the completed run has no recorded usage: %+v", done)
	}
	if u.Total.ModelCalls != 1 || u.Total.PromptTokens != 1000 || u.Total.CompletionTokens != 100 ||
		u.Total.Cost == nil || u.Total.Cost.Micros != cost(1000, 100) || u.Total.Cost.Currency != "USD" {
		t.Fatalf("usage = %+v, cost %+v", u.Total, u.Total.Cost)
	}
	if u.Context == nil || u.Context.Window != window || u.Context.Tokens != 1000 {
		t.Fatalf("context = %+v, want %d of %d", u.Context, 1000, window)
	}
	if done.Spent != cost(1000, 100) {
		t.Fatalf("the run spent %d, want %d", done.Spent, cost(1000, 100))
	}
	before := spendOf(p, tenant)
	if before.Spend != cost(1000, 100) || before.ModelCalls != 1 || before.Tokens != 1100 {
		t.Fatalf("the ledger counted %+v", before)
	}

	// Everything again: every event the run published, its ending, a pass of
	// the reconciler.
	for _, e := range p.Journal.Events(tenant, []string{r.ID}) {
		p.Redeliver(ctx, e)
	}
	p.Ended(ctx, done)
	p.Reconcile(ctx)

	after := spendOf(p, tenant)
	if after.Spend != before.Spend || after.ModelCalls != before.ModelCalls || after.Tokens != before.Tokens {
		t.Fatalf("redelivery changed the ledger from %+v to %+v", before, after)
	}
	if n := settled(p, r.ID); n != 1 {
		t.Fatalf("the run was settled %d times", n)
	}
	again, _ := p.Runs.Get(tenant, r.ID)
	if again.Spent != done.Spent || again.Usage.Total.Cost.Micros != u.Total.Cost.Micros {
		t.Fatalf("redelivery changed the run: spent %d, usage %+v", again.Spent, again.Usage.Total)
	}
}

// A run stopped mid-call spent what its provider reported before the stop,
// and that spend counts: on its record and against its scopes.
func TestAStoppedRunsSpendCounts(t *testing.T) {
	p := metered(t, platform.Config{}, map[string]agentModel{
		"lead": {tools: probes, model: func(int) saigetypes.Provider {
			return priced{Provider: &heldAfter{ScriptedProvider: &agenttest.ScriptedProvider{Responses: [][]saigetypes.Delta{
				spends(probe("p1"), 2000, 50),
			}}, n: 1}, pricing: card}
		}},
	})
	ctx := t.Context()
	r, err := p.Invoke(ctx, tag("evt-1", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	within(t, "the first call was counted", func() bool { return spendOf(p, tenant).ModelCalls == 1 })
	if _, err := p.StopRun(ctx, admin, r.ID); err != nil {
		t.Fatal(err)
	}
	await(t, p, r.ID, runs.Stopped)
	within(t, "the stopped run is settled", func() bool { return settled(p, r.ID) == 1 })
	stopped, _ := p.Runs.Get(tenant, r.ID)
	// The call that was cut short is counted too, as saige settled it.
	if stopped.Usage == nil || stopped.Usage.Total.Cost == nil || stopped.Usage.Total.Cost.Micros < cost(2000, 50) {
		t.Fatalf("the stopped run's usage is %+v", stopped.Usage)
	}
	if got := spendOf(p, tenant); got.Spend != stopped.Usage.Total.Cost.Micros || got.ModelCalls != stopped.Usage.Total.ModelCalls {
		t.Fatalf("the ledger counted %d over %d calls for the stopped run, which recorded %d over %d",
			got.Spend, got.ModelCalls, stopped.Usage.Total.Cost.Micros, stopped.Usage.Total.ModelCalls)
	}
}

// An attached run's usage rolls up into the run that waited on it, through
// the Delegator, exactly once: the caller's total is its own calls and the
// child's, and the usage ledger counts every call once, in the run that made
// it. A delegated run is not rolled up into its caller, and its spend still
// counts against the same workspace.
func TestChildrenRollUpExactlyOnceThroughTheDelegator(t *testing.T) {
	p := metered(t, platform.Config{Limits: call.Limits{MaxDepth: 2, MaxFanOut: 4}}, map[string]agentModel{
		"lead": {
			spawn: []string{"helper", "worker"},
			tools: []string{platform.ToolDelegate},
			model: func(int) saigetypes.Provider {
				return script(
					spends(tagging("helper", "Count the failing tests"), 1000, 100),
					spends(agenttest.ToolCallResponse("d1", platform.ToolDelegate, map[string]any{"agent": "worker", "text": "Write it up"}), 1000, 100),
					spends(say("Three fail."), 2000, 100),
				)
			},
		},
		"helper": {model: func(int) saigetypes.Provider { return script(spends(say("Three tests fail."), 500, 50)) }},
		"worker": {model: func(int) saigetypes.Provider { return script(spends(say("Written."), 300, 30)) }},
	})
	ctx := t.Context()
	r, err := p.Invoke(ctx, tag("evt-1", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	lead := await(t, p, r.ID, runs.Completed)
	helper := called(t, p, lead.ID, "helper", runs.Completed)
	worker := called(t, p, lead.ID, "worker", runs.Completed)
	within(t, "every run is settled", func() bool {
		return settled(p, lead.ID) == 1 && settled(p, helper.ID) == 1 && settled(p, worker.ID) == 1
	})
	lead, _ = p.Runs.Get(tenant, lead.ID)
	helper, _ = p.Runs.Get(tenant, helper.ID)

	own := cost(1000, 100) + cost(1000, 100) + cost(2000, 100)
	child := cost(500, 50)
	delegated := cost(300, 30)
	u := lead.Usage
	if u == nil || u.Own.Cost == nil || u.Own.Cost.Micros != own || u.Own.ModelCalls != 3 {
		t.Fatalf("lead's own usage = %+v", u)
	}
	if u.Children.Cost == nil || u.Children.Cost.Micros != child || u.Children.ModelCalls != 1 {
		t.Fatalf("lead's children = %+v, want the helper's %d and not the delegated worker's", u.Children, child)
	}
	if u.Total.Cost.Micros != own+child {
		t.Fatalf("lead's total = %d, want %d", u.Total.Cost.Micros, own+child)
	}
	if helper.Usage.Total.Cost.Micros != child {
		t.Fatalf("helper's total = %+v", helper.Usage.Total)
	}
	reports := 0
	for _, e := range p.Journal.Events(tenant, []string{lead.ID}) {
		if e.Kind == envelope.KindUsageChild {
			reports++
		}
	}
	if reports != 1 {
		t.Fatalf("lead's stream carries %d usage.child reports, want the helper's one", reports)
	}
	// Every call of the tree once, in the run that made it.
	if got := spendOf(p, tenant); got.Spend != own+child+delegated || got.ModelCalls != 5 {
		t.Fatalf("the ledger counted %d over %d calls, want %d over 5", got.Spend, got.ModelCalls, own+child+delegated)
	}
	// The caller's lease accounting has both calls settled into it.
	if lead.Spent != own+child+delegated || lead.Granted != 0 {
		t.Fatalf("lead spent %d with %d still granted, want %d and 0", lead.Spent, lead.Granted, own+child+delegated)
	}
}

// Admission refuses a new run once the scope's spend for the period has
// reached its limit, naming the limit and the policy.
func TestAdmissionRefusesAtTheSpendLimit(t *testing.T) {
	p := metered(t, platform.Config{}, map[string]agentModel{
		"lead": {model: func(int) saigetypes.Provider { return script(spends(say("Done."), 1000, 100)) }},
	})
	ctx := t.Context()
	if _, err := p.Policies.Put(tenant, policy.Policy{
		Scope: policy.Scope{Kind: policy.Tenant}, Period: policy.Month, SpendLimit: cost(1000, 100), Action: policy.Block,
	}, "ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	r, err := p.Invoke(ctx, tag("evt-1", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	await(t, p, r.ID, runs.Completed)
	within(t, "the spend is counted", func() bool { return spendOf(p, tenant).Spend == cost(1000, 100) })

	_, err = p.Invoke(ctx, tag("evt-2", "lead"))
	var denied *platform.Denied
	if !errors.As(err, &denied) || denied.Decision.Limit != policy.LimitSpend || denied.Decision.Policy.Version != 1 || denied.Decision.Remaining != 0 {
		t.Fatalf("the second run was not refused at the spend limit: %v", err)
	}
	// Another workspace's limit is its own.
	if _, err := p.Invoke(ctx, envelope.Tag{ID: "evt-2", Tenant: "globex", Agent: "lead", Origin: "api", Text: "hello"}); err != nil {
		t.Fatalf("another workspace was refused: %v", err)
	}
}

// A run that crosses the hard limit mid-run is stopped through the runtime,
// with a reason naming the limit, and what it spent is recorded. Crossing the
// warning threshold on the way raises one inbox message.
func TestARunCrossingTheHardLimitMidRunIsStoppedThroughTheRuntime(t *testing.T) {
	p := metered(t, platform.Config{}, map[string]agentModel{
		"lead": {tools: probes, model: func(int) saigetypes.Provider {
			return priced{Provider: &heldAfter{ScriptedProvider: &agenttest.ScriptedProvider{Responses: [][]saigetypes.Delta{
				spends(probe("p1"), 1000, 100), // 4,500: 90% of the limit
				spends(probe("p2"), 1000, 100), // 9,000: over it
			}}, n: 2}, pricing: card}
		}},
	})
	ctx := t.Context()
	if _, err := p.Policies.Put(tenant, policy.Policy{
		Scope: policy.Scope{Kind: policy.Tenant}, Period: policy.Day, SpendLimit: 5000, Action: policy.Block,
	}, "ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	r, err := p.Invoke(ctx, tag("evt-1", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	stopped := await(t, p, r.ID, runs.Stopped)
	if !strings.Contains(stopped.Cause, "reached its limit of spend") || !strings.Contains(stopped.Cause, "version 1") {
		t.Fatalf("the run was stopped for %q", stopped.Cause)
	}
	stoppedEvent := false
	for _, e := range p.Journal.Events(tenant, []string{r.ID}) {
		stoppedEvent = stoppedEvent || (e.Kind == envelope.KindStopped && strings.Contains(string(e.Payload), platform.StoppedByLimit))
	}
	if !stoppedEvent {
		t.Fatal("the runtime did not publish lifecycle.stopped for the limit")
	}
	within(t, "the stopped run is settled", func() bool { return settled(p, r.ID) == 1 })
	// Both calls, and the one the stop cut short as saige settled it: the
	// same as the run's own record.
	stopped, _ = p.Runs.Get(tenant, r.ID)
	if got := spendOf(p, tenant); got.Spend < 2*cost(1000, 100) || stopped.Usage == nil || got.Spend != stopped.Usage.Total.Cost.Micros {
		t.Fatalf("the ledger counted %d, the run recorded %+v; want both calls, once", got.Spend, stopped.Usage)
	}
	var warned, halted int
	for _, m := range p.Inbox.OpenOf(tenant, inbox.KindBudget) {
		for _, part := range m.Parts {
			if part.Budget != nil && part.Budget.Limit == "spend" {
				if part.Budget.Refused {
					halted++
				} else {
					warned++
				}
			}
		}
	}
	if warned != 1 || halted != 1 {
		t.Fatalf("%d warning and %d stop messages, want one of each", warned, halted)
	}
	notices := 0
	for _, n := range p.Usage.Notices(tenant, 0) {
		if n.Kind == usage.Halted && n.RunID == r.ID && n.Limit == policy.LimitSpend {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("%d halted notices for the run", notices)
	}
}

// Crossing the warning threshold raises one inbox message, however many calls
// and runs cross it while it is open.
func TestTheWarningThresholdRaisesOneInboxMessage(t *testing.T) {
	p := metered(t, platform.Config{}, map[string]agentModel{
		"lead": {tools: probes, model: func(int) saigetypes.Provider {
			return script(spends(probe("p1"), 1000, 100), spends(probe("p2"), 1000, 100), spends(say("Done."), 1000, 100))
		}},
	})
	ctx := t.Context()
	if _, err := p.Policies.Put(tenant, policy.Policy{
		Scope: policy.Scope{Kind: policy.Tenant}, Period: policy.Day, SpendLimit: 1_000_000, WarnAt: 1, Action: policy.Block,
	}, "ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"evt-1", "evt-2"} {
		r, err := p.Invoke(ctx, tag(id, "lead"))
		if err != nil {
			t.Fatal(err)
		}
		within(t, "the run ended", func() bool { got, _ := p.Runs.Get(tenant, r.ID); return got.Status.Terminal() })
		if got, _ := p.Runs.Get(tenant, r.ID); got.Status != runs.Completed {
			t.Fatalf("run %s: %s %s", id, got.Status, got.Error)
		}
	}
	within(t, "six calls are counted", func() bool { return spendOf(p, tenant).ModelCalls == 6 })
	if n := len(p.Inbox.OpenOf(tenant, inbox.KindBudget)); n != 1 {
		t.Fatalf("%d budget messages, want one", n)
	}
}

// A call with no price, or priced in another currency, is never counted as
// dollars against a spend limit. The scope is told once that its spend is a
// lower bound, and a token limit bounds every call whatever its price.
func TestUnpricedAndNonUSDCallsFollowTheRule(t *testing.T) {
	euro := card
	euro.Currency = "EUR"
	p := metered(t, platform.Config{}, map[string]agentModel{
		"plain": {model: func(int) saigetypes.Provider {
			return &agenttest.ScriptedProvider{Responses: [][]saigetypes.Delta{spends(say("Done."), 1000, 100)}}
		}},
		"euro": {model: func(int) saigetypes.Provider {
			return priced{Provider: &agenttest.ScriptedProvider{Responses: [][]saigetypes.Delta{spends(say("Fertig."), 1000, 100)}}, pricing: euro}
		}},
	})
	ctx := t.Context()
	if _, err := p.Policies.Put(tenant, policy.Policy{
		Scope: policy.Scope{Kind: policy.Tenant}, Period: policy.Day, SpendLimit: 1, MaxTokens: 3000, Action: policy.Block,
	}, "ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, r := range []envelope.Tag{tag("evt-1", "plain"), tag("evt-2", "euro")} {
		got, err := p.Invoke(ctx, r)
		if err != nil {
			t.Fatalf("%s was refused: %v", r.Agent, err)
		}
		// Neither is stopped: neither spent a dollar that can be counted.
		await(t, p, got.ID, runs.Completed)
	}
	within(t, "both calls are counted", func() bool { return spendOf(p, tenant).ModelCalls == 2 })
	got := spendOf(p, tenant)
	if got.Spend != 0 || got.UnpricedCalls != 1 || len(got.OtherCosts) != 1 || got.OtherCosts[0].Currency != "EUR" ||
		got.OtherCosts[0].Micros != cost(1000, 100) || got.Tokens != 2200 {
		t.Fatalf("usage = %+v", got)
	}
	lower := 0
	for _, m := range p.Inbox.OpenOf(tenant, inbox.KindBudget) {
		if strings.Contains(m.Subject, "lower bound") {
			lower++
		}
	}
	if lower != 1 {
		t.Fatalf("%d lower-bound messages, want one for the scope", lower)
	}
	// A third run takes the tokens past their limit, and the next is refused
	// on tokens.
	third, err := p.Invoke(ctx, tag("evt-3", "plain"))
	if err != nil {
		t.Fatal(err)
	}
	within(t, "the third run ended", func() bool { r, _ := p.Runs.Get(tenant, third.ID); return r.Status.Terminal() })
	within(t, "its tokens are counted", func() bool { return spendOf(p, tenant).Tokens == 3300 })
	_, err = p.Invoke(ctx, tag("evt-4", "plain"))
	var denied *platform.Denied
	if !errors.As(err, &denied) || denied.Decision.Limit != policy.LimitTokens {
		t.Fatalf("the run past the token limit was not refused on tokens: %v", err)
	}
}

// One workspace's spending never counts against another's limits, and
// another workspace's run is not found.
func TestAnotherTenantsUsageNeverAffectsALimit(t *testing.T) {
	p := metered(t, platform.Config{}, map[string]agentModel{
		"lead": {model: func(int) saigetypes.Provider { return script(spends(say("Done."), 1000, 100)) }},
	})
	ctx := t.Context()
	if _, err := p.Policies.Put(tenant, policy.Policy{
		Scope: policy.Scope{Kind: policy.Tenant}, Period: policy.Day, SpendLimit: cost(1000, 100), Action: policy.Block,
	}, "ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	theirs, err := p.Invoke(ctx, envelope.Tag{ID: "evt-1", Tenant: "globex", Agent: "lead", Origin: "api", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	within(t, "globex's run is settled", func() bool { return settled(p, theirs.ID) == 1 })
	if got := spendOf(p, tenant); got.Spend != 0 || got.ModelCalls != 0 {
		t.Fatalf("acme counts globex's spending: %+v", got)
	}
	if _, err := p.Runs.Get(tenant, theirs.ID); !errors.Is(err, runs.ErrNotFound) {
		t.Fatalf("acme can read globex's run: %v", err)
	}
	if _, err := p.Invoke(ctx, tag("evt-1", "lead")); err != nil {
		t.Fatalf("acme was refused for globex's spending: %v", err)
	}
}

// A called run leases budget from its caller, is held to it with real cost,
// and settles with its caller when it ends: what it leased is released and
// what it spent becomes the caller's.
func TestALeaseIsHeldToRealCostAndSettlesWithTheCaller(t *testing.T) {
	gate := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	p := metered(t, platform.Config{Limits: call.Limits{MaxDepth: 2, MaxFanOut: 4}}, map[string]agentModel{
		"lead": {spawn: []string{"helper"}, model: func(int) saigetypes.Provider {
			return &gated{ScriptedProvider: &agenttest.ScriptedProvider{Responses: [][]saigetypes.Delta{say("Done.")}}, gate: gate}
		}},
		"helper": {tools: probes, model: func(int) saigetypes.Provider {
			return priced{Provider: &heldAfter{ScriptedProvider: &agenttest.ScriptedProvider{Responses: [][]saigetypes.Delta{
				spends(probe("p1"), 1000, 100), // 4,500 of a 5,000 lease
				spends(probe("p2"), 1000, 100), // 9,000: the lease is spent
			}}, n: 2}, pricing: card}
		}},
	})
	ctx := t.Context()
	lead, err := p.Invoke(ctx, tag("evt-1", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	child, err := p.Call(ctx, tenant, lead.ID, call.Request{Key: "k1", Agent: "helper", Text: "Go", Mode: call.Delegate, Lease: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if held, _ := p.Runs.Get(tenant, lead.ID); held.Granted != 5000 {
		t.Fatalf("lead holds %d granted, want the 5,000 lease", held.Granted)
	}
	stopped := await(t, p, child.ID, runs.Stopped)
	if !strings.Contains(stopped.Cause, "budget") {
		t.Fatalf("the child was stopped for %q", stopped.Cause)
	}
	within(t, "the child settled with its caller", func() bool {
		l, _ := p.Runs.Get(tenant, lead.ID)
		c, _ := p.Runs.Get(tenant, child.ID)
		return l.Granted == 0 && c.Spent >= 2*cost(1000, 100) && l.Spent == c.Spent
	})
	close(gate)
	await(t, p, lead.ID, runs.Completed)
}

// A usage report for a run that has already settled, carrying a call the run
// never reported before, is counted in the ledger only beyond the settled
// total, and moves neither the run's Spent nor its caller's.
func TestACallReportedAfterItsRunSettledDoesNotInflateSpent(t *testing.T) {
	p := metered(t, platform.Config{Limits: call.Limits{MaxDepth: 2, MaxFanOut: 4}}, map[string]agentModel{
		"lead": {spawn: []string{"helper"}, model: func(int) saigetypes.Provider {
			return script(tagging("helper", "Count"), say("Done."))
		}},
		"helper": {model: func(int) saigetypes.Provider { return script(spends(say("Three."), 1000, 100)) }},
	})
	ctx := t.Context()
	r, err := p.Invoke(ctx, tag("evt-1", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	await(t, p, r.ID, runs.Completed)
	helper := called(t, p, r.ID, "helper", runs.Completed)
	within(t, "both runs are settled", func() bool { return settled(p, r.ID) == 1 && settled(p, helper.ID) == 1 })
	lead, _ := p.Runs.Get(tenant, r.ID)
	helper, _ = p.Runs.Get(tenant, helper.ID)
	if lead.Spent != cost(1000, 100) || helper.Spent != cost(1000, 100) {
		t.Fatalf("lead spent %d, helper %d", lead.Spent, helper.Spent)
	}
	before := spendOf(p, tenant)

	// The helper's call again, under a key never seen: the shape of a call
	// whose event was missed and arrives after settlement.
	var late envelope.Event
	for _, e := range p.Journal.Events(tenant, []string{helper.ID}) {
		if e.Kind == envelope.KindUsageCall {
			late = e
		}
	}
	c, err := payload.Decode[payload.UsageCall](late)
	if err != nil {
		t.Fatal(err)
	}
	c.AccountingID += "-late"
	if late.Payload, err = payload.Encode(c); err != nil {
		t.Fatal(err)
	}
	p.Redeliver(ctx, late)

	if l, _ := p.Runs.Get(tenant, r.ID); l.Spent != lead.Spent {
		t.Fatalf("the caller's spent moved from %d to %d", lead.Spent, l.Spent)
	}
	if h, _ := p.Runs.Get(tenant, helper.ID); h.Spent != helper.Spent {
		t.Fatalf("the settled run's spent moved from %d to %d", helper.Spent, h.Spent)
	}
	// The ledger counts what goes beyond the settled total, once.
	after := spendOf(p, tenant)
	if after.Spend != before.Spend+cost(1000, 100) || after.ModelCalls != before.ModelCalls+1 {
		t.Fatalf("the ledger went from %+v to %+v", before, after)
	}
	p.Redeliver(ctx, late)
	if again := spendOf(p, tenant); again.Spend != after.Spend {
		t.Fatal("the late call was counted twice")
	}
}

// A lease covers the run and everything it calls: what it has handed to a
// call still working counts against it, and a call is granted half of what is
// left, so a caller whose own spend was not counted yet still leaves room.
func TestALeaseCountsWhatIsHandedToCallsStillWorking(t *testing.T) {
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	p := metered(t, platform.Config{Limits: call.Limits{MaxDepth: 3, MaxFanOut: 4}}, map[string]agentModel{
		"lead": {spawn: []string{"helper"}, model: func(int) saigetypes.Provider {
			return &gated{ScriptedProvider: &agenttest.ScriptedProvider{Responses: [][]saigetypes.Delta{say("Done.")}}, gate: gate}
		}},
		"helper": {spawn: []string{"worker"}, tools: append([]string{platform.ToolDelegate}, probes...), model: func(int) saigetypes.Provider {
			return priced{Provider: &heldAfter{ScriptedProvider: &agenttest.ScriptedProvider{Responses: [][]saigetypes.Delta{
				spends(agenttest.ToolCallResponse("d1", platform.ToolDelegate, map[string]any{"agent": "worker", "text": "Go"}), 1000, 100), // 4,500
				spends(probe("p1"), 500, 100), // 3,000: 7,500 spent, below the lease on its own
			}}, n: 2}, pricing: card}
		}},
		// The worker holds what it was granted and never finishes.
		"worker": {model: func(int) saigetypes.Provider {
			return &gated{ScriptedProvider: &agenttest.ScriptedProvider{Responses: [][]saigetypes.Delta{say("Done.")}}, gate: gate}
		}},
	})
	ctx := t.Context()
	lead, err := p.Invoke(ctx, tag("evt-1", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	helper, err := p.Call(ctx, tenant, lead.ID, call.Request{Key: "k1", Agent: "helper", Text: "Go", Mode: call.Delegate, Lease: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	worker := called(t, p, lead.ID, "worker", runs.Running)
	if worker.Lease <= 0 || worker.Lease > 5_000 {
		t.Fatalf("the worker was granted %d of a 10,000 lease, want at most half", worker.Lease)
	}
	stopped := await(t, p, helper.ID, runs.Stopped)
	if !strings.Contains(stopped.Cause, "handed to calls still working") {
		t.Fatalf("the helper was stopped for %q", stopped.Cause)
	}
	if stopped.Spent+stopped.Granted < 10_000 {
		t.Fatalf("the helper was stopped at %d spent and %d granted, under its lease", stopped.Spent, stopped.Granted)
	}
}

// A delegated call that ends after the run that made it has settled climbs on
// to the root, so the root's spend is the whole tree's, as the ledger has it.
func TestACallEndingAfterItsCallerReachesTheRoot(t *testing.T) {
	gate, rootGate := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(rootGate) })
	p := metered(t, platform.Config{Limits: call.Limits{MaxDepth: 3, MaxFanOut: 4}}, map[string]agentModel{
		"lead": {spawn: []string{"mid"}, model: func(int) saigetypes.Provider {
			return &gated{ScriptedProvider: &agenttest.ScriptedProvider{Responses: [][]saigetypes.Delta{say("Done.")}}, gate: rootGate}
		}},
		"mid": {spawn: []string{"worker"}, tools: []string{platform.ToolDelegate}, model: func(int) saigetypes.Provider {
			return script(
				spends(agenttest.ToolCallResponse("d1", platform.ToolDelegate, map[string]any{"agent": "worker", "text": "Go"}), 1000, 100),
				spends(say("Handed over."), 100, 10),
			)
		}},
		"worker": {model: func(int) saigetypes.Provider {
			return priced{Provider: &gated{ScriptedProvider: &agenttest.ScriptedProvider{Responses: [][]saigetypes.Delta{
				spends(say("Done late."), 2000, 200),
			}}, gate: gate}, pricing: card}
		}},
	})
	ctx := t.Context()
	lead, err := p.Invoke(ctx, tag("evt-1", "lead"))
	if err != nil {
		t.Fatal(err)
	}
	mid, err := p.Call(ctx, tenant, lead.ID, call.Request{Key: "k1", Agent: "mid", Text: "Go", Mode: call.Delegate})
	if err != nil {
		t.Fatal(err)
	}
	await(t, p, mid.ID, runs.Completed)
	within(t, "mid settled into the root", func() bool { return settled(p, mid.ID) == 1 })
	close(gate)
	worker := called(t, p, lead.ID, "worker", runs.Completed)
	within(t, "the worker settled", func() bool { return settled(p, worker.ID) == 1 })
	tree := spendOf(p, tenant).Spend
	if want := cost(1000, 100) + cost(100, 10) + cost(2000, 200); tree != want {
		t.Fatalf("the ledger counted %d, want %d", tree, want)
	}
	within(t, "the root's spend is the tree's", func() bool { r, _ := p.Runs.Get(tenant, lead.ID); return r.Spent == tree })
}

// Housekeeping is never stopped at a limit, though what it spends counts.
func TestHousekeepingIsExemptFromTheSpendStop(t *testing.T) {
	p := metered(t, platform.Config{OfflineHooks: true}, map[string]agentModel{
		"lead": {model: func(int) saigetypes.Provider { return script(spends(say("Done."), 1000, 100)) }},
		"scribe": {tools: probes, model: func(int) saigetypes.Provider {
			return script(spends(probe("p1"), 1000, 100), spends(probe("p2"), 1000, 100), spends(say("A title"), 1000, 100))
		}},
	})
	ctx := t.Context()
	if err := p.SetSettings(ctx, tenant, platform.Settings{TitleAgent: "scribe"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Policies.Put(tenant, policy.Policy{
		Scope: policy.Scope{Kind: policy.Tenant}, Period: policy.Day, SpendLimit: 1000, Action: policy.Block,
	}, "ada", time.Now()); err != nil {
		t.Fatal(err)
	}
	first, err := p.Converse(ctx, tenant, platform.Turn{ID: "api-1", Agent: "lead", Text: "Name this"})
	if err != nil {
		t.Fatal(err)
	}
	within(t, "the turn ended", func() bool { r, _ := p.Runs.Get(tenant, first.ID); return r.Status.Terminal() })
	var hook runs.Run
	within(t, "the title run ended", func() bool {
		for _, r := range p.Runs.List(tenant, 20) {
			if r.Origin == runs.OriginSystem && r.Status.Terminal() {
				hook = r
				return true
			}
		}
		return false
	})
	if hook.Status != runs.Completed {
		t.Fatalf("the housekeeping run ended %s: %s", hook.Status, hook.Cause)
	}
	within(t, "its spend is counted", func() bool { return settled(p, hook.ID) == 1 })
	for _, n := range p.Usage.Notices(tenant, 0) {
		if n.Kind == usage.Halted && n.RunID == hook.ID {
			t.Fatal("the housekeeping run was halted")
		}
	}
	if got := p.Usage.RunSpend(tenant, hook.ID); got.Micros != 3*cost(1000, 100) {
		t.Fatalf("the housekeeping run's spend = %+v", got)
	}
}
