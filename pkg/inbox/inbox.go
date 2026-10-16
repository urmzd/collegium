// Package inbox is the one place things that need a person land, and the place
// they are resolved from.
//
// A message is like an email with typed parts. A part is something to read or
// something to decide: acknowledge an alert, retry a failed run, choose which
// proposed memories to keep, raise a limit. Alert rules decide what to watch
// for. The inbox is where what they found gets dealt with.
//
// The store is a projection. Everything that happens to a message is a Change,
// Apply is the only thing that alters state, and Rebuild replays changes into
// the same state. So what the inbox says can always be accounted for.
//
// The inbox does not decide who may do what, and it does not carry out a
// decision. The platform checks the caller's rights and acts on the source
// (the alert, the run, the notes), then records the outcome here.
package inbox

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Errors a caller can match.
var (
	ErrNotFound = errors.New("inbox: not found")
	ErrCursor   = errors.New("inbox: invalid cursor")
	ErrInvalid  = errors.New("inbox: invalid")
)

// Kind says what a message is about.
type Kind string

const (
	KindAlert         Kind = "alert"
	KindFailure       Kind = "failure"
	KindMemory        Kind = "memory"
	KindBudget        Kind = "budget"
	KindApproval      Kind = "approval"
	KindClarification Kind = "clarification"
	// KindResult is the answer of a run nobody was waiting on.
	KindResult Kind = "result"
)

// State is where a message stands.
type State string

const (
	Open      State = "open"
	Resolved  State = "resolved"
	Dismissed State = "dismissed"
	Snoozed   State = "snoozed"
)

// Severity orders messages by how much they matter.
type Severity string

const (
	Info     Severity = "info"
	Warning  Severity = "warning"
	Critical Severity = "critical"
)

// PartType says what a part holds, and so what can be done with it.
type PartType string

const (
	PartText    PartType = "text"
	PartAlert   PartType = "alert"
	PartFailure PartType = "failure"
	PartMemory  PartType = "memory"
	PartBudget  PartType = "budget"
	// PartApproval, PartChoice and PartReply are a question a run is parked
	// on: approve or deny a tool call, choose one of some options, or reply
	// in words. Each carries the question in Question.
	PartApproval PartType = "approval"
	PartChoice   PartType = "choice"
	PartReply    PartType = "reply"
)

// PartState says whether a part still needs someone.
type PartState string

const (
	// PartOpen needs a decision.
	PartOpen PartState = "open"
	// PartResolved has one.
	PartResolved PartState = "resolved"
	// PartInfo is only there to be read.
	PartInfo PartState = "info"
)

// From is who a message is from.
type From struct {
	// Kind is "rule", "agent", "system" or "person".
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Source links a message to what it is about.
type Source struct {
	Alert   string `json:"alert,omitempty"`
	Rule    string `json:"rule,omitempty"`
	Run     string `json:"run,omitempty"`
	Thread  string `json:"thread,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Project string `json:"project,omitempty"`
}

// AlertPart is an alert to acknowledge.
type AlertPart struct {
	ID       string `json:"id"`
	Rule     string `json:"rule"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
}

// FailurePart is a run that failed.
type FailurePart struct {
	Run       string `json:"run"`
	Agent     string `json:"agent"`
	Error     string `json:"error"`
	Retryable bool   `json:"retryable"`
}

// NoteRef is one proposed memory.
type NoteRef struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// MemoryPart is a set of proposed memories to choose from.
type MemoryPart struct {
	// Owner is who would remember them, as the memory store names it.
	Owner   string    `json:"owner"`
	Agent   string    `json:"agent,omitempty"`
	Project string    `json:"project,omitempty"`
	Notes   []NoteRef `json:"notes"`
}

// Limits are the numbers a billing policy sets.
type Limits struct {
	MaxRuns       int   `json:"max_runs,omitempty"`
	MaxConcurrent int   `json:"max_concurrent,omitempty"`
	SpendLimit    int64 `json:"spend_limit,omitempty"`
	MaxTokens     int64 `json:"max_tokens,omitempty"`
}

// BudgetPart is a limit that was reached.
type BudgetPart struct {
	ScopeKind string `json:"scope_kind"`
	ScopeName string `json:"scope_name,omitempty"`
	// Limit is which one: "runs", "concurrent", "spend", "tokens" or "lease".
	Limit   string `json:"limit"`
	Period  string `json:"period"`
	Version int    `json:"version"`
	// Used and Max are where the scope stood against that limit when it was
	// reached: runs, runs at once, micro-USD, or tokens.
	Used int64 `json:"used"`
	// Refused is true when a run was turned away or stopped at the limit,
	// false for a warning.
	Refused bool   `json:"refused"`
	Max     int64  `json:"max"`
	Current Limits `json:"current"`
}

// QuestionTool is the call an approval is about.
type QuestionTool struct {
	Name string `json:"name"`
	// Arguments are what the model asked to call it with, as a JSON object.
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// Schema is the tool's parameter schema. Edited arguments must satisfy it.
	Schema json.RawMessage `json:"schema,omitempty"`
}

// QuestionPart is a question a run is parked on.
type QuestionPart struct {
	// Run is the run that asked, and ID the question within it.
	Run string `json:"run"`
	ID  string `json:"id"`
	// Kind is approval, choice or reply.
	Kind   string `json:"kind"`
	Agent  string `json:"agent,omitempty"`
	Prompt string `json:"prompt"`
	// Options are what a choice offers.
	Options []string      `json:"options,omitempty"`
	Tool    *QuestionTool `json:"tool,omitempty"`
	// AskedOf is who the agent asked, as a hint for routing.
	AskedOf string `json:"asked_of,omitempty"`
	// ExpiresAt is when the question takes its default if nobody answers.
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	// Default is what the run carries on with if it expires: "deny" for an
	// approval, the stated default for a choice or a reply, or empty when
	// the agent is told nobody answered.
	Default string `json:"default,omitempty"`
	// AskedAt is when the run asked.
	AskedAt time.Time `json:"asked_at,omitzero"`
	// Via is the run the question was raised through, when a run waiting on
	// another shows its question: the caller of the run that asked.
	Via string `json:"via,omitempty"`
}

// Outcome is what was decided about a part, and by whom.
type Outcome struct {
	Action string    `json:"action"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
	Note   string    `json:"note,omitempty"`
}

// Part is one piece of a message.
type Part struct {
	ID    string    `json:"id"`
	Type  PartType  `json:"type"`
	State PartState `json:"state"`
	// Actions is what the reader may do with the part. The store leaves it
	// empty: it depends on who is reading.
	Actions []string `json:"actions"`
	Outcome *Outcome `json:"outcome,omitempty"`

	Text    string       `json:"text,omitempty"`
	Alert   *AlertPart   `json:"alert,omitempty"`
	Failure *FailurePart `json:"failure,omitempty"`
	Memory  *MemoryPart  `json:"memory,omitempty"`
	Budget  *BudgetPart  `json:"budget,omitempty"`
	// Question is set on approval, choice and reply parts.
	Question *QuestionPart `json:"question,omitempty"`
}

// Message is one thing in the inbox.
type Message struct {
	ID string `json:"id"`
	// Key names what the message is about, so the same thing is not raised
	// twice. It is not shown.
	Key    string `json:"-"`
	Tenant string `json:"-"`
	// To is the one person it is for. Empty means everyone who may see its kind.
	To string `json:"to,omitempty"`
	// Audience, when set, is who the message is for: principals by subject,
	// and roles as "role:<name>". A reader sees it only if they are named or
	// hold a role named. Empty means everyone who may see its kind.
	Audience []string `json:"-"`
	// Excluded are principals the message is never shown to, whatever else
	// admits them: the person whose request raised a call they may not
	// approve.
	Excluded     []string  `json:"-"`
	Kind         Kind      `json:"kind"`
	Subject      string    `json:"subject"`
	From         From      `json:"from"`
	At           time.Time `json:"at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Severity     Severity  `json:"severity"`
	State        State     `json:"state"`
	SnoozedUntil time.Time `json:"snoozed_until,omitzero"`
	// Unread is filled in for whoever is reading.
	Unread bool   `json:"unread"`
	Source Source `json:"source"`
	Parts  []Part `json:"parts"`
}

func (m Message) clone() Message {
	m.Parts = slices.Clone(m.Parts)
	for i := range m.Parts {
		p := &m.Parts[i]
		if p.Outcome != nil {
			o := *p.Outcome
			p.Outcome = &o
		}
		if p.Question != nil {
			q := *p.Question
			q.Options = slices.Clone(q.Options)
			if q.Tool != nil {
				t := *q.Tool
				q.Tool = &t
			}
			p.Question = &q
		}
		if p.Memory != nil {
			mem := *p.Memory
			mem.Notes = slices.Clone(mem.Notes)
			p.Memory = &mem
		}
		p.Actions = slices.Clone(p.Actions)
	}
	m.Audience = slices.Clone(m.Audience)
	m.Excluded = slices.Clone(m.Excluded)
	return m
}

// Reader is who is reading: their subject, and the roles they hold, which
// decide whether a message addressed to a role is theirs.
type Reader struct {
	Subject string
	Roles   []string
}

// For reports whether a message is addressed to a reader. Who may see its
// kind at all is the caller's to decide.
func (m Message) For(r Reader) bool {
	if m.To != "" && m.To != r.Subject {
		return false
	}
	if slices.Contains(m.Excluded, r.Subject) {
		return false
	}
	if len(m.Audience) == 0 {
		return true
	}
	if r.Subject != "" && slices.Contains(m.Audience, r.Subject) {
		return true
	}
	for _, role := range r.Roles {
		if role != "" && slices.Contains(m.Audience, "role:"+role) {
			return true
		}
	}
	return false
}

// at reports the message's state at a moment: a snooze that has run out is open.
func (m Message) at(now time.Time) State {
	if m.State == Snoozed && !now.Before(m.SnoozedUntil) {
		return Open
	}
	return m.State
}

// What names a change.
type What string

const (
	Raised      What = "raised"
	MarkRead    What = "read"
	MarkUnread  What = "unread"
	DidResolve  What = "resolved"
	DidDismiss  What = "dismissed"
	DidSnooze   What = "snoozed"
	DidWithdraw What = "withdrawn"
)

// Change is one thing that happened to a message. The list of them is the
// record of the inbox, and enough to rebuild it.
type Change struct {
	Tenant  string    `json:"-"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
	By      string    `json:"by,omitempty"`
	What    What      `json:"what"`
	Part    string    `json:"part,omitempty"`
	Action  string    `json:"action,omitempty"`
	Note    string    `json:"note,omitempty"`
	Until   time.Time `json:"until,omitzero"`
	// Raised is the message as it was raised, on a Raised change.
	Raised *Message `json:"-"`
}

// Notice says a message changed. It carries no content: a reader fetches the
// message, and is shown only what they may see.
type Notice struct {
	ID        string    `json:"id"`
	Kind      Kind      `json:"kind"`
	State     State     `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
}

type tenant struct {
	messages []*Message // oldest first
	byKey    map[string]*Message
	byID     map[string]*Message
	read     map[string]map[string]bool // message id -> subject
	log      []Change
	next     int
	watchers map[int]chan Notice
	nextW    int
}

// Store holds every tenant's inbox.
type Store struct {
	mu      sync.Mutex
	tenants map[string]*tenant
}

// NewStore returns an empty inbox.
func NewStore() *Store { return &Store{tenants: map[string]*tenant{}} }

func (s *Store) of(name string) *tenant {
	t, ok := s.tenants[name]
	if !ok {
		t = &tenant{byKey: map[string]*Message{}, byID: map[string]*Message{}, read: map[string]map[string]bool{}, watchers: map[int]chan Notice{}}
		s.tenants[name] = t
	}
	return t
}

// apply is the only thing that alters state. It must depend on nothing but
// the state and the change, so that replaying changes rebuilds the state.
func (t *tenant) apply(c Change) (*Message, bool) {
	if c.What == Raised {
		if prior := t.byKey[c.Raised.Key]; c.Raised == nil || prior != nil && prior.State != Resolved && prior.State != Dismissed {
			return nil, false
		}
		m := c.Raised.clone()
		t.next++
		m.ID = "m-" + strconv.Itoa(t.next)
		m.Tenant, m.At, m.UpdatedAt, m.State, m.Unread = c.Tenant, c.At, c.At, Open, false
		for i := range m.Parts {
			m.Parts[i].ID = "p" + strconv.Itoa(i+1)
			m.Parts[i].Actions, m.Parts[i].Outcome = nil, nil
			if m.Parts[i].State == "" {
				m.Parts[i].State = PartOpen
			}
		}
		t.messages = append(t.messages, &m)
		t.byKey[m.Key], t.byID[m.ID] = &m, &m
		c.Message = m.ID
		t.log = append(t.log, c)
		return &m, true
	}

	m := t.byID[c.Message]
	if m == nil {
		return nil, false
	}
	switch c.What {
	case MarkRead, MarkUnread:
		was := t.read[m.ID][c.By]
		if was == (c.What == MarkRead) {
			return m, false
		}
		if t.read[m.ID] == nil {
			t.read[m.ID] = map[string]bool{}
		}
		t.read[m.ID][c.By] = c.What == MarkRead
		t.log = append(t.log, c)
		// Reading changes nothing anyone else sees.
		return m, false
	case DidResolve:
		i := slices.IndexFunc(m.Parts, func(p Part) bool { return p.ID == c.Part })
		if i < 0 || m.Parts[i].State != PartOpen {
			return m, false
		}
		m.Parts[i].State = PartResolved
		m.Parts[i].Outcome = &Outcome{Action: c.Action, By: c.By, At: c.At, Note: c.Note}
		if !slices.ContainsFunc(m.Parts, func(p Part) bool { return p.State == PartOpen }) {
			m.State, m.SnoozedUntil = Resolved, time.Time{}
		}
	case DidDismiss, DidWithdraw:
		if m.State == Resolved || m.State == Dismissed {
			return m, false
		}
		m.State, m.SnoozedUntil = Dismissed, time.Time{}
	case DidSnooze:
		if m.State == Resolved || m.State == Dismissed {
			return m, false
		}
		m.State, m.SnoozedUntil = Snoozed, c.Until
	default:
		return m, false
	}
	m.UpdatedAt = c.At
	t.log = append(t.log, c)
	return m, true
}

func (t *tenant) tell(m *Message) {
	n := Notice{ID: m.ID, Kind: m.Kind, State: m.State, UpdatedAt: m.UpdatedAt}
	for _, ch := range t.watchers {
		// A watcher that is not keeping up misses a notice, never blocks the
		// inbox. It refetches on the next one.
		select {
		case ch <- n:
		default:
		}
	}
}

func (s *Store) do(c Change) (Message, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.of(c.Tenant)
	if c.What != Raised && t.byID[c.Message] == nil {
		return Message{}, false, fmt.Errorf("%w: message %q", ErrNotFound, c.Message)
	}
	m, changed := t.apply(c)
	if m == nil {
		return Message{}, false, nil
	}
	if changed {
		t.tell(m)
	}
	out := m.clone()
	out.Unread = !t.read[m.ID][c.By]
	return out, changed, nil
}

// Raise puts a message in the inbox. key names what it is about: while a
// message with that key is open, a second is not raised and the first is
// returned.
func (s *Store) Raise(tenantName, key string, m Message, at time.Time) (Message, bool) {
	if existing, ok := s.ByKey(tenantName, key); ok && existing.State != Resolved && existing.State != Dismissed {
		return existing, false
	}
	m.Key = key
	out, raised, _ := s.do(Change{Tenant: tenantName, What: Raised, At: at, By: m.From.Name, Raised: &m})
	return out, raised
}

// ByKey returns the latest message raised under a key.
func (s *Store) ByKey(tenantName, key string) (Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.of(tenantName).byKey[key]
	if m == nil {
		return Message{}, false
	}
	return m.clone(), true
}

// Get returns one message as a reader known only by their subject sees it.
func (s *Store) Get(tenantName, id, reader string) (Message, error) {
	return s.GetAs(tenantName, id, Reader{Subject: reader})
}

// GetAs returns one message as a reader sees it. A message not addressed to
// them does not exist for them.
func (s *Store) GetAs(tenantName, id string, r Reader) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.of(tenantName)
	m := t.byID[id]
	if m == nil || !m.For(r) {
		return Message{}, fmt.Errorf("%w: message %q", ErrNotFound, id)
	}
	out := m.clone()
	out.Unread = !t.read[id][r.Subject]
	return out, nil
}

// Read marks a message read or unread for one person.
func (s *Store) Read(tenantName, id, by string, read bool, at time.Time) (Message, error) {
	what := MarkUnread
	if read {
		what = MarkRead
	}
	m, _, err := s.do(Change{Tenant: tenantName, Message: id, What: what, By: by, At: at})
	return m, err
}

// Resolve records what was decided about a part. Resolving a part that has an
// outcome changes nothing and returns the message with the first outcome.
func (s *Store) Resolve(tenantName, id, part string, o Outcome) (Message, bool, error) {
	return s.do(Change{Tenant: tenantName, Message: id, What: DidResolve, Part: part, Action: o.Action, By: o.By, At: o.At, Note: o.Note})
}

// Dismiss sets a message aside without a decision.
func (s *Store) Dismiss(tenantName, id, by string, at time.Time) (Message, bool, error) {
	return s.do(Change{Tenant: tenantName, Message: id, What: DidDismiss, By: by, At: at})
}

// Withdraw closes a message because what it was about is gone.
func (s *Store) Withdraw(tenantName, id, note string, at time.Time) (Message, bool, error) {
	return s.do(Change{Tenant: tenantName, Message: id, What: DidWithdraw, By: "system", Note: note, At: at})
}

// Snooze hides a message until a time, when it is open again.
func (s *Store) Snooze(tenantName, id, by string, until, at time.Time) (Message, bool, error) {
	if !until.After(at) {
		return Message{}, false, fmt.Errorf("%w: snooze until a time that is still to come", ErrInvalid)
	}
	return s.do(Change{Tenant: tenantName, Message: id, What: DidSnooze, By: by, Until: until, At: at})
}

// History returns what happened to one message, oldest first.
func (s *Store) History(tenantName, id string) []Change {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Change
	for _, c := range s.of(tenantName).log {
		if c.Message == id {
			out = append(out, c)
		}
	}
	return out
}

// Changes returns a tenant's whole record, oldest first.
func (s *Store) Changes(tenantName string) []Change {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.of(tenantName).log)
}

// Rebuild returns the inbox a record describes.
func Rebuild(changes []Change) *Store {
	s := NewStore()
	for _, c := range changes {
		s.of(c.Tenant).apply(c)
	}
	return s
}

// Snapshot returns every message in a tenant with who has read it, for
// comparing a rebuilt inbox with a live one.
func (s *Store) Snapshot(tenantName string) ([]Message, map[string][]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.of(tenantName)
	msgs := make([]Message, 0, len(t.messages))
	read := map[string][]string{}
	for _, m := range t.messages {
		msgs = append(msgs, m.clone())
		for who, on := range t.read[m.ID] {
			if on {
				read[m.ID] = append(read[m.ID], who)
			}
		}
		slices.Sort(read[m.ID])
	}
	return msgs, read
}

// Paging bounds for Page.
const (
	DefaultPage = 50
	MaxPage     = 200
)

// Query narrows a page of the inbox.
type Query struct {
	// Reader is whose inbox this is: it decides unread, and which messages
	// addressed to one person are included.
	Reader string
	// Roles are the roles the reader holds, for messages addressed to a role.
	Roles []string
	// State keeps messages in one state. Empty means open. "all" means any.
	State State
	Kind  Kind
	// Alert keeps the messages about one alert.
	Alert string
	// Unread keeps only what the reader has not read.
	Unread bool
	// Visible reports whether the reader may see a kind. Nil means all.
	Visible func(Kind) bool
	Now     time.Time
	Limit   int
	Before  string
}

// All is the State that matches every message.
const All State = "all"

func (t *tenant) match(m *Message, q Query) bool {
	if !m.For(Reader{Subject: q.Reader, Roles: q.Roles}) {
		return false
	}
	if q.Visible != nil && !q.Visible(m.Kind) {
		return false
	}
	if q.Kind != "" && m.Kind != q.Kind {
		return false
	}
	if q.Alert != "" && m.Source.Alert != q.Alert {
		return false
	}
	want := q.State
	if want == "" {
		want = Open
	}
	if want != All && m.at(q.Now) != want {
		return false
	}
	return !q.Unread || !t.read[m.ID][q.Reader]
}

func encodeCursor(m *Message) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(m.At.UnixNano(), 10) + ":" + m.ID))
}

func decodeCursor(c string) (int64, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, "", ErrCursor
	}
	stamp, id, ok := strings.Cut(string(raw), ":")
	if !ok || id == "" {
		return 0, "", ErrCursor
	}
	at, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return 0, "", ErrCursor
	}
	return at, id, nil
}

// Page returns one page of an inbox, newest first, and the cursor for the page
// after it. The cursor is empty at the end. A message's place is when it was
// raised, which never changes, so paging neither repeats nor skips.
func (s *Store) Page(tenantName string, q Query) ([]Message, string, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultPage
	}
	limit = min(limit, MaxPage)
	var beforeAt int64
	var beforeID string
	if q.Before != "" {
		var err error
		if beforeAt, beforeID, err = decodeCursor(q.Before); err != nil {
			return nil, "", err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.of(tenantName)
	out := []Message{}
	var last *Message
	for i := len(t.messages) - 1; i >= 0; i-- {
		m := t.messages[i]
		if q.Before != "" {
			at := m.At.UnixNano()
			// Ids are issued in order, so the later of two messages raised
			// at the same instant has the longer or greater id.
			if at > beforeAt || at == beforeAt && !older(m.ID, beforeID) {
				continue
			}
		}
		if !t.match(m, q) {
			continue
		}
		if len(out) == limit {
			return out, encodeCursor(last), nil
		}
		c := m.clone()
		c.State = m.at(q.Now)
		c.Unread = !t.read[m.ID][q.Reader]
		out = append(out, c)
		last = m
	}
	return out, "", nil
}

// older reports whether id a was issued before id b.
func older(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// Count returns how many messages are open for a reader, and how many of those
// are unread.
func (s *Store) Count(tenantName string, q Query) (open, unread int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.of(tenantName)
	q.State, q.Kind, q.Alert, q.Unread = Open, "", "", false
	for _, m := range t.messages {
		if !t.match(m, q) {
			continue
		}
		open++
		if !t.read[m.ID][q.Reader] {
			unread++
		}
	}
	return open, unread
}

// OpenOf returns the open and snoozed messages of a kind, for keeping the
// inbox in step with what the messages are about.
func (s *Store) OpenOf(tenantName string, kind Kind) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for _, m := range s.of(tenantName).messages {
		if m.Kind == kind && (m.State == Open || m.State == Snoozed) {
			out = append(out, m.clone())
		}
	}
	return out
}

// Watch reports changes to a tenant's inbox until cancel is called.
func (s *Store) Watch(tenantName string) (<-chan Notice, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.of(tenantName)
	ch := make(chan Notice, 32)
	id := t.nextW
	t.nextW++
	t.watchers[id] = ch
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(t.watchers, id)
	}
}
