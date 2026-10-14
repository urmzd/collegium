// Package platform assembles the system: mandatum's registry, bus, router and
// durable runtime, the cron trigger, and this repo's run index and call path.
//
//	trigger or API  →  Invoke  →  runtime  →  bus  →  router  →  sinks
//	                     ↓                     ↓
//	                 run index  ←──────────  observer
//
// Read New top to bottom and you have read how the platform fits together.
package platform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/urmzd/duraturo"
	"github.com/urmzd/duraturo/pkg/ledger"
	dqueue "github.com/urmzd/duraturo/pkg/queue"
	drun "github.com/urmzd/duraturo/pkg/run"
	"github.com/urmzd/duraturo/pkg/worker"
	"github.com/urmzd/legatus/pkg/controlplane"
	"github.com/urmzd/legatus/pkg/metrics"
	"github.com/urmzd/legatus/pkg/node/inproc"
	"github.com/urmzd/legatus/pkg/tool"
	"github.com/urmzd/legatus/pkg/workspace"
	"github.com/urmzd/mandatum/pkg/agentrt"
	"github.com/urmzd/mandatum/pkg/bus"
	"github.com/urmzd/mandatum/pkg/connector"
	"github.com/urmzd/mandatum/pkg/connectors/cron"
	"github.com/urmzd/mandatum/pkg/envelope"
	"github.com/urmzd/mandatum/pkg/registry"
	"github.com/urmzd/mandatum/pkg/router"
	"github.com/urmzd/mandatum/pkg/runtime"
	"github.com/urmzd/mandatum/pkg/topic"

	"github.com/urmzd/collegium/pkg/alerts"
	"github.com/urmzd/collegium/pkg/artifacts"
	"github.com/urmzd/collegium/pkg/blob"
	"github.com/urmzd/collegium/pkg/call"
	"github.com/urmzd/collegium/pkg/clients"
	"github.com/urmzd/collegium/pkg/evals"
	"github.com/urmzd/collegium/pkg/inbox"
	"github.com/urmzd/collegium/pkg/integrations/github"
	"github.com/urmzd/collegium/pkg/integrations/slack"
	"github.com/urmzd/collegium/pkg/journal"
	"github.com/urmzd/collegium/pkg/knowledge"
	"github.com/urmzd/collegium/pkg/memory"
	"github.com/urmzd/collegium/pkg/policy"
	"github.com/urmzd/collegium/pkg/prefs"
	"github.com/urmzd/collegium/pkg/runs"
	"github.com/urmzd/collegium/pkg/usage"
)

// ErrForbidden reports a call the caller's agent spec does not allow.
var ErrForbidden = errors.New("platform: forbidden")

// Config is what a running platform needs. Nothing here reads the environment,
// so a test can build a second platform in the same process.
type Config struct {
	// Connections seats connectors the embedding program built itself, after
	// the registry exists. Clients created through CreateClient need none.
	Connections func(registry.Store) ([]Connection, error)
	// Clients keeps client records, and Secrets the secret values written for
	// them. Nil means in memory. Enabled clients already in the store are
	// started by New.
	Clients clients.Store
	Secrets clients.Secrets
	// Env reads the process's environment for an "env:NAME" secret reference.
	// Nil resolves none.
	Env func(name string) (string, bool)
	// SecretRefs is which environment variables each tenant's clients may
	// reference, by tenant. The environment belongs to the whole process, so
	// this is the tenant boundary on it: a name is read for a tenant only if
	// it is listed here for that tenant. It is the server's configuration and
	// nothing in the API can change it. A tenant with no entry may reference
	// nothing, and can still write its secrets as values.
	SecretRefs map[string][]string
	// SlackAPI and GitHubAPI build the API a client writes through from its
	// token. Nil means the real one over HTTP.
	SlackAPI  func(token string) slack.API
	GitHubAPI func(token string) github.API
	// ClientInterval coalesces a client's edits to its surface. Zero means
	// the connector's default.
	ClientInterval time.Duration
	// Workspace is the directory agents write artifacts under. Empty takes a
	// directory under the user's cache.
	Workspace string
	// Limits bounds every tree of calls.
	Limits call.Limits
	// Concurrency is how many runs execute at once. Zero means one.
	Concurrency int
	// StopGrace is how long a run asked to stop is given to wrap up before
	// its turn is cancelled. Zero means the runtime's default, ten seconds.
	StopGrace time.Duration
	// OfflineHooks lets housekeeping run on the offline provider. It exists
	// for tests: an offline answer is a notice, not a title or a fact.
	OfflineHooks bool
	// BuiltinProvider and BuiltinModel are what the platform's own agents
	// run on. With either empty there are no built-in agents.
	BuiltinProvider string
	BuiltinModel    string
	// AgentOptions are passed to every agent the sandbox builds. A test
	// uses them to script a model.
	AgentOptions []agentrt.Option
	Log          *slog.Logger
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
}

// Platform is the assembled system.
type Platform struct {
	cfg Config
	log *slog.Logger
	now func() time.Time

	Agents    *registry.Memory
	Runs      *runs.Index
	Schedules *Schedules
	Journal   *journal.Journal
	Alerts    *alerts.Engine
	Knowledge *knowledge.Store
	Evals     *evals.Store
	Prefs     *prefs.Store
	Memory    *memory.Store
	Policies  *policy.Store
	Usage     *usage.Ledger
	Inbox     *inbox.Store
	Artifacts *artifacts.Index
	// Clients is the external clients each tenant answers through.
	Clients clients.Store
	// Blobs holds the bytes of published files.
	Blobs blob.Store

	// dir is the workspace directory, resolved once.
	dir string
	// waiting counts runs that are waiting on a run attached to them.
	waiting atomic.Int64
	// publishing makes a storage check and the write it allows one step.
	publishing sync.Mutex

	broker  bus.Bus
	sinks   *connector.Registry
	deliver *router.Router
	rt      *runtime.Runtime
	// stops is how a run is asked to stop: the runtime, unless a test
	// stands in for it.
	stops  stopper
	ledger *ledger.Memory
	queue  *dqueue.Memory

	settings settings

	// surfaces is one per client surface, by address scheme. The map is
	// filled by New and never changes; what is seated on each does.
	surfaces map[string]*surface
	secrets  clients.Secrets
	// clientsMu serializes creating, updating and deleting clients.
	clientsMu sync.Mutex
	// admitting holds runs between admission and their record, by run id.
	admitting sync.Map

	// admission serializes letting a run in. It is taken inside calls, never
	// the other way round.
	admission sync.Mutex

	// calls serializes Call, so two calls from one run cannot both pass a
	// fan-out or budget check that only one of them fits under.
	calls sync.Mutex
}

// New assembles a platform. Nothing runs until Run is called.
func New(cfg Config) (*Platform, error) {
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	p := &Platform{
		cfg: cfg, log: log, now: now,
		Agents:    registry.NewMemory(),
		Runs:      runs.NewIndex(),
		Schedules: &Schedules{},
		Journal:   journal.New(0),
		Alerts:    alerts.NewEngine(),
		Knowledge: knowledge.NewStore(),
		Evals:     evals.NewStore(),
		Prefs:     prefs.NewStore(),
		Memory:    memory.NewStore(),
		Policies:  policy.NewStore(),
		Usage:     usage.NewLedger(),
		Inbox:     inbox.NewStore(),
		Artifacts: artifacts.NewIndex(),
		broker:    bus.NewMemory(),
		sinks:     connector.NewRegistry(),
		ledger:    ledger.NewMemory(),
		queue:     dqueue.NewMemory(),
	}

	// Schedules are a trigger like any other: the cron connector raises tags
	// and never learns what a runtime is. It reads the store on every tick,
	// so a schedule created through the API takes effect without a restart.
	clock, err := cron.New(cron.Config{Store: p.Schedules, Log: log})
	if err != nil {
		return nil, fmt.Errorf("platform: cron: %w", err)
	}
	if err := p.sinks.Register(clock); err != nil {
		return nil, fmt.Errorf("platform: register cron: %w", err)
	}
	// One surface per scheme, whether or not it has clients yet: clients are
	// created while the platform runs, and the router and the trigger loop
	// are given their connectors once.
	p.Clients, p.secrets = cfg.Clients, cfg.Secrets
	if p.Clients == nil {
		p.Clients = clients.NewMemory()
	}
	if p.secrets == nil {
		p.secrets = clients.NewMemorySecrets()
	}
	p.surfaces = make(map[string]*surface)
	for _, name := range clients.Surfaces() {
		s := &surface{name: string(name), p: p}
		if err := p.sinks.Register(s); err != nil {
			return nil, fmt.Errorf("platform: register %s: %w", name, err)
		}
		p.surfaces[s.name] = s
	}
	if cfg.Connections != nil {
		connections, err := cfg.Connections(p.Agents)
		if err != nil {
			return nil, fmt.Errorf("platform: connections: %w", err)
		}
		for _, connection := range connections {
			if err := p.static(connection); err != nil {
				return nil, err
			}
		}
	}
	p.restore()

	p.deliver = router.New(p.broker, p.sinks, router.WithLogger(log))

	exec, err := p.executor()
	if err != nil {
		return nil, err
	}
	rt, err := runtime.New(
		duraturo.New(p.ledger, p.queue),
		registry.NewSpecs(p.Agents),
		p.broker,
		exec,
		runtime.WithLogger(log),
		runtime.WithStopGrace(cfg.StopGrace),
	)
	if err != nil {
		return nil, fmt.Errorf("platform: runtime: %w", err)
	}
	p.rt, p.stops = rt, rt
	return p, nil
}

// executor builds the sandbox every turn runs in: a legatus task under the
// NGAC policy compiled from the pinned revision's access grant.
func (p *Platform) executor() (runtime.Executor, error) {
	dir := p.cfg.Workspace
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		}
		dir = filepath.Join(base, "collegium", "workspace")
	}
	p.dir = dir
	ws, err := workspace.NewLocal(dir)
	if err != nil {
		return nil, fmt.Errorf("platform: workspace %q: %w", dir, err)
	}
	if p.Blobs == nil {
		blobs, err := blob.NewDisk(filepath.Join(dir, "blobs"))
		if err != nil {
			return nil, fmt.Errorf("platform: storage: %w", err)
		}
		p.Blobs = blobs
	}
	tools := tool.NewRegistry()
	plane := controlplane.NewMemory(inproc.NewFactory(tools, ws), metrics.Nop())
	exec, err := runtime.NewSandbox(plane, tools, registry.NewSpecs(p.Agents),
		runtime.WithCatalog(p.fileTools(p.connectorTools)),
		runtime.WithDelegator(p.attach),
		// A run waiting on an attached run of the same agent holds one of
		// that agent's nodes, so there are as many as there are places.
		runtime.WithReplicas(p.slots()),
		runtime.WithAgentOptions(p.cfg.AgentOptions...),
		runtime.WithSandboxLogger(p.log),
	)
	if err != nil {
		return nil, fmt.Errorf("platform: sandbox: %w", err)
	}
	return exec, nil
}

// Invoke accepts a tag and starts its run. Routes are registered before the run
// is accepted, because accepting publishes the run's first event.
func (p *Platform) Invoke(ctx context.Context, tag envelope.Tag) (runs.Run, error) {
	if tag.At.IsZero() {
		tag.At = p.now()
	}
	// Fail before anything is registered if the tag claims to come from an
	// agent but carries no usable edge.
	edge, err := call.EdgeOf(tag)
	if err != nil {
		return runs.Run{}, err
	}
	// The run id is the runtime's, derived from the tenant and the tag id
	// together. Two tenants offering the same tag id name two different runs,
	// so a trigger's id, chosen outside, cannot join another tenant's run.
	runID := runtime.RunID(tag.Tenant, tag.ID)

	// Admission and acceptance are one step, so the count a policy is judged
	// on always includes every run already let in.
	p.admission.Lock()
	defer p.admission.Unlock()
	// The same tag again joins the run it already started. It is not a new
	// run, so it is neither judged nor counted a second time.
	if existing, err := p.Runs.Get(tag.Tenant, runID); err == nil {
		return existing, nil
	}
	project, client := p.placeOf(tag, edge)
	if err := p.admit(tag, edge, project, client); err != nil {
		return runs.Run{}, err
	}
	// Accepting publishes the run's first event before the run is recorded,
	// and delivering that event needs to know which client raised it.
	p.admitting.Store(runID, admitted{tenant: tag.Tenant, client: client})
	defer p.admitting.Delete(runID)
	if err := p.deliver.Register(runID, tag.Routes()); err != nil {
		p.Usage.Append(usage.Entry{Kind: usage.Ended, Tenant: tag.Tenant, RunID: runID, At: p.now()})
		return runs.Run{}, fmt.Errorf("platform: register routes: %w", err)
	}
	acc, err := p.rt.Accept(ctx, tag)
	if err != nil {
		// It never started, so it holds no place.
		p.Usage.Append(usage.Entry{Kind: usage.Ended, Tenant: tag.Tenant, RunID: runID, At: p.now()})
		return runs.Run{}, err
	}
	if err := p.Runs.Record(acc.RunID, tag, acc.Rev); err != nil {
		return runs.Run{}, err
	}
	return p.Runs.Get(tag.Tenant, acc.RunID)
}

// Call has one run call another agent. It is the only path by which an agent
// starts another agent's run.
//
// Three things are checked, in this order: the caller's pinned spec must name
// the target in its spawn allowlist, the tree's limits must allow the call, and
// the caller's remaining budget must cover the lease.
func (p *Platform) Call(ctx context.Context, tenant, runID string, req call.Request) (runs.Run, error) {
	p.calls.Lock()
	defer p.calls.Unlock()

	parent, err := p.Runs.Get(tenant, runID)
	if err != nil {
		return runs.Run{}, err
	}
	rev, err := p.Agents.Get(ctx, tenant, parent.Agent, parent.Rev)
	if err != nil {
		return runs.Run{}, fmt.Errorf("platform: caller's spec: %w", err)
	}
	if !slices.Contains(rev.Spec.Access.Spawn, req.Agent) {
		return runs.Run{}, fmt.Errorf("%w: %q may not call %q", ErrForbidden, parent.Agent, req.Agent)
	}

	tag, err := call.New(parent.Tag(), req, parent.State(), p.cfg.Limits, p.now())
	if err != nil {
		return runs.Run{}, err
	}
	// Policy is checked before liveness, so a refusal always names the rule
	// that refused it. A run that was asked to stop is over as far as new
	// work goes, from the moment it was asked.
	if parent.Status.Terminal() || parent.Stopping {
		if existing, err := p.Runs.Get(tenant, runtime.RunID(tenant, tag.ID)); err == nil {
			return existing, nil
		}
		if parent.Stopping {
			return runs.Run{}, fmt.Errorf("%w: run %q was asked to stop and cannot make calls", call.ErrInvalid, runID)
		}
		return runs.Run{}, fmt.Errorf("%w: run %q has ended and cannot make calls", call.ErrInvalid, runID)
	}
	// A replayed turn makes the same call again. It joins the run it already
	// started and is not counted twice.
	if existing, err := p.Runs.Get(tenant, runtime.RunID(tenant, tag.ID)); err == nil {
		return existing, nil
	}
	child, err := p.Invoke(ctx, tag)
	if err != nil {
		return runs.Run{}, err
	}
	p.Runs.Called(runID, req.Lease)
	return child, nil
}

// reserved reports whether an origin is one only the platform may use.
func reserved(origin string) bool {
	switch origin {
	case call.Origin, runs.OriginAPI, runs.OriginSystem, runs.OriginManual, OriginEval:
		return true
	}
	return false
}

// reconcileEvery is how often open runs are checked against the ledger.
const reconcileEvery = 2 * time.Second

// reconcile closes runs the ledger says are over but whose events never said so.
//
// The ledger is the authority on whether a run has ended. The usual way to
// learn it is the run's own completed or terminal-failed event, and this is the
// fallback for when there is none: a run that exhausts its retries is failed in
// the ledger with nothing further published.
func (p *Platform) reconcile(ctx context.Context) {
	for _, id := range p.Runs.Active() {
		rec, err := p.ledger.GetRun(ctx, id)
		if err != nil {
			continue
		}
		status := runs.Status("")
		switch rec.Status {
		case drun.StatusSucceeded:
			status = runs.Completed
		case drun.StatusFailed:
			status = runs.Failed
		default:
			continue
		}
		at := p.now()
		if !rec.CompletedAt.IsZero() {
			at = rec.CompletedAt
		}
		// A stopped run succeeds in the ledger: it ended and did not fail.
		// Its output says it was stopped, and what it had said.
		if status == runs.Completed {
			if out, err := p.rt.Result(ctx, id); err == nil && out.Stopped != nil {
				if run, ended := p.Runs.Stop(id, out.Text, out.Stopped.Reason, at); ended {
					p.log.Info("platform: run ended stopped in the ledger without a final event", "run", id)
					p.ended(ctx, run)
				}
				continue
			}
		}
		if run, ended := p.Runs.End(id, status, "", rec.Error, at); ended {
			p.log.Info("platform: run ended in the ledger without a final event", "run", id, "status", status)
			p.ended(ctx, run)
		}
	}
}

// raise checks a run that has just ended against its tenant's alert rules.
func (p *Platform) raise(run runs.Run) {
	spoken := false
	for _, a := range p.Alerts.Evaluate(run, p.Runs.Recent) {
		p.log.Warn("platform: alert", "rule", a.Rule, "severity", a.Severity, "title", a.Title, "run", a.RunID)
		p.inboxAlert(run.Tenant, a)
		spoken = spoken || a.RunID == run.ID
	}
	// A failure no rule was watching for still needs someone.
	if !spoken {
		p.inboxFailure(run)
	}
}

// Subscribe opens a stream of a tenant's events. The tenant is applied here,
// after the bus, because a subscriber never names a tenant: it is the caller's
// scope, not part of the address.
func (p *Platform) Subscribe(ctx context.Context, tenant string, sub envelope.Subscription) (*Stream, error) {
	s, err := p.broker.Subscribe(ctx, sub)
	if err != nil {
		return nil, err
	}
	return &Stream{inner: s, tenant: tenant}, nil
}

// Stream is one tenant's view of the bus.
type Stream struct {
	inner  bus.Stream
	tenant string
}

// Recv returns the next event in the stream's tenant.
func (s *Stream) Recv(ctx context.Context) (envelope.Event, error) {
	for {
		e, err := s.inner.Recv(ctx)
		if err != nil {
			return envelope.Event{}, err
		}
		// Requests addressed to runs are the runtime's traffic, not events
		// a reader of runs is owed.
		if e.Tenant == s.tenant && !e.Topic.IsControl() {
			return e, nil
		}
	}
}

// Close ends the stream.
func (s *Stream) Close() error { return s.inner.Close() }

// Run starts every long-lived loop and blocks until ctx is done or one fails.
func (p *Platform) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Subscribe before anything can publish, so the journal and the index
	// never miss the first event of the first run.
	everything := envelope.Subscription{Topic: topic.All()}
	observed, err := p.broker.Subscribe(ctx, everything)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("platform: observe: %w", err)
	}

	var wg sync.WaitGroup
	errc := make(chan error, 8)
	loop := func(name string, fn func() error) {
		wg.Go(func() {
			if err := fn(); err != nil && !errors.Is(err, context.Canceled) {
				errc <- fmt.Errorf("platform: %s: %w", name, err)
				cancel()
			}
		})
	}

	loop("router", func() error { return p.deliver.Run(ctx) })
	loop("connector flush", func() error { return p.flushConnections(ctx) })
	loop("sandbox sweep", func() error {
		tick := time.NewTicker(time.Hour)
		defer tick.Stop()
		for {
			p.sweepSandboxes()
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
			}
		}
	})
	loop("worker", func() error {
		return worker.New(p.ledger, p.queue,
			worker.WithRegistry(p.rt.Registry()),
			worker.WithConcurrency(p.slots()),
			worker.WithLogger(p.log),
		).Run(ctx)
	})
	loop("observer", func() error {
		defer func() { _ = observed.Close() }()
		for {
			e, err := observed.Recv(ctx)
			switch {
			case ctx.Err() != nil:
				return nil
			case errors.Is(err, bus.ErrSlowConsumer):
				// The bus cut this subscriber off rather than hold events
				// for it. Rejoin and carry on: the journal has a gap, which
				// is logged, and the platform keeps running.
				p.log.Warn("platform: observer fell behind and missed events")
				_ = observed.Close()
				if observed, err = p.broker.Subscribe(ctx, everything); err != nil {
					return err
				}
				continue
			case errors.Is(err, bus.ErrClosed):
				return nil
			case err != nil:
				return err
			}
			if e.Topic.IsControl() {
				// A request addressed to a run, such as a stop, not
				// something a run did. The run's own events say what came
				// of it.
				continue
			}
			p.Journal.Append(e)
			if run, ended := p.Runs.Observe(e); ended {
				p.ended(ctx, run)
			}
		}
	})

	loop("reconciler", func() error {
		tick := time.NewTicker(reconcileEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
				p.reconcile(ctx)
			}
		}
	})

	tags := make(chan envelope.Tag)
	for _, t := range p.sinks.Triggers() {
		loop("trigger "+t.Name(), func() error { return t.Ingest(ctx, tags) })
	}
	loop("tags", func() error {
		for {
			select {
			case <-ctx.Done():
				return nil
			case tag := <-tags:
				// A trigger's tag cannot claim an origin the platform keeps
				// for itself. Lineage is only ever minted by Call, and the
				// rest carry keys the index believes.
				if reserved(tag.Origin) {
					p.log.Warn("platform: dropped a trigger tag claiming a reserved origin", "tag", tag.ID, "origin", tag.Origin)
					continue
				}
				if r, err := p.Invoke(ctx, tag); err != nil {
					p.log.Error("platform: accept tag", "tag", tag.ID, "agent", tag.Agent, "error", err)
				} else {
					p.log.Info("platform: accepted", "run", r.ID, "agent", r.Agent, "origin", r.Origin)
				}
			}
		}
	})

	wg.Wait()
	close(errc)
	return <-errc
}
