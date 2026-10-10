// Package journal is the platform's record of what happened and when.
//
// Every event a run publishes is appended here, so three questions have one
// answer: what did this tenant's agents do (Query), what happened inside this
// run and the runs it called (Trace), and in what order.
//
// The journal is a bounded ring in memory. When it is full the oldest events
// are dropped, and it is empty after a restart. That bound is the reason a
// durable store is planned: this answers "what happened in the last while", not
// "what happened last quarter".
package journal

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/urmzd/opentag/pkg/agentrt/payload"
	"github.com/urmzd/opentag/pkg/envelope"
)

// DefaultCapacity is how many events a journal keeps.
const DefaultCapacity = 100_000

// maxDetail bounds the text an entry carries, so a query result stays small
// however long an answer was.
const maxDetail = 240

// Journal holds the most recent events across every tenant. Reads are always
// confined to one tenant.
type Journal struct {
	mu     sync.RWMutex
	events []envelope.Event
	head   int
	full   bool
}

// New returns a journal that keeps the last capacity events.
func New(capacity int) *Journal {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Journal{events: make([]envelope.Event, capacity)}
}

// Append records an event.
func (j *Journal) Append(e envelope.Event) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.events[j.head] = e
	j.head = (j.head + 1) % len(j.events)
	if j.head == 0 {
		j.full = true
	}
}

// each visits events oldest first. The caller holds the read lock.
func (j *Journal) each(fn func(envelope.Event)) {
	if j.full {
		for _, e := range j.events[j.head:] {
			fn(e)
		}
	}
	for _, e := range j.events[:j.head] {
		fn(e)
	}
}

// Events returns the events of the named runs in a tenant, oldest first, as
// they were published. It is what the journal still holds: a run whose first
// events have left the ring comes back without them.
func (j *Journal) Events(tenant string, runIDs []string) []envelope.Event {
	j.mu.RLock()
	defer j.mu.RUnlock()
	var out []envelope.Event
	j.each(func(e envelope.Event) {
		if e.Tenant == tenant && slices.Contains(runIDs, e.RunID) {
			out = append(out, e)
		}
	})
	return out
}

// Query selects events. Tenant is required. Every other field narrows, and an
// empty field matches everything.
type Query struct {
	Tenant string
	Agent  string
	RunID  string
	// Kind is an exact kind ("delta.tool.call") or a family ("lifecycle").
	Kind   string
	Origin string
	// Contains matches a substring of the entry's detail, ignoring case.
	Contains string
	Since    time.Time
	Until    time.Time
	// Limit caps the result. Zero means DefaultLimit.
	Limit int
}

// DefaultLimit and MaxLimit bound a query's result.
const (
	DefaultLimit = 200
	MaxLimit     = 2000
)

// Entry is one event as a person reads it: the envelope's fields plus a short
// plain-text account of its body.
type Entry struct {
	Seq    uint64        `json:"seq"`
	RunID  string        `json:"run_id"`
	Agent  string        `json:"agent"`
	Rev    int           `json:"rev"`
	Origin string        `json:"origin"`
	Kind   envelope.Kind `json:"kind"`
	At     time.Time     `json:"at"`
	Detail string        `json:"detail,omitempty"`
}

// Query returns matching entries, newest first.
func (j *Journal) Query(q Query) []Entry {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	needle := strings.ToLower(q.Contains)

	j.mu.RLock()
	defer j.mu.RUnlock()
	var out []Entry
	j.each(func(e envelope.Event) {
		switch {
		case q.Tenant == "" || e.Tenant != q.Tenant:
			return
		case q.Agent != "" && e.Agent != q.Agent:
			return
		case q.RunID != "" && e.RunID != q.RunID:
			return
		case q.Origin != "" && e.Origin != q.Origin:
			return
		case q.Kind != "" && !e.Kind.Matches(q.Kind):
			return
		case !q.Since.IsZero() && e.At.Before(q.Since):
			return
		case !q.Until.IsZero() && e.At.After(q.Until):
			return
		}
		entry := entryOf(e)
		if needle != "" && !strings.Contains(strings.ToLower(entry.Detail), needle) {
			return
		}
		out = append(out, entry)
	})
	slices.Reverse(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func entryOf(e envelope.Event) Entry {
	return Entry{
		Seq: e.Seq, RunID: e.RunID, Agent: e.Agent, Rev: e.Rev, Origin: e.Origin,
		Kind: e.Kind, At: e.At, Detail: clip(detail(e)),
	}
}

// detail says what an event's body means, in a line.
func detail(e envelope.Event) string {
	switch e.Kind {
	case envelope.KindText:
		b, _ := payload.Decode[payload.Text](e)
		return b.Text
	case envelope.KindThinking:
		b, _ := payload.Decode[payload.Thinking](e)
		return b.Text
	case envelope.KindToolCall:
		b, _ := payload.Decode[payload.ToolCall](e)
		return b.Name + " " + b.Phase
	case envelope.KindToolDone:
		b, _ := payload.Decode[payload.ToolDone](e)
		if b.Error != "" {
			return b.Name + " failed: " + b.Error
		}
		return b.Name + " done"
	case envelope.KindCitation:
		b, _ := payload.Decode[payload.Citation](e)
		return b.Source.DocumentTitle
	case envelope.KindActionTaken:
		b, _ := payload.Decode[payload.Action](e)
		if b.Error != "" {
			return b.Name + " failed: " + b.Error
		}
		return b.Name + " " + b.Summary
	}
	if e.Kind.Family() == "lifecycle" {
		b, _ := payload.Decode[payload.Lifecycle](e)
		switch {
		case b.Error != "":
			return Cause(b.Error)
		case b.Reason != "":
			return b.Reason
		case b.Text != "":
			return b.Text
		}
	}
	return ""
}

// Cause returns the last line of an error. Runtime errors nest one wrapper per
// line, so the first line names the run and the last one says what went wrong.
func Cause(err string) string {
	lines := strings.Split(strings.TrimSpace(err), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxDetail {
		return s
	}
	// Cut on a rune boundary.
	cut := maxDetail
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// SpanKind says what a span measures.
type SpanKind string

const (
	// SpanRun is a whole run, from accepted to its last event.
	SpanRun SpanKind = "run"
	// SpanAttempt is one execution attempt inside a run.
	SpanAttempt SpanKind = "attempt"
	// SpanTool is one tool call, from requested to done.
	SpanTool SpanKind = "tool"
	// SpanOutput is the window in which the answer streamed.
	SpanOutput SpanKind = "output"
	// SpanParked is time the run spent waiting on something outside itself.
	SpanParked SpanKind = "parked"
)

// Span is an interval inside a run. A span with no End is still open.
type Span struct {
	RunID string    `json:"run_id"`
	Agent string    `json:"agent"`
	Kind  SpanKind  `json:"kind"`
	Name  string    `json:"name"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end,omitzero"`
	// Error is set when the span ended in a failure.
	Error string `json:"error,omitempty"`
}

// Trace rebuilds the spans of the given runs from their events, ordered by
// start. Runs whose events have left the journal contribute nothing.
func (j *Journal) Trace(tenant string, runIDs []string) []Span {
	want := make(map[string]bool, len(runIDs))
	for _, id := range runIDs {
		want[id] = true
	}

	type open struct {
		run, attempt, output, parked *Span
		tools                        map[string]*Span
	}
	state := map[string]*open{}
	var spans []*Span
	start := func(e envelope.Event, kind SpanKind, name string) *Span {
		s := &Span{RunID: e.RunID, Agent: e.Agent, Kind: kind, Name: name, Start: e.At}
		spans = append(spans, s)
		return s
	}
	end := func(s **Span, at time.Time, err string) {
		if *s != nil {
			(*s).End, (*s).Error = at, err
			*s = nil
		}
	}

	j.mu.RLock()
	j.each(func(e envelope.Event) {
		if e.Tenant != tenant || !want[e.RunID] {
			return
		}
		o := state[e.RunID]
		if o == nil {
			o = &open{tools: map[string]*Span{}}
			state[e.RunID] = o
		}
		if o.run == nil && e.Kind != envelope.KindAccepted {
			// The accepted event fell out of the journal. Open the run at
			// the first event still held, rather than drop the run.
			o.run = start(e, SpanRun, e.Agent)
		}
		switch e.Kind {
		case envelope.KindAccepted:
			if o.run == nil {
				o.run = start(e, SpanRun, e.Agent)
			}
		case envelope.KindStarted, envelope.KindResumed:
			end(&o.parked, e.At, "")
			end(&o.attempt, e.At, "")
			b, _ := payload.Decode[payload.Lifecycle](e)
			name := "attempt"
			if b.Attempt > 0 {
				name = "attempt " + itoa(b.Attempt)
			}
			o.attempt = start(e, SpanAttempt, name)
		case envelope.KindParked:
			b, _ := payload.Decode[payload.Lifecycle](e)
			end(&o.output, e.At, "")
			end(&o.attempt, e.At, "")
			name := "waiting"
			if b.Reason != "" {
				name = "waiting: " + b.Reason
			}
			o.parked = start(e, SpanParked, name)
		case envelope.KindText:
			if o.output == nil {
				o.output = start(e, SpanOutput, "answer")
			}
			o.output.End = e.At
		case envelope.KindToolCall:
			b, _ := payload.Decode[payload.ToolCall](e)
			if _, seen := o.tools[b.ID]; !seen && b.ID != "" {
				o.tools[b.ID] = start(e, SpanTool, b.Name)
			}
		case envelope.KindToolDone:
			b, _ := payload.Decode[payload.ToolDone](e)
			if s := o.tools[b.ID]; s != nil {
				s.End, s.Error = e.At, b.Error
				delete(o.tools, b.ID)
			}
		case envelope.KindCompleted, envelope.KindFailed:
			b, _ := payload.Decode[payload.Lifecycle](e)
			o.output = nil
			end(&o.attempt, e.At, b.Error)
			// A failed attempt that will be retried does not end the run.
			if e.Kind == envelope.KindCompleted || b.Terminal {
				for id, s := range o.tools {
					s.End = e.At
					delete(o.tools, id)
				}
				end(&o.run, e.At, b.Error)
			}
		}
	})
	j.mu.RUnlock()

	out := make([]Span, len(spans))
	for i, s := range spans {
		out[i] = *s
	}
	slices.SortStableFunc(out, func(a, b Span) int { return a.Start.Compare(b.Start) })
	return out
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
