package runs

import (
	"testing"
	"time"

	"github.com/urmzd/opentag/pkg/agentrt/payload"
	"github.com/urmzd/opentag/pkg/envelope"
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
