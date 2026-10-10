// Package memory is what an agent carries from one conversation to the next.
//
// Each agent has its own short list of notes. They are sent to the agent with
// every conversation turn, the way a person brings what they know to a meeting.
// A note is written by a person, or by the workspace's memory agent after a
// turn. Either way it is plain text that anyone with access can read, correct
// and remove: nothing an agent remembers is hidden from the people it works for.
//
// The store is in memory and is lost on restart.
package memory

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Errors.
var (
	ErrInvalid  = errors.New("memory: invalid")
	ErrNotFound = errors.New("memory: not found")
)

// Bounds. Memory rides along on every turn, so it is kept small.
const (
	MaxNotes = 50
	MaxChars = 300
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
	return text, nil
}

// Add stores a note for an agent. A note the agent already holds is not stored
// twice. When the list is full, the oldest consolidated note makes room, and a
// list full of notes people wrote refuses the new one: a person's note is never
// pushed out by an automatic one.
func (s *Store) Add(tenant, agent, text string, source Source, by string, at time.Time) (Note, error) {
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
	if len(notes) >= MaxNotes {
		i := slices.IndexFunc(notes, func(n Note) bool { return n.Source == Consolidated })
		if i < 0 {
			return Note{}, fmt.Errorf("%w: an agent holds at most %d notes", ErrInvalid, MaxNotes)
		}
		notes = slices.Delete(notes, i, i+1)
	}
	s.next++
	note := Note{ID: "m" + strconv.Itoa(s.next), Text: text, Source: source, By: by, At: at}
	s.by[k] = append(notes, note)
	return note, nil
}

// List returns an agent's notes, oldest first.
func (s *Store) List(tenant, agent string) []Note {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.by[key(tenant, agent)])
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
	notes := s.List(tenant, agent)
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("What you remember from earlier conversations:")
	for _, n := range notes {
		b.WriteString("\n- ")
		b.WriteString(n.Text)
	}
	return b.String()
}
