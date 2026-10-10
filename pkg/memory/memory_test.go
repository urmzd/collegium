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
	a, err := s.Add("acme", "lead", "  - Releases go out\n on Tuesdays.  ", Person, Approved, "ada", now)
	if err != nil || a.Text != "Releases go out on Tuesdays." {
		t.Fatalf("got %+v, %v", a, err)
	}
	b, _ := s.Add("acme", "lead", "releases go out on tuesdays.", Consolidated, Approved, "run-1", now)
	if b.ID != a.ID || len(s.List("acme", "lead")) != 1 {
		t.Fatalf("the same note was stored twice: %+v", s.List("acme", "lead"))
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", MaxChars+1)} {
		if _, err := s.Add("acme", "lead", bad, Person, Approved, "ada", now); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: got %v", bad[:min(len(bad), 10)], err)
		}
	}
}

func TestMemoryBelongsToOneAgentInOneTenant(t *testing.T) {
	s := NewStore()
	n, _ := s.Add("acme", "lead", "The launch is in Q4.", Person, Approved, "ada", now)
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
		if _, err := s.Add("acme", "lead", fmt.Sprintf("person %d", i), Person, Approved, "ada", now); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = s.Add("acme", "lead", "auto 1", Consolidated, Approved, "run-1", now)
	if _, err := s.Add("acme", "lead", "auto 2", Consolidated, Approved, "run-2", now); err != nil {
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
		_, _ = full.Add("acme", "lead", fmt.Sprintf("person %d", i), Person, Approved, "ada", now)
	}
	if _, err := full.Add("acme", "lead", "auto", Consolidated, Approved, "run-1", now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an automatic note displaced a person's: %v", err)
	}
}

// What the memory agent writes is a proposal until a person approves it.
func TestProposalsWaitForApproval(t *testing.T) {
	s := NewStore()
	a, _ := s.Add("acme", "lead", "The launch is in Q4.", Consolidated, Pending, "run-1", now)
	b, _ := s.Add("acme", "lead", "The team ships on Tuesdays.", Consolidated, Pending, "run-1", now)
	if s.Prompt("acme", "lead") != "" {
		t.Fatal("a proposal was sent to the agent before anyone approved it")
	}
	if got := s.Pending("acme"); len(got) != 2 || got[0].Agent != "lead" {
		t.Fatalf("pending = %+v", got)
	}
	if len(s.Pending("globex")) != 0 {
		t.Fatal("another tenant sees the proposals")
	}

	// Approving for the wrong agent or tenant approves nothing.
	for _, who := range [][2]string{{"acme", "helper"}, {"globex", "lead"}} {
		if n, _ := s.Approve(who[0], who[1], []string{a.ID}); n != 0 {
			t.Fatalf("%v approved a proposal that is not theirs", who)
		}
	}
	n, err := s.Approve("acme", "lead", []string{a.ID, "ghost"})
	if err != nil || n != 1 {
		t.Fatalf("approved %d, %v", n, err)
	}
	if got := s.Prompt("acme", "lead"); !strings.Contains(got, "Q4") || strings.Contains(got, "Tuesdays") {
		t.Fatalf("prompt = %q", got)
	}
	// Approving twice does nothing, and discarding is removal.
	if n, _ := s.Approve("acme", "lead", []string{a.ID}); n != 0 {
		t.Fatal("a memory was approved twice")
	}
	if err := s.Remove("acme", "lead", b.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.Pending("acme")) != 0 {
		t.Fatal("a discarded proposal is still pending")
	}
}

func TestPendingIsBounded(t *testing.T) {
	s := NewStore()
	for i := range MaxPending + 5 {
		if _, err := s.Add("acme", "lead", fmt.Sprintf("proposal %d", i), Consolidated, Pending, "run", now); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Pending("acme")
	if len(got) != MaxPending || got[0].Note.Text != "proposal 5" {
		t.Fatalf("%d pending, oldest %q", len(got), got[0].Note.Text)
	}
}

// saige's memory policy refuses personal data, for people and agents alike.
func TestPersonalDataIsRefused(t *testing.T) {
	s := NewStore()
	for _, text := range []string{
		"Ada can be reached at ada@example.com.",
		"Her card is 4111 1111 1111 1111.",
	} {
		if _, err := s.Add("acme", "lead", text, Consolidated, Pending, "run", now); !errors.Is(err, ErrSensitive) {
			t.Errorf("%q: got %v, want ErrSensitive", text, err)
		}
	}
	if len(s.List("acme", "lead")) != 0 {
		t.Fatal("a refused note was stored")
	}
}
