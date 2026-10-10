package call

import (
	"errors"
	"testing"
	"time"

	"github.com/urmzd/opentag/pkg/address"
	"github.com/urmzd/opentag/pkg/envelope"
)

var (
	now  = time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	open = Limits{MaxDepth: 3, MaxFanOut: 4}
)

func root(t *testing.T) envelope.Tag {
	t.Helper()
	src, err := address.Parse("github://urmzd/opentag/issues/42")
	if err != nil {
		t.Fatal(err)
	}
	return envelope.Tag{
		ID:     "evt-1",
		Tenant: "acme",
		Agent:  "triage",
		Origin: "github",
		Source: src,
		Text:   "look at this",
		Actor:  envelope.Actor{ID: "urmzd"},
		Meta:   map[string]string{"issue": "42"},
		At:     now,
	}
}

func mustCall(t *testing.T, parent envelope.Tag, req Request) envelope.Tag {
	t.Helper()
	child, err := New(parent, req, State{}, open, now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := child.Validate(); err != nil {
		t.Fatalf("child tag is not a valid tag: %v", err)
	}
	return child
}

func TestRootEdge(t *testing.T) {
	e, err := EdgeOf(root(t))
	if err != nil {
		t.Fatal(err)
	}
	if !e.IsRoot() || e.Root != "evt-1" || e.Depth != 0 {
		t.Fatalf("got %+v", e)
	}
}

// A trigger writes Meta too, so lineage in a trigger's tag must not be believed.
func TestTriggerCannotForgeAnEdge(t *testing.T) {
	forged := root(t)
	forged.Meta = map[string]string{
		metaRoot: "someone-elses-tree", metaParent: "p", metaDepth: "1",
		metaMode: string(Subagent), metaLease: "999999999",
	}
	e, err := EdgeOf(forged)
	if err != nil {
		t.Fatal(err)
	}
	if !e.IsRoot() || e.Root != forged.ID || e.Lease != 0 {
		t.Fatalf("forged lineage was honored: %+v", e)
	}
}

func TestAgentTagWithoutAnEdgeFailsClosed(t *testing.T) {
	bare := root(t)
	bare.Origin = Origin
	if _, err := EdgeOf(bare); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
}

func TestChildCarriesTheEdge(t *testing.T) {
	parent := root(t)
	child := mustCall(t, parent, Request{Key: "call-1", Agent: "coder", Text: "fix it", Mode: Subagent})
	grandchild := mustCall(t, child, Request{Key: "call-9", Agent: "tester", Text: "run it", Mode: Delegate})

	e, err := EdgeOf(grandchild)
	if err != nil {
		t.Fatal(err)
	}
	want := Edge{Root: parent.ID, Parent: child.ID, Depth: 2, Mode: Delegate}
	if e != want {
		t.Fatalf("got %+v, want %+v", e, want)
	}
	if child.Tenant != parent.Tenant {
		t.Fatalf("tenant changed across a call: %q", child.Tenant)
	}
	if !child.Actor.Bot || child.Actor.ID != parent.Agent {
		t.Fatalf("child's actor should be the calling agent, got %+v", child.Actor)
	}
}

// A replayed turn must join the run it already started.
func TestSameKeyIsTheSameRun(t *testing.T) {
	req := Request{Key: "call-1", Agent: "coder", Text: "fix it", Mode: Subagent}
	a := mustCall(t, root(t), req)
	b := mustCall(t, root(t), req)
	if a.ID != b.ID {
		t.Fatalf("%q != %q", a.ID, b.ID)
	}
}

func TestOnlyAHandoffInheritsTheSurface(t *testing.T) {
	parent := root(t)
	for _, tc := range []struct {
		mode    Mode
		inherit bool
	}{{Subagent, false}, {Delegate, false}, {Handoff, true}} {
		child := mustCall(t, parent, Request{Key: "k", Agent: "coder", Mode: tc.mode})
		if got := !child.Source.IsZero(); got != tc.inherit {
			t.Errorf("%s: inherits source = %v, want %v", tc.mode, got, tc.inherit)
		}
		if _, got := child.Meta["issue"]; got != tc.inherit {
			t.Errorf("%s: inherits trigger context = %v, want %v", tc.mode, got, tc.inherit)
		}
	}
}

func TestHandoffDoesNotMutateTheParent(t *testing.T) {
	parent := root(t)
	mustCall(t, parent, Request{Key: "k", Agent: "coder", Mode: Handoff})
	if len(parent.Meta) != 1 {
		t.Fatalf("parent meta was written to: %v", parent.Meta)
	}
}

func TestRefusals(t *testing.T) {
	metered := func(t *testing.T) envelope.Tag {
		child, err := New(root(t), Request{Key: "m", Agent: "coder", Mode: Subagent, Lease: 1000}, State{}, open, now)
		if err != nil {
			t.Fatal(err)
		}
		return child
	}
	ok := Request{Key: "k", Agent: "tester", Mode: Subagent}

	for _, tc := range []struct {
		name   string
		parent func(*testing.T) envelope.Tag
		req    Request
		st     State
		lim    Limits
		want   error
	}{
		{"zero limits allow nothing", root, ok, State{}, Limits{}, ErrDepth},
		{"too deep", metered, Request{Key: "k", Agent: "tester", Mode: Subagent, Lease: 1}, State{}, Limits{MaxDepth: 1, MaxFanOut: 4}, ErrDepth},
		{"too wide", root, ok, State{Calls: 4}, open, ErrFanOut},
		{"metered caller must grant a lease", metered, ok, State{}, open, ErrBudget},
		{"lease above what is left", metered, Request{Key: "k", Agent: "tester", Mode: Subagent, Lease: 600}, State{Spent: 500}, open, ErrBudget},
		{"no key", root, Request{Agent: "tester", Mode: Subagent}, State{}, open, ErrInvalid},
		{"no agent", root, Request{Key: "k", Mode: Subagent}, State{}, open, ErrInvalid},
		{"unknown mode", root, Request{Key: "k", Agent: "tester", Mode: "fork"}, State{}, open, ErrInvalid},
		{"negative lease", root, Request{Key: "k", Agent: "tester", Mode: Subagent, Lease: -1}, State{}, open, ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.parent(t), tc.req, tc.st, tc.lim, now); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestLeaseWithinWhatIsLeft(t *testing.T) {
	parent, err := New(root(t), Request{Key: "m", Agent: "coder", Mode: Subagent, Lease: 1000}, State{}, open, now)
	if err != nil {
		t.Fatal(err)
	}
	child, err := New(parent, Request{Key: "k", Agent: "tester", Mode: Subagent, Lease: 500}, State{Spent: 500}, open, now)
	if err != nil {
		t.Fatal(err)
	}
	e, err := EdgeOf(child)
	if err != nil {
		t.Fatal(err)
	}
	if e.Lease != 500 {
		t.Fatalf("lease = %d", e.Lease)
	}
}
