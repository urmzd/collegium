// Package memory is what an agent carries from one conversation to the next.
//
// Each agent has its own short list of notes. They are sent to the agent with
// every conversation turn, the way a person brings what they know to a meeting.
// A note is written by a person, or proposed by the workspace's memory agent
// after a turn. Either way it is plain text that anyone with access can read,
// correct and remove: nothing an agent remembers is hidden from the people it
// works for.
//
// # A person decides what is kept
//
// What the memory agent writes is a proposal. It is held as pending, it is not
// sent to the agent, and it becomes a memory only when a person approves it.
// That is saige's own default for memory writes: its Policy asks a human before
// every write unless AutoApprove is set, and a workspace can choose the same.
//
// What may be stored at all is also saige's rule. Every note passes through
// saige's memory Policy, which refuses text that carries personal data.
//
// The store is in memory and is lost on restart.
package memory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	saigememory "github.com/urmzd/saige/agent/memory"
)

// Errors.
var (
	ErrInvalid  = errors.New("memory: invalid")
	ErrNotFound = errors.New("memory: not found")
	// ErrSensitive reports a note that carries personal data.
	ErrSensitive = errors.New("memory: the note carries personal data")
)

// Bounds. Memory rides along on every turn, so it is kept small.
const (
	MaxNotes = 50
	MaxChars = 300
	// MaxPending bounds proposals waiting for a person. When it is full the
	// oldest proposal is dropped, so an unattended queue cannot grow.
	MaxPending = 20
)

// Status says whether a note is in use.
type Status string

const (
	// Approved notes are sent to the agent.
	Approved Status = "approved"
	// Pending notes are proposals. They are not sent to the agent.
	Pending Status = "pending"
)

// Source says who wrote a note.
type Source string

const (
	// Person is a note someone typed.
	Person Source = "person"
	// Consolidated is a note the memory agent wrote after a turn.
	Consolidated Source = "consolidated"
)

// Note is one thing an agent remembers.
type Note struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Source Source `json:"source"`
	Status Status `json:"status"`
	// By is the person who wrote it, or the run it was drawn from.
	By string    `json:"by,omitempty"`
	At time.Time `json:"at"`
}

// Store holds every agent's notes.
type Store struct {
	mu   sync.RWMutex
	by   map[string][]Note
	next int
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{by: map[string][]Note{}} }

func key(tenant, agent string) string { return tenant + "\x00" + agent }

// clean makes text fit for a note: one line, trimmed, bounded.
func clean(text string) (string, error) {
	text = strings.Join(strings.Fields(text), " ")
	text = strings.TrimLeft(text, "-*• ")
	switch {
	case text == "":
		return "", fmt.Errorf("%w: a note needs text", ErrInvalid)
	case len(text) > MaxChars:
		return "", fmt.Errorf("%w: a note is at most %d characters", ErrInvalid, MaxChars)
	}
	// saige's memory policy decides what may be stored. With no redactor
	// configured it refuses text its detector finds personal data in.
	if _, err := (saigememory.Policy{}).CheckContent(context.Background(), saigememory.Scope{}, text); err != nil {
		if errors.Is(err, saigememory.ErrSensitive) {
			return "", fmt.Errorf("%w: %v", ErrSensitive, err)
		}
		return "", err
	}
	return text, nil
}

// Add stores a note for an agent, approved or pending. A note the agent already
// holds, in either state, is not stored twice.
//
// When the approved list is full, the oldest consolidated note makes room, and
// a list full of notes people wrote refuses the new one: a person's note is
// never pushed out by an automatic one. When the pending list is full, the
// oldest proposal is dropped.
func (s *Store) Add(tenant, agent, text string, source Source, status Status, by string, at time.Time) (Note, error) {
	text, err := clean(text)
	if err != nil {
		return Note{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(tenant, agent)
	notes := s.by[k]
	for _, n := range notes {
		if strings.EqualFold(n.Text, text) {
			return n, nil
		}
	}
	notes, err = makeRoom(notes, status)
	if err != nil {
		return Note{}, err
	}
	s.next++
	note := Note{ID: "m" + strconv.Itoa(s.next), Text: text, Source: source, Status: status, By: by, At: at}
	s.by[k] = append(notes, note)
	return note, nil
}

func count(notes []Note, status Status) int {
	n := 0
	for _, x := range notes {
		if x.Status == status {
			n++
		}
	}
	return n
}

// makeRoom frees a place for one more note of a status, or refuses.
func makeRoom(notes []Note, status Status) ([]Note, error) {
	if status == Pending {
		if count(notes, Pending) >= MaxPending {
			i := slices.IndexFunc(notes, func(n Note) bool { return n.Status == Pending })
			notes = slices.Delete(notes, i, i+1)
		}
		return notes, nil
	}
	if count(notes, Approved) < MaxNotes {
		return notes, nil
	}
	i := slices.IndexFunc(notes, func(n Note) bool { return n.Status == Approved && n.Source == Consolidated })
	if i < 0 {
		return nil, fmt.Errorf("%w: an agent holds at most %d notes", ErrInvalid, MaxNotes)
	}
	return slices.Delete(notes, i, i+1), nil
}

// Approve turns the named proposals into memories. Ids that are not pending
// proposals of this agent are ignored. It returns how many were approved.
func (s *Store) Approve(tenant, agent string, ids []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(tenant, agent)
	notes := s.by[k]
	approved := 0
	for _, id := range ids {
		i := slices.IndexFunc(notes, func(n Note) bool { return n.ID == id && n.Status == Pending })
		if i < 0 {
			continue
		}
		note := notes[i]
		rest, err := makeRoom(slices.Delete(slices.Clone(notes), i, i+1), Approved)
		if err != nil {
			s.by[k] = notes
			return approved, err
		}
		note.Status = Approved
		notes = append(rest, note)
		approved++
	}
	s.by[k] = notes
	return approved, nil
}

// Proposal is a pending note and the agent it is for.
type Proposal struct {
	Agent string `json:"agent"`
	Note  Note   `json:"note"`
}

// Pending returns every proposal waiting in a tenant, oldest first.
func (s *Store) Pending(tenant string) []Proposal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Proposal
	prefix := tenant + "\x00"
	for k, notes := range s.by {
		agent, ok := strings.CutPrefix(k, prefix)
		if !ok {
			continue
		}
		for _, n := range notes {
			if n.Status == Pending {
				out = append(out, Proposal{Agent: agent, Note: n})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b Proposal) int { return a.Note.At.Compare(b.Note.At) })
	return out
}

// List returns an agent's notes, oldest first.
func (s *Store) List(tenant, agent string) []Note {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.by[key(tenant, agent)])
}

// Drop forgets everything an owner holds, for when the owner is deleted.
func (s *Store) Drop(tenant, owner string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.by, key(tenant, owner))
}

// Remove deletes one note.
func (s *Store) Remove(tenant, agent, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(tenant, agent)
	i := slices.IndexFunc(s.by[k], func(n Note) bool { return n.ID == id })
	if i < 0 {
		return fmt.Errorf("%w: note %q", ErrNotFound, id)
	}
	s.by[k] = slices.Delete(s.by[k], i, i+1)
	return nil
}

// Prompt renders an agent's notes for the start of a turn, or "" for none.
func (s *Store) Prompt(tenant, agent string) string {
	return s.PromptAs(tenant, agent, "What you remember from earlier conversations:")
}

// projectPrefix marks a memory owner that is a project rather than an agent.
// An agent's name cannot contain a colon, so the two never collide.
const projectPrefix = "project:"

// ProjectOwner is the owner under which a project's shared notes are kept.
// Every agent answering in that project is sent them.
func ProjectOwner(project string) string { return projectPrefix + project }

// OwnerProject reports the project an owner names, if it names one.
func OwnerProject(owner string) (string, bool) { return strings.CutPrefix(owner, projectPrefix) }

// PromptAs renders an owner's approved notes under a heading, or "" for none.
func (s *Store) PromptAs(tenant, owner, heading string) string {
	var b strings.Builder
	for _, n := range s.List(tenant, owner) {
		// A proposal nobody has approved is not a memory yet.
		if n.Status != Approved {
			continue
		}
		if b.Len() == 0 {
			b.WriteString(heading)
		}
		b.WriteString("\n- ")
		b.WriteString(n.Text)
	}
	return b.String()
}
