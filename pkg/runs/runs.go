// Package runs is the platform's index of runs and the tree they form.
//
// opentag's ledger knows whether a run survived. This index knows how runs
// relate: which run called which, how many calls each has made, and how much
// budget each has handed out. It is a projection of two inputs, the tag a run
// was accepted with and the lifecycle events that run later publishes.
//
// Index is in memory. It is rebuilt empty on restart, which is acceptable for a
// single process and is the reason a Postgres implementation is planned.
package runs

import (
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/urmzd/opentag/pkg/agentrt/payload"
	"github.com/urmzd/opentag/pkg/envelope"
	"github.com/urmzd/opentag/pkg/runtime"

	"github.com/urmzd/open-agent-platform/pkg/call"
)

// ErrNotFound reports a run that does not exist in the caller's tenant. A run
// in another tenant is reported the same way, so the answer never confirms that
// it exists.
var ErrNotFound = errors.New("runs: not found")

// OriginManual marks a schedule fired by a person rather than by the clock.
const OriginManual = "manual"

// OriginSystem marks a run the platform raised for its own housekeeping, such
// as naming a conversation. Nothing outside the platform can raise one.
const OriginSystem = "system"

// OriginAPI marks a run a caller asked for through the platform's API. It is
// the only origin whose conversation keys are believed.
const OriginAPI = "api"

// Keys the platform's API writes into a tag's Meta to place a run in a
// conversation. A tag from any other origin carrying them is ignored.
const (
	MetaThread  = "oap.thread"
	MetaPrev    = "oap.prev"
	MetaAsk     = "oap.ask"
	MetaPicked  = "oap.picked"
	MetaProject = "oap.project"
)

// Status is where a run is in its life.
type Status string

const (
	Accepted  Status = "accepted"
	Running   Status = "running"
	Parked    Status = "parked"
	Completed Status = "completed"
	Failed    Status = "failed"
)

// Terminal reports whether the run has ended.
func (s Status) Terminal() bool { return s == Completed || s == Failed }

// Run is one run and its place in the tree.
type Run struct {
	ID     string         `json:"id"`
	Tenant string         `json:"tenant"`
	Agent  string         `json:"agent"`
	Rev    int            `json:"rev"`
	Origin string         `json:"origin"`
	Text   string         `json:"text"`
	Actor  envelope.Actor `json:"actor,omitzero"`

	// Root, Parent, Depth and Mode are the edge the run was called with.
	Root   string    `json:"root"`
	Parent string    `json:"parent,omitempty"`
	Depth  int       `json:"depth"`
	Mode   call.Mode `json:"mode,omitempty"`
	// Schedule names the schedule that raised the run, if one did.
	Schedule string `json:"schedule,omitempty"`

	// Thread is the conversation the run belongs to. A run that starts a
	// conversation is its own thread.
	Thread string `json:"thread"`
	// Prev is the turn this one follows. Two runs with the same Prev are
	// branches: different answers to the same point in the conversation.
	Prev string `json:"prev,omitempty"`
	// Picked says why the agent was chosen, when the caller left it to the
	// platform.
	Picked string `json:"picked,omitempty"`
	// Project is the project the conversation is filed under, if any.
	Project string `json:"project,omitempty"`

	// Lease is the run's budget in micro-USD. Zero means unmetered.
	Lease int64 `json:"lease"`
	// Granted is how much of the lease the run has handed to its calls.
	Granted int64 `json:"granted"`
	// Calls is how many calls the run has made.
	Calls int `json:"calls"`

	Status Status `json:"status"`
	// Answer is the final text of a completed run.
	Answer string `json:"answer,omitempty"`
	Error  string `json:"error,omitempty"`
	// Reason says what a parked run is waiting for.
	Reason string `json:"reason,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// tag is what the run was accepted with. A call from this run is built
	// from it.
	tag envelope.Tag
}

// Tag returns the tag the run was accepted with.
func (r Run) Tag() envelope.Tag { return r.tag }

// State is what the run has used so far, for call.New.
func (r Run) State() call.State { return call.State{Calls: r.Calls, Spent: r.Granted} }

// Index holds every run this process has accepted.
type Index struct {
	mu   sync.RWMutex
	runs map[string]*Run
	// order is run ids, oldest first.
	order []string
	// titles names conversations, by tenant and thread.
	titles map[string]string
}

// NewIndex returns an empty index.
func NewIndex() *Index { return &Index{runs: map[string]*Run{}, titles: map[string]string{}} }

// SetTitle names a conversation. A conversation with no name is shown by the
// first thing that was asked in it.
func (x *Index) SetTitle(tenant, thread, title string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.titles[tenant+"\x00"+thread] = title
}

// Record adds an accepted run. Recording a run that is already known changes
// nothing, because accepting the same tag twice joins the existing run.
func (x *Index) Record(runID string, tag envelope.Tag, rev int) error {
	edge, err := call.EdgeOf(tag)
	if err != nil {
		return err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if _, ok := x.runs[runID]; ok {
		return nil
	}
	// The edge names tags. Runs are indexed by run id, which opentag derives
	// from the tag id, so the links are translated once, here.
	parent := ""
	if edge.Parent != "" {
		parent = runtime.RunID(edge.Parent)
	}
	// Only the cron trigger and the platform's own "run now" set this key. A
	// tag from anywhere else does not get to claim a schedule.
	schedule := ""
	if tag.Origin == "cron" || tag.Origin == OriginManual {
		schedule = tag.Meta["cron_schedule"]
	}
	// A run is its own conversation unless the API placed it in one. A run
	// called by an agent belongs to its root's conversation.
	thread, prev, picked, text, project := runID, "", "", tag.Text, ""
	if tag.Origin == OriginAPI {
		project = tag.Meta[MetaProject]
		if v := tag.Meta[MetaThread]; v != "" {
			thread = v
		}
		prev, picked = tag.Meta[MetaPrev], tag.Meta[MetaPicked]
		// The agent is sent the conversation so far. What the person
		// actually typed is kept apart, and is what the record shows.
		if v := tag.Meta[MetaAsk]; v != "" {
			text = v
		}
	}
	if root, ok := x.runs[runtime.RunID(edge.Root)]; ok && parent != "" {
		thread, project = root.Thread, root.Project
	}
	x.runs[runID] = &Run{
		ID: runID, Tenant: tag.Tenant, Agent: tag.Agent, Rev: rev, Origin: tag.Origin,
		Text: text, Actor: tag.Actor, Thread: thread, Prev: prev, Picked: picked, Project: project,
		Root: runtime.RunID(edge.Root), Parent: parent, Depth: edge.Depth, Mode: edge.Mode, Lease: edge.Lease, Schedule: schedule,
		Status: Accepted, CreatedAt: tag.At, UpdatedAt: tag.At,
		tag: tag,
	}
	x.order = append(x.order, runID)
	return nil
}

// Called records that a run made a call and granted it a lease.
func (x *Index) Called(runID string, lease int64) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if r, ok := x.runs[runID]; ok {
		r.Calls++
		r.Granted += lease
	}
}

// Observe folds a lifecycle event into the run it belongs to. It returns the
// run as it now stands and whether this event is the one that ended it, so a
// caller can react to an ending exactly once. Other kinds, and events for runs
// this index never recorded, change nothing.
func (x *Index) Observe(e envelope.Event) (Run, bool) {
	if e.Kind.Family() != "lifecycle" {
		return Run{}, false
	}
	body, err := payload.Decode[payload.Lifecycle](e)
	if err != nil {
		return Run{}, false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	r, ok := x.runs[e.RunID]
	if !ok || r.Tenant != e.Tenant || r.Status.Terminal() {
		return Run{}, false
	}
	r.UpdatedAt = e.At
	switch e.Kind {
	case envelope.KindStarted, envelope.KindResumed:
		r.Status, r.Reason = Running, ""
	case envelope.KindParked:
		r.Status, r.Reason = Parked, body.Reason
	case envelope.KindCompleted:
		r.Status, r.Answer = Completed, body.Text
	case envelope.KindFailed:
		// A failed attempt may still be retried. Only a terminal failure
		// ends the run.
		r.Error = body.Error
		if body.Terminal {
			r.Status = Failed
		}
	}
	return *r, r.Status.Terminal()
}

// Active returns the ids of runs that have not ended.
func (x *Index) Active() []string {
	x.mu.RLock()
	defer x.mu.RUnlock()
	var out []string
	for _, id := range x.order {
		if !x.runs[id].Status.Terminal() {
			out = append(out, id)
		}
	}
	return out
}

// End closes a run from a source other than its own events. It returns the run
// and whether this call is what ended it, so an ending is still acted on once.
//
// The bus reports what each attempt did. Whether the run is over is the durable
// ledger's call, and the two can disagree: a run that exhausts its retries is
// failed in the ledger without a final event ever being published.
func (x *Index) End(runID string, status Status, answer, failure string, at time.Time) (Run, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	r, ok := x.runs[runID]
	if !ok || r.Status.Terminal() || !status.Terminal() {
		return Run{}, false
	}
	r.Status, r.UpdatedAt = status, at
	if answer != "" {
		r.Answer = answer
	}
	if failure != "" {
		r.Error = failure
	}
	return *r, true
}

// Recent counts an agent's runs that started since a time, and how many of
// those failed. An empty agent counts every agent.
func (x *Index) Recent(tenant, agent string, since time.Time) (total, failed int) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	for _, id := range x.order {
		r := x.runs[id]
		if r.Tenant != tenant || r.CreatedAt.Before(since) || (agent != "" && r.Agent != agent) {
			continue
		}
		total++
		if r.Status == Failed {
			failed++
		}
	}
	return total, failed
}

// Get returns one run in a tenant.
func (x *Index) Get(tenant, runID string) (Run, error) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	r, ok := x.runs[runID]
	if !ok || r.Tenant != tenant {
		return Run{}, ErrNotFound
	}
	return *r, nil
}

// List returns a tenant's runs, newest first, at most limit of them.
func (x *Index) List(tenant string, limit int) []Run {
	x.mu.RLock()
	defer x.mu.RUnlock()
	var out []Run
	for _, id := range slices.Backward(x.order) {
		if r := x.runs[id]; r.Tenant == tenant {
			out = append(out, *r)
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out
}

// Tree returns every run under the same root as runID, oldest first, so a
// parent always precedes its children.
func (x *Index) Tree(tenant, runID string) ([]Run, error) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	r, ok := x.runs[runID]
	if !ok || r.Tenant != tenant {
		return nil, ErrNotFound
	}
	var out []Run
	for _, id := range x.order {
		if c := x.runs[id]; c.Tenant == tenant && c.Root == r.Root {
			out = append(out, *c)
		}
	}
	return out, nil
}

// ThreadSummary is one conversation at a glance.
type ThreadSummary struct {
	ID string `json:"id"`
	// Title is the conversation's name, or the first thing that was asked
	// when it has not been named.
	Title string `json:"title"`
	// Named reports whether the title is a name rather than the first message.
	Named   bool   `json:"named"`
	Origin  string `json:"origin"`
	Project string `json:"project,omitempty"`
	// Agents is every agent that has answered in it, in order of first reply.
	Agents []string `json:"agents"`
	Turns  int      `json:"turns"`
	// Branches is how many turns have more than one answer.
	Branches int `json:"branches"`
	// Active is how many of its runs have not ended.
	Active    int       `json:"active"`
	Failed    int       `json:"failed"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Conversation returns the turns of a thread, oldest first. Runs an agent
// called along the way are not turns and are left out: they hang off a turn's
// call tree.
func (x *Index) Conversation(tenant, thread string) ([]Run, error) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	var out []Run
	for _, id := range x.order {
		if r := x.runs[id]; r.Tenant == tenant && r.Thread == thread && r.Parent == "" {
			out = append(out, *r)
		}
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}

// Path returns the turns from the start of a conversation down to a run,
// oldest first: the one branch that run sits on.
func (x *Index) Path(tenant, runID string) ([]Run, error) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	var out []Run
	seen := map[string]bool{}
	for id := runID; id != "" && !seen[id]; {
		r, ok := x.runs[id]
		if !ok || r.Tenant != tenant {
			break
		}
		seen[id] = true
		out = append(out, *r)
		id = r.Prev
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	slices.Reverse(out)
	return out, nil
}

// Threads returns a tenant's conversations, most recently active first.
func (x *Index) Threads(tenant string, limit int) []ThreadSummary {
	x.mu.RLock()
	defer x.mu.RUnlock()
	byID := map[string]*ThreadSummary{}
	answers := map[string]int{}
	var order []string
	for _, id := range x.order {
		r := x.runs[id]
		if r.Tenant != tenant {
			continue
		}
		s, ok := byID[r.Thread]
		if !ok {
			s = &ThreadSummary{ID: r.Thread, Title: r.Text, Origin: r.Origin, Project: r.Project, CreatedAt: r.CreatedAt}
			byID[r.Thread] = s
			order = append(order, r.Thread)
		}
		if !r.Status.Terminal() {
			s.Active++
		}
		if r.Status == Failed {
			s.Failed++
		}
		if r.UpdatedAt.After(s.UpdatedAt) {
			s.UpdatedAt = r.UpdatedAt
		}
		if r.Parent != "" {
			continue
		}
		s.Turns++
		if !slices.Contains(s.Agents, r.Agent) {
			s.Agents = append(s.Agents, r.Agent)
		}
		// A second answer at the same point makes that point a branch.
		key := r.Thread + "\x00" + r.Prev
		if answers[key]++; answers[key] == 2 {
			s.Branches++
		}
	}
	out := make([]ThreadSummary, 0, len(order))
	for _, id := range order {
		s := *byID[id]
		if title := x.titles[tenant+"\x00"+id]; title != "" {
			s.Title, s.Named = title, true
		}
		out = append(out, s)
	}
	slices.SortStableFunc(out, func(a, b ThreadSummary) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
