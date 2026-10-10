package memory

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func TestAddCleansAndDeduplicates(t *testing.T) {
	s := NewStore()
	a, err := s.Add("acme", "lead", "  - Releases go out\n on Tuesdays.  ", Person, "ada", now)
	if err != nil || a.Text != "Releases go out on Tuesdays." {
		t.Fatalf("got %+v, %v", a, err)
	}
	b, _ := s.Add("acme", "lead", "releases go out on tuesdays.", Consolidated, "run-1", now)
	if b.ID != a.ID || len(s.List("acme", "lead")) != 1 {
		t.Fatalf("the same note was stored twice: %+v", s.List("acme", "lead"))
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", MaxChars+1)} {
		if _, err := s.Add("acme", "lead", bad, Person, "ada", now); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: got %v", bad[:min(len(bad), 10)], err)
		}
	}
}

func TestMemoryBelongsToOneAgentInOneTenant(t *testing.T) {
	s := NewStore()
	n, _ := s.Add("acme", "lead", "The launch is in Q4.", Person, "ada", now)
	for _, who := range [][2]string{{"acme", "helper"}, {"globex", "lead"}} {
		if got := s.List(who[0], who[1]); len(got) != 0 {
			t.Errorf("%v sees %v", who, got)
		}
		if s.Prompt(who[0], who[1]) != "" {
			t.Errorf("%v is sent the note", who)
		}
		if err := s.Remove(who[0], who[1], n.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("%v removed it: %v", who, err)
		}
	}
	if got := s.Prompt("acme", "lead"); !strings.Contains(got, "- The launch is in Q4.") {
		t.Fatalf("prompt = %q", got)
	}
}

// A person's note is never pushed out by an automatic one.
func TestAFullListDropsConsolidatedNotesFirst(t *testing.T) {
	s := NewStore()
	for i := range MaxNotes - 1 {
		if _, err := s.Add("acme", "lead", fmt.Sprintf("person %d", i), Person, "ada", now); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = s.Add("acme", "lead", "auto 1", Consolidated, "run-1", now)
	if _, err := s.Add("acme", "lead", "auto 2", Consolidated, "run-2", now); err != nil {
		t.Fatal(err)
	}
	notes := s.List("acme", "lead")
	if len(notes) != MaxNotes || notes[len(notes)-1].Text != "auto 2" {
		t.Fatalf("got %d notes, last %q", len(notes), notes[len(notes)-1].Text)
	}
	for _, n := range notes {
		if n.Text == "auto 1" {
			t.Fatal("the older automatic note should have made room")
		}
	}

	full := NewStore()
	for i := range MaxNotes {
		_, _ = full.Add("acme", "lead", fmt.Sprintf("person %d", i), Person, "ada", now)
	}
	if _, err := full.Add("acme", "lead", "auto", Consolidated, "run-1", now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an automatic note displaced a person's: %v", err)
	}
}
