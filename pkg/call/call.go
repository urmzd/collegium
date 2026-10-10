// Package call is the one way an agent invokes another agent.
//
// # One primitive
//
// A subagent, a handoff and a delegation are the same thing: a new run of
// another agent, in its own sandbox, raised as a Tag and linked to its caller by
// an Edge. They differ only in Mode, which decides where the answer goes and
// what the caller does next. Nothing else in the platform distinguishes them.
//
// # Detached
//
// The child is a durable run of its own. It has its own journal, its own
// context window and its own topic, so the caller's context holds a reference
// and a result rather than the child's work. Each branch compacts alone, and a
// tree of calls can spread across nodes.
//
// opentag's delegate tool is the attached counterpart: a sub-task inside the
// caller's turn with no run id and no stream. This package is the other shape
// its doc comment describes, the one that outlives the turn.
//
// # Governance rides the edge
//
// A call is refused before it reaches the bus when it would exceed the tree's
// depth, the caller's fan-out, or the caller's remaining budget. The zero
// Limits allows no calls at all.
//
// # Trust
//
// The edge travels in Tag.Meta, which triggers also write. EdgeOf therefore
// honors lineage only on a tag whose Origin is this package's, and treats every
// other tag as a root. The server must set Origin itself for that to hold, the
// same way it sets Tenant.
package call

import (
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/urmzd/opentag/pkg/envelope"
)

// Origin marks a tag raised by an agent rather than by a trigger.
const Origin = "agent"

// metaPrefix namespaces the edge inside Tag.Meta.
const metaPrefix = "oap."

const (
	metaRoot   = metaPrefix + "root"
	metaParent = metaPrefix + "parent"
	metaDepth  = metaPrefix + "depth"
	metaMode   = metaPrefix + "mode"
	metaLease  = metaPrefix + "lease"
)

// Sentinel errors. A refusal is the policy speaking: surface it to the model as
// a tool error so it can say so, instead of failing the turn.
var (
	ErrInvalid = errors.New("call: invalid")
	ErrDepth   = errors.New("call: depth limit reached")
	ErrFanOut  = errors.New("call: fan-out limit reached")
	ErrBudget  = errors.New("call: budget exceeded")
)

// Mode says where a call's answer goes and what the caller does next.
type Mode string

const (
	// Subagent parks the caller until the child answers. The answer returns
	// to the caller.
	Subagent Mode = "subagent"
	// Handoff ends the caller. The child answers wherever the caller would
	// have, so it inherits the caller's source, routes and trigger context.
	Handoff Mode = "handoff"
	// Delegate lets the caller keep working. The answer arrives later.
	Delegate Mode = "delegate"
)

func (m Mode) valid() bool {
	return m == Subagent || m == Handoff || m == Delegate
}

// Edge links a run to the run that called it.
type Edge struct {
	// Root is the run at the top of the tree. Budgets, traces and
	// cancellation are keyed by it.
	Root string
	// Parent is the calling run. Empty on a root.
	Parent string
	// Depth is the distance from the root, which is 0.
	Depth int
	// Mode is how this run was called. Empty on a root.
	Mode Mode
	// Lease is the most this run may spend, in micro-USD, including
	// everything it calls. Zero means unmetered.
	Lease int64
}

// IsRoot reports whether the run was raised by a trigger rather than an agent.
func (e Edge) IsRoot() bool { return e.Parent == "" }

// EdgeOf reads a tag's edge. A tag raised by a trigger is a root, whatever its
// Meta claims. A tag raised by an agent must carry a complete edge.
func EdgeOf(t envelope.Tag) (Edge, error) {
	if t.Origin != Origin {
		return Edge{Root: t.ID}, nil
	}
	e := Edge{
		Root:   t.Meta[metaRoot],
		Parent: t.Meta[metaParent],
		Mode:   Mode(t.Meta[metaMode]),
	}
	if e.Root == "" || e.Parent == "" {
		return Edge{}, fmt.Errorf("%w: tag %q is from an agent but names no root or parent", ErrInvalid, t.ID)
	}
	if !e.Mode.valid() {
		return Edge{}, fmt.Errorf("%w: tag %q has mode %q", ErrInvalid, t.ID, e.Mode)
	}
	depth, err := strconv.Atoi(t.Meta[metaDepth])
	if err != nil || depth < 1 {
		return Edge{}, fmt.Errorf("%w: tag %q has depth %q", ErrInvalid, t.ID, t.Meta[metaDepth])
	}
	e.Depth = depth
	lease, err := strconv.ParseInt(t.Meta[metaLease], 10, 64)
	if err != nil || lease < 0 {
		return Edge{}, fmt.Errorf("%w: tag %q has lease %q", ErrInvalid, t.ID, t.Meta[metaLease])
	}
	e.Lease = lease
	return e, nil
}

// Limits bounds a tree of calls. The zero value allows none.
type Limits struct {
	// MaxDepth is the deepest a call may land. 1 lets a root call agents
	// that may not call further.
	MaxDepth int
	// MaxFanOut is the most calls one run may make.
	MaxFanOut int
}

// State is what the caller has used so far. The platform's ledger supplies it.
type State struct {
	// Calls is how many calls this run has already made.
	Calls int
	// Spent is what this run has spent so far, in micro-USD, including the
	// leases it has already granted.
	Spent int64
}

// Request is one agent asking for another.
type Request struct {
	// Key identifies the call within the caller's run, typically the tool
	// call id. It makes the child's id deterministic, so a replayed turn
	// joins the run it already started instead of starting a second one.
	Key string
	// Agent is the agent being called.
	Agent string
	// Text is the request.
	Text string
	Mode Mode
	// Lease is the budget granted to the child, in micro-USD. A metered
	// caller must grant one, and cannot grant more than it has left.
	Lease int64
}

// New builds the tag that starts the child run, or refuses the call.
func New(parent envelope.Tag, req Request, st State, lim Limits, at time.Time) (envelope.Tag, error) {
	switch {
	case strings.TrimSpace(req.Key) == "":
		return envelope.Tag{}, fmt.Errorf("%w: call has no key, which is its idempotency key", ErrInvalid)
	case req.Agent == "":
		return envelope.Tag{}, fmt.Errorf("%w: call names no agent", ErrInvalid)
	case !req.Mode.valid():
		return envelope.Tag{}, fmt.Errorf("%w: call has mode %q", ErrInvalid, req.Mode)
	case req.Lease < 0:
		return envelope.Tag{}, fmt.Errorf("%w: call has a negative lease", ErrInvalid)
	}
	edge, err := EdgeOf(parent)
	if err != nil {
		return envelope.Tag{}, err
	}
	if edge.Depth+1 > lim.MaxDepth {
		return envelope.Tag{}, fmt.Errorf("%w: %q is at depth %d of %d", ErrDepth, parent.Agent, edge.Depth, lim.MaxDepth)
	}
	if st.Calls >= lim.MaxFanOut {
		return envelope.Tag{}, fmt.Errorf("%w: %q has made %d of %d calls", ErrFanOut, parent.Agent, st.Calls, lim.MaxFanOut)
	}
	if edge.Lease > 0 {
		left := edge.Lease - st.Spent
		if req.Lease == 0 {
			return envelope.Tag{}, fmt.Errorf("%w: %q is metered, so its calls must carry a lease", ErrBudget, parent.Agent)
		}
		if req.Lease > left {
			return envelope.Tag{}, fmt.Errorf("%w: %q asked to grant %d with %d left", ErrBudget, parent.Agent, req.Lease, left)
		}
	}

	child := envelope.Tag{
		ID:     parent.ID + "/" + req.Key,
		Tenant: parent.Tenant,
		Agent:  req.Agent,
		Origin: Origin,
		Text:   req.Text,
		Actor:  envelope.Actor{ID: parent.Agent, Display: parent.Agent, Bot: true},
		Meta:   map[string]string{},
		At:     at,
	}
	if req.Mode == Handoff {
		child.Source = parent.Source
		child.Deliver = parent.Deliver
		maps.Copy(child.Meta, parent.Meta)
	}
	child.Meta[metaRoot] = edge.Root
	child.Meta[metaParent] = parent.ID
	child.Meta[metaDepth] = strconv.Itoa(edge.Depth + 1)
	child.Meta[metaMode] = string(req.Mode)
	child.Meta[metaLease] = strconv.FormatInt(req.Lease, 10)
	return child, nil
}
