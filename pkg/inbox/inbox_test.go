package inbox

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

const acme = "acme"

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func raise(s *Store, key string, at time.Time, parts ...Part) Message {
	m, _ := s.Raise(acme, key, Message{Kind: KindAlert, Subject: key, From: From{Kind: "rule", Name: "any"}, Severity: Warning, Parts: parts}, at)
	return m
}

// Every kind of part is resolved the same way: once, by whoever got there
// first, and the message is resolved when nothing in it is still open.
func TestResolvingAPart(t *testing.T) {
	parts := map[string]Part{
		"alert":    {Type: PartAlert, Alert: &AlertPart{ID: "al-1", Title: "lead failed"}},
		"failure":  {Type: PartFailure, Failure: &FailurePart{Run: "r1", Agent: "lead", Error: "timeout", Retryable: true}},
		"memory":   {Type: PartMemory, Memory: &MemoryPart{Owner: "lead", Notes: []NoteRef{{ID: "n1", Text: "Ships on Tuesdays."}}}},
		"budget":   {Type: PartBudget, Budget: &BudgetPart{ScopeKind: "project", ScopeName: "Launch", Limit: "runs", Current: Limits{MaxRuns: 10}}},
		"approval": {Type: PartApproval},
		"choice":   {Type: PartChoice},
		"reply":    {Type: PartReply},
	}
	for name, part := range parts {
		t.Run(name, func(t *testing.T) {
			s := NewStore()
			m := raise(s, name, t0, Part{Type: PartText, State: PartInfo, Text: "For context."}, part)
			if m.State != Open || len(m.Parts) != 2 || m.Parts[0].State != PartInfo || m.Parts[1].State != PartOpen || m.Parts[1].ID != "p2" {
				t.Fatalf("raised as %+v", m)
			}

			first, changed, err := s.Resolve(acme, m.ID, "p2", Outcome{Action: "acknowledge", By: "ada", At: t0.Add(time.Minute), Note: "seen"})
			if err != nil || !changed || first.State != Resolved || first.Parts[1].Outcome == nil || first.Parts[1].Outcome.By != "ada" {
				t.Fatalf("resolve = %+v, %v, %v", first, changed, err)
			}
			// A part meant only to be read never held the message open.
			if first.Parts[0].Outcome != nil {
				t.Fatal("a text part was given an outcome")
			}

			again, changed, err := s.Resolve(acme, m.ID, "p2", Outcome{Action: "retry", By: "grace", At: t0.Add(time.Hour)})
			if err != nil || changed {
				t.Fatalf("resolving twice changed something: %v, %v", changed, err)
			}
			if o := again.Parts[1].Outcome; o.By != "ada" || o.Action != "acknowledge" || !o.At.Equal(t0.Add(time.Minute)) {
				t.Fatalf("the second resolve replaced the first outcome: %+v", o)
			}
			if n := len(s.History(acme, m.ID)); n != 2 {
				t.Fatalf("history has %d changes, want raised and resolved", n)
			}
		})
	}
}

func TestAMessageStaysOpenWhileAPartIs(t *testing.T) {
	s := NewStore()
	m := raise(s, "k", t0, Part{Type: PartAlert}, Part{Type: PartFailure})
	got, _, _ := s.Resolve(acme, m.ID, "p1", Outcome{Action: "acknowledge", By: "ada", At: t0})
	if got.State != Open {
		t.Fatalf("state = %s with a part still open", got.State)
	}
	got, _, _ = s.Resolve(acme, m.ID, "p2", Outcome{Action: "retry", By: "ada", At: t0})
	if got.State != Resolved {
		t.Fatalf("state = %s with every part resolved", got.State)
	}
	if _, _, err := s.Resolve(acme, "m-404", "p1", Outcome{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing message = %v", err)
	}
}

func TestOneMessagePerOpenKey(t *testing.T) {
	s := NewStore()
	first := raise(s, "alert:al-1", t0, Part{Type: PartAlert})
	if again, raised := s.Raise(acme, "alert:al-1", Message{Kind: KindAlert}, t0.Add(time.Second)); raised || again.ID != first.ID {
		t.Fatalf("a second message was raised for an open key: %v %s", raised, again.ID)
	}
	if _, _, err := s.Dismiss(acme, first.ID, "ada", t0); err != nil {
		t.Fatal(err)
	}
	if next, raised := s.Raise(acme, "alert:al-1", Message{Kind: KindAlert, Parts: []Part{{Type: PartAlert}}}, t0.Add(time.Minute)); !raised || next.ID == first.ID {
		t.Fatal("a closed key could not be raised again")
	}
	// Another tenant's inbox is another inbox.
	if _, err := s.Get("globex", first.ID, "ada"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant read the message: %v", err)
	}
}

func TestReadIsPerPerson(t *testing.T) {
	s := NewStore()
	m := raise(s, "k", t0, Part{Type: PartAlert})
	if _, err := s.Read(acme, m.ID, "ada", true, t0); err != nil {
		t.Fatal(err)
	}
	ada, _ := s.Get(acme, m.ID, "ada")
	grace, _ := s.Get(acme, m.ID, "grace")
	if ada.Unread || !grace.Unread {
		t.Fatalf("unread: ada=%v grace=%v", ada.Unread, grace.Unread)
	}
	if open, unread := s.Count(acme, Query{Reader: "ada", Now: t0}); open != 1 || unread != 0 {
		t.Fatalf("ada counts %d open, %d unread", open, unread)
	}
	if page, _, _ := s.Page(acme, Query{Reader: "grace", Unread: true, Now: t0}); len(page) != 1 {
		t.Fatalf("grace's unread page has %d", len(page))
	}
	if _, err := s.Read(acme, m.ID, "ada", false, t0); err != nil {
		t.Fatal(err)
	}
	if ada, _ = s.Get(acme, m.ID, "ada"); !ada.Unread {
		t.Fatal("marking unread did nothing")
	}
}

func TestAMessageForOnePersonIsOnlyTheirs(t *testing.T) {
	s := NewStore()
	m, _ := s.Raise(acme, "k", Message{Kind: KindApproval, To: "ada", Parts: []Part{{Type: PartApproval}}}, t0)
	if _, err := s.Get(acme, m.ID, "grace"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("grace read ada's message: %v", err)
	}
	if page, _, _ := s.Page(acme, Query{Reader: "grace", Now: t0}); len(page) != 0 {
		t.Fatal("ada's message is on grace's page")
	}
	if open, _ := s.Count(acme, Query{Reader: "ada", Now: t0}); open != 1 {
		t.Fatal("ada's message is not in ada's count")
	}
}

func TestSnoozeEndsByItself(t *testing.T) {
	s := NewStore()
	m := raise(s, "k", t0, Part{Type: PartAlert})
	if _, _, err := s.Snooze(acme, m.ID, "ada", t0, t0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("snoozing until now = %v", err)
	}
	if _, _, err := s.Snooze(acme, m.ID, "ada", t0.Add(time.Hour), t0); err != nil {
		t.Fatal(err)
	}
	during, after := t0.Add(30*time.Minute), t0.Add(2*time.Hour)
	if page, _, _ := s.Page(acme, Query{Reader: "ada", Now: during}); len(page) != 0 {
		t.Fatal("a snoozed message is in the open page")
	}
	if page, _, _ := s.Page(acme, Query{Reader: "ada", State: Snoozed, Now: during}); len(page) != 1 {
		t.Fatal("a snoozed message is not in the snoozed page")
	}
	page, _, _ := s.Page(acme, Query{Reader: "ada", Now: after})
	if len(page) != 1 || page[0].State != Open {
		t.Fatalf("after the snooze ran out the open page is %+v", page)
	}
	// An alert acknowledged while its message is snoozed still resolves it.
	got, _, _ := s.Resolve(acme, m.ID, "p1", Outcome{Action: "acknowledge", By: "ada", At: during})
	if got.State != Resolved {
		t.Fatalf("state = %s", got.State)
	}
}

func TestPagingNeitherRepeatsNorSkips(t *testing.T) {
	s := NewStore()
	const n = 23
	for i := range n {
		// Several raised at the same instant, to exercise the tie-break.
		raise(s, fmt.Sprint("k", i), t0.Add(time.Duration(i/4)*time.Second), Part{Type: PartAlert})
	}
	seen := map[string]bool{}
	var order []string
	before := ""
	for range n {
		page, next, err := s.Page(acme, Query{Reader: "ada", Limit: 5, Before: before, Now: t0})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range page {
			if seen[m.ID] {
				t.Fatalf("%s came back on a later page", m.ID)
			}
			seen[m.ID] = true
			order = append(order, m.ID)
		}
		if next == "" {
			break
		}
		before = next
		// A message raised while someone is paging must not shift the pages.
		raise(s, "late"+next, t0.Add(time.Hour), Part{Type: PartAlert})
	}
	if len(order) != n || order[0] != "m-23" || order[n-1] != "m-1" {
		t.Fatalf("paged %d messages from %s to %s, want %d from m-23 to m-1", len(order), order[0], order[len(order)-1], n)
	}
	if _, _, err := s.Page(acme, Query{Before: "not-a-cursor!"}); !errors.Is(err, ErrCursor) {
		t.Fatalf("a made-up cursor = %v", err)
	}
	if page, _, _ := s.Page(acme, Query{Reader: "ada", Kind: KindMemory, Now: t0}); len(page) != 0 {
		t.Fatal("the kind filter let another kind through")
	}
	if page, _, _ := s.Page(acme, Query{Reader: "ada", Now: t0, Visible: func(Kind) bool { return false }}); len(page) != 0 {
		t.Fatal("a reader saw a kind they may not see")
	}
}

// The record of changes is enough to rebuild the inbox exactly.
func TestRebuildMatchesLiveState(t *testing.T) {
	s := NewStore()
	a := raise(s, "a", t0, Part{Type: PartAlert}, Part{Type: PartFailure})
	b := raise(s, "b", t0.Add(time.Second), Part{Type: PartMemory, Memory: &MemoryPart{Owner: "lead", Notes: []NoteRef{{ID: "n1", Text: "x"}}}})
	c := raise(s, "c", t0.Add(2*time.Second), Part{Type: PartBudget, Budget: &BudgetPart{Limit: "runs"}})
	d := raise(s, "d", t0.Add(3*time.Second), Part{Type: PartAlert})
	_, _ = s.Read(acme, a.ID, "ada", true, t0)
	_, _ = s.Read(acme, a.ID, "grace", true, t0)
	_, _ = s.Read(acme, a.ID, "grace", false, t0)
	_, _, _ = s.Resolve(acme, a.ID, "p1", Outcome{Action: "acknowledge", By: "ada", At: t0.Add(time.Minute)})
	_, _, _ = s.Resolve(acme, a.ID, "p1", Outcome{Action: "acknowledge", By: "grace", At: t0.Add(time.Hour)})
	_, _, _ = s.Resolve(acme, b.ID, "p1", Outcome{Action: "keep", By: "ada", At: t0.Add(time.Minute), Note: "Kept 1 of 1"})
	_, _, _ = s.Snooze(acme, c.ID, "ada", t0.Add(time.Hour), t0)
	_, _, _ = s.Dismiss(acme, d.ID, "ada", t0)
	_, _, _ = s.Dismiss(acme, d.ID, "grace", t0.Add(time.Hour))
	raise(s, "d", t0.Add(4*time.Second), Part{Type: PartAlert})
	s.Raise("globex", "a", Message{Kind: KindAlert, Parts: []Part{{Type: PartAlert}}}, t0)

	rebuilt := Rebuild(append(s.Changes(acme), s.Changes("globex")...))
	for _, tenantName := range []string{acme, "globex"} {
		liveMsgs, liveRead := s.Snapshot(tenantName)
		gotMsgs, gotRead := rebuilt.Snapshot(tenantName)
		if !reflect.DeepEqual(liveMsgs, gotMsgs) {
			t.Fatalf("%s: rebuilt messages differ\nlive %+v\nrebuilt %+v", tenantName, liveMsgs, gotMsgs)
		}
		if !reflect.DeepEqual(liveRead, gotRead) {
			t.Fatalf("%s: rebuilt read state differs: %v vs %v", tenantName, liveRead, gotRead)
		}
	}
	if msgs, _ := s.Snapshot(acme); len(msgs) != 5 {
		t.Fatalf("%d messages, want 5", len(msgs))
	}
}

func TestWatchersHearChanges(t *testing.T) {
	s := NewStore()
	ch, cancel := s.Watch(acme)
	other, cancelOther := s.Watch("globex")
	defer cancelOther()
	m := raise(s, "k", t0, Part{Type: PartAlert})
	if n := <-ch; n.ID != m.ID || n.State != Open {
		t.Fatalf("notice = %+v", n)
	}
	// Reading is one person's business and tells nobody.
	_, _ = s.Read(acme, m.ID, "ada", true, t0)
	_, _, _ = s.Resolve(acme, m.ID, "p1", Outcome{Action: "acknowledge", By: "ada", At: t0})
	if n := <-ch; n.State != Resolved {
		t.Fatalf("notice after resolving = %+v", n)
	}
	select {
	case n := <-other:
		t.Fatalf("another tenant heard %+v", n)
	default:
	}
	cancel()
	raise(s, "k2", t0, Part{Type: PartAlert})
	select {
	case n := <-ch:
		t.Fatalf("a cancelled watcher heard %+v", n)
	default:
	}
}
