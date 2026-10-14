package runs

import (
	"errors"
	"testing"
	"time"

	"github.com/urmzd/mandatum/pkg/agentrt/payload"
	"github.com/urmzd/mandatum/pkg/envelope"
)

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func record(t *testing.T, x *Index, tenant, id, agent, origin string, at time.Duration, meta map[string]string) {
	t.Helper()
	tag := envelope.Tag{ID: id, Tenant: tenant, Agent: agent, Origin: origin, Text: "go", Meta: meta, At: t0.Add(at)}
	if err := x.Record(id, tag, 1); err != nil {
		t.Fatal(err)
	}
}

func end(t *testing.T, x *Index, tenant, id string, at time.Duration, kind envelope.Kind, body payload.Lifecycle) (Run, bool) {
	t.Helper()
	b, err := payload.Encode(body)
	if err != nil {
		t.Fatal(err)
	}
	return x.Observe(envelope.Event{RunID: id, Tenant: tenant, Kind: kind, Payload: b, At: t0.Add(at)})
}

func TestObserveReportsAnEndingOnce(t *testing.T) {
	x := NewIndex()
	record(t, x, "acme", "r1", "lead", "api", 0, nil)
	if _, ended := end(t, x, "acme", "r1", time.Second, envelope.KindFailed, payload.Lifecycle{Error: "timeout"}); ended {
		t.Fatal("a failure that will be retried ended the run")
	}
	r, ended := end(t, x, "acme", "r1", 2*time.Second, envelope.KindFailed, payload.Lifecycle{Error: "timeout", Terminal: true})
	if !ended || r.Status != Failed || r.Error != "timeout" {
		t.Fatalf("got %+v, %v", r, ended)
	}
	if _, again := end(t, x, "acme", "r1", 3*time.Second, envelope.KindCompleted, payload.Lifecycle{}); again {
		t.Fatal("an ended run ended twice")
	}
	if _, ok := end(t, x, "globex", "r1", time.Second, envelope.KindCompleted, payload.Lifecycle{}); ok {
		t.Fatal("another tenant's event moved the run")
	}
}

func TestOnlyTrustedOriginsNameASchedule(t *testing.T) {
	x := NewIndex()
	meta := map[string]string{"cron_schedule": "nightly"}
	record(t, x, "acme", "a", "lead", "cron", 0, meta)
	record(t, x, "acme", "b", "lead", OriginManual, 0, meta)
	record(t, x, "acme", "c", "lead", "webhook", 0, meta)
	got := x.ForSchedule("acme", "nightly", 0)
	if len(got) != 2 {
		t.Fatalf("got %d runs, want the cron and manual ones only", len(got))
	}
	if len(x.ForSchedule("globex", "nightly", 0)) != 0 {
		t.Fatal("another tenant saw the schedule's runs")
	}
}

func TestSummarize(t *testing.T) {
	x := NewIndex()
	// Three lead runs in minute 0 (1s, 2s, 9s; the last one fails), one helper
	// run in minute 2 still going, and one run in another tenant.
	for i, took := range []time.Duration{time.Second, 2 * time.Second, 9 * time.Second} {
		id := string(rune('a' + i))
		record(t, x, "acme", id, "lead", "api", time.Duration(i)*time.Second, nil)
		kind, body := envelope.KindCompleted, payload.Lifecycle{}
		if i == 2 {
			kind, body = envelope.KindFailed, payload.Lifecycle{Error: "x", Terminal: true}
		}
		end(t, x, "acme", id, time.Duration(i)*time.Second+took, kind, body)
	}
	record(t, x, "acme", "d", "helper", "cron", 2*time.Minute, nil)
	record(t, x, "globex", "z", "lead", "api", 0, nil)

	s := x.Summarize("acme", t0, t0.Add(3*time.Minute), time.Minute)
	if s.Total.Runs != 4 || s.Total.Completed != 2 || s.Total.Failed != 1 || s.Total.Active != 1 {
		t.Fatalf("total = %+v", s.Total)
	}
	if s.Total.P50 != 2000 || s.Total.P95 != 9000 {
		t.Fatalf("p50 = %d, p95 = %d", s.Total.P50, s.Total.P95)
	}
	if len(s.Buckets) != 3 || s.Buckets[0].Completed != 2 || s.Buckets[0].Failed != 1 || s.Buckets[2].Active != 1 {
		t.Fatalf("buckets = %+v", s.Buckets)
	}
	if len(s.ByAgent) != 2 || s.ByAgent[0].Name != "lead" || s.ByAgent[0].Runs != 3 {
		t.Fatalf("by agent = %+v", s.ByAgent)
	}
	if len(s.Slowest) != 3 || s.Slowest[0].ID != "c" {
		t.Fatalf("slowest = %+v", s.Slowest)
	}
}

func TestSummarizeBoundsItsBuckets(t *testing.T) {
	s := NewIndex().Summarize("acme", t0, t0.Add(30*24*time.Hour), time.Second)
	if len(s.Buckets) > MaxBuckets+1 {
		t.Fatalf("%d buckets", len(s.Buckets))
	}
	if s.Total.P50 != 0 {
		t.Fatal("percentile of nothing should be 0")
	}
}

func TestRecent(t *testing.T) {
	x := NewIndex()
	record(t, x, "acme", "a", "lead", "api", 0, nil)
	record(t, x, "acme", "b", "helper", "api", time.Minute, nil)
	end(t, x, "acme", "b", 2*time.Minute, envelope.KindFailed, payload.Lifecycle{Terminal: true})
	if total, failed := x.Recent("acme", "", t0); total != 2 || failed != 1 {
		t.Fatalf("all = %d, %d", total, failed)
	}
	if total, failed := x.Recent("acme", "lead", t0); total != 1 || failed != 0 {
		t.Fatalf("lead = %d, %d", total, failed)
	}
	if total, _ := x.Recent("acme", "", t0.Add(30*time.Second)); total != 1 {
		t.Fatalf("since = %d", total)
	}
}

func TestThreadPaging(t *testing.T) {
	x := NewIndex()
	// Seven conversations, each a minute after the last. Odd ones are
	// answered by helper and started by cron, and t3 has a failure.
	for i := range 7 {
		id := "t" + string(rune('0'+i))
		agent, origin := "lead", "api"
		if i%2 == 1 {
			agent, origin = "helper", "cron"
		}
		record(t, x, "acme", id, agent, origin, time.Duration(i)*time.Minute, nil)
		kind, body := envelope.KindCompleted, payload.Lifecycle{}
		if i == 3 {
			kind, body = envelope.KindFailed, payload.Lifecycle{Terminal: true}
		}
		if i != 6 {
			end(t, x, "acme", id, time.Duration(i)*time.Minute+time.Second, kind, body)
		}
	}
	record(t, x, "globex", "other", "lead", "api", 0, nil)
	ids := func(page []ThreadSummary) string {
		out := ""
		for _, s := range page {
			out += s.ID + " "
		}
		return out
	}

	// Pages walk from newest to oldest with no gap and no repeat.
	var seen string
	cursor := ""
	for range 5 {
		page, next, err := x.ThreadPage("acme", ThreadQuery{Limit: 3, Before: cursor})
		if err != nil {
			t.Fatal(err)
		}
		seen += ids(page)
		if cursor = next; cursor == "" {
			break
		}
	}
	if seen != "t6 t5 t4 t3 t2 t1 t0 " {
		t.Fatalf("walked %q", seen)
	}
	// A page that ends exactly at the last conversation has no next.
	if _, next, _ := x.ThreadPage("acme", ThreadQuery{Limit: 7}); next != "" {
		t.Fatalf("a full last page offered a next page: %q", next)
	}

	// Filters apply before the limit.
	page, next, _ := x.ThreadPage("acme", ThreadQuery{Origin: "api", Limit: 2})
	if ids(page) != "t6 t4 " || next == "" {
		t.Fatalf("origin page = %q, next %q", ids(page), next)
	}
	page, _, _ = x.ThreadPage("acme", ThreadQuery{Origin: "api", Limit: 2, Before: next})
	if ids(page) != "t2 t0 " {
		t.Fatalf("origin page two = %q", ids(page))
	}
	if page, _, _ := x.ThreadPage("acme", ThreadQuery{Agent: "helper"}); ids(page) != "t5 t3 t1 " {
		t.Fatalf("agent = %q", ids(page))
	}
	if page, _, _ := x.ThreadPage("acme", ThreadQuery{Standing: StandingFailed}); ids(page) != "t3 " {
		t.Fatalf("failed = %q", ids(page))
	}
	if page, _, _ := x.ThreadPage("acme", ThreadQuery{Standing: StandingRunning}); ids(page) != "t6 " {
		t.Fatalf("running = %q", ids(page))
	}

	// New activity between pages: a conversation on a later page moves to the
	// top. It is not repeated and nothing else is skipped.
	first, next, _ := x.ThreadPage("acme", ThreadQuery{Limit: 3})
	record(t, x, "acme", "t1-again", "helper", "cron", 30*time.Minute, map[string]string{})
	x.runs["t1-again"].Thread = "t1"
	rest, _, _ := x.ThreadPage("acme", ThreadQuery{Limit: 10, Before: next})
	if ids(first) != "t6 t5 t4 " || ids(rest) != "t3 t2 t0 " {
		t.Fatalf("after new activity: first %q, rest %q", ids(first), ids(rest))
	}
	if top, _, _ := x.ThreadPage("acme", ThreadQuery{Limit: 1}); ids(top) != "t1 " {
		t.Fatalf("the newly active conversation is not on top: %q", ids(top))
	}

	// Another tenant, a bad cursor, and the cap.
	if page, _, _ := x.ThreadPage("globex", ThreadQuery{}); ids(page) != "other " {
		t.Fatalf("other tenant = %q", ids(page))
	}
	if _, _, err := x.ThreadPage("acme", ThreadQuery{Before: "not a cursor"}); !errors.Is(err, ErrCursor) {
		t.Fatalf("bad cursor: %v", err)
	}
	if page, _, _ := x.ThreadPage("acme", ThreadQuery{Limit: 100000}); len(page) > MaxThreadPage {
		t.Fatalf("the limit was not capped: %d", len(page))
	}
}

// A run asked to stop is stopping, not stopped, until the runtime says how it
// ended. lifecycle.stopped ends it with its partial answer and the reason the
// runtime honoured. A completion that wins the race ends it completed, with
// no cause and no stopping left on it.
func TestAStopIsRequestedThenConfirmedByTheRuntime(t *testing.T) {
	x := NewIndex()
	record(t, x, "acme", "r1", "lead", "api", 0, nil)
	record(t, x, "acme", "r2", "lead", "api", 0, nil)
	for _, id := range []string{"r1", "r2"} {
		r, ok := x.AskStop(id, t0.Add(time.Second))
		if !ok || !r.Stopping || r.Status.Terminal() || r.Cause != "" {
			t.Fatalf("asked = %+v, %v", r, ok)
		}
	}
	// Asking again keeps the first request.
	if r, _ := x.AskStop("r1", t0.Add(2*time.Second)); !r.StopRequestedAt.Equal(t0.Add(time.Second)) {
		t.Fatalf("a second request moved the first: %+v", r)
	}

	b, _ := payload.Encode(payload.Stopped{Reason: "Stopped by ada", By: "ada", Text: "Half"})
	r, ended := x.Observe(envelope.Event{RunID: "r1", Tenant: "acme", Kind: envelope.KindStopped, Payload: b, At: t0.Add(3 * time.Second)})
	if !ended || r.Status != Stopped || r.Answer != "Half" || r.Cause != "Stopped by ada" || r.Stopping {
		t.Fatalf("stopped = %+v, %v", r, ended)
	}
	r, ended = end(t, x, "acme", "r2", 3*time.Second, envelope.KindCompleted, payload.Lifecycle{Text: "Whole"})
	if !ended || r.Status != Completed || r.Answer != "Whole" || r.Cause != "" || r.Stopping {
		t.Fatalf("completed first = %+v, %v", r, ended)
	}
	if _, ok := x.AskStop("r2", t0.Add(4*time.Second)); ok {
		t.Fatal("a run that ended was asked to stop")
	}
	if _, again := x.Observe(envelope.Event{RunID: "r1", Tenant: "acme", Kind: envelope.KindStopped, Payload: b, At: t0.Add(5 * time.Second)}); again {
		t.Fatal("a redelivered lifecycle.stopped ended the run twice")
	}
}
