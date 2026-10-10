package runs

import (
	"cmp"
	"slices"
	"time"
)

// Bucket is the runs that started in one interval, by how they ended.
type Bucket struct {
	Start     time.Time `json:"start"`
	Completed int       `json:"completed"`
	Failed    int       `json:"failed"`
	// Active is runs that have not ended: queued, running or parked.
	Active int `json:"active"`
}

// Group is one slice of the runs: an agent, an origin or a schedule.
type Group struct {
	Name      string `json:"name"`
	Runs      int    `json:"runs"`
	Completed int    `json:"completed"`
	Failed    int    `json:"failed"`
	Active    int    `json:"active"`
	// P50 and P95 are durations of ended runs, in milliseconds.
	P50 int64 `json:"p50_ms"`
	P95 int64 `json:"p95_ms"`
}

// Summary is what happened in a window.
type Summary struct {
	Since  time.Time     `json:"since"`
	Until  time.Time     `json:"until"`
	Bucket time.Duration `json:"bucket_ns"`

	Total     Group    `json:"total"`
	Buckets   []Bucket `json:"buckets"`
	ByAgent   []Group  `json:"by_agent"`
	ByOrigin  []Group  `json:"by_origin"`
	Slowest   []Run    `json:"slowest"`
	Delegated int      `json:"delegated"`
}

// MaxBuckets bounds a summary, so a narrow bucket over a wide window cannot
// ask for an unbounded result.
const MaxBuckets = 240

// Summarize aggregates a tenant's runs that started in [since, until). bucket
// is widened if the window would need more than MaxBuckets of them.
func (x *Index) Summarize(tenant string, since, until time.Time, bucket time.Duration) Summary {
	if bucket <= 0 {
		bucket = time.Minute
	}
	if span := until.Sub(since); span > 0 && span/bucket > MaxBuckets {
		bucket = (span / MaxBuckets).Truncate(time.Second) + time.Second
	}
	since = since.Truncate(bucket)

	s := Summary{Since: since, Until: until, Bucket: bucket, Total: Group{Name: "all"}}
	for t := since; t.Before(until); t = t.Add(bucket) {
		s.Buckets = append(s.Buckets, Bucket{Start: t})
	}

	type acc struct {
		g         Group
		durations []int64
	}
	total := &acc{g: s.Total}
	agents, origins := map[string]*acc{}, map[string]*acc{}
	at := func(m map[string]*acc, name string) *acc {
		a, ok := m[name]
		if !ok {
			a = &acc{g: Group{Name: name}}
			m[name] = a
		}
		return a
	}

	x.mu.RLock()
	for _, id := range x.order {
		r := x.runs[id]
		if r.Tenant != tenant || r.CreatedAt.Before(since) || !r.CreatedAt.Before(until) {
			continue
		}
		var b *Bucket
		if i := int(r.CreatedAt.Sub(since) / bucket); i >= 0 && i < len(s.Buckets) {
			b = &s.Buckets[i]
		}
		if r.Parent != "" {
			s.Delegated++
		}
		for _, a := range []*acc{total, at(agents, r.Agent), at(origins, r.Origin)} {
			a.g.Runs++
			switch r.Status {
			case Completed:
				a.g.Completed++
			case Failed:
				a.g.Failed++
			default:
				a.g.Active++
			}
			if r.Status.Terminal() {
				a.durations = append(a.durations, r.UpdatedAt.Sub(r.CreatedAt).Milliseconds())
			}
		}
		if b != nil {
			switch r.Status {
			case Completed:
				b.Completed++
			case Failed:
				b.Failed++
			default:
				b.Active++
			}
		}
		if r.Status.Terminal() {
			s.Slowest = append(s.Slowest, *r)
		}
	}
	x.mu.RUnlock()

	finish := func(a *acc) Group {
		slices.Sort(a.durations)
		a.g.P50, a.g.P95 = percentile(a.durations, 50), percentile(a.durations, 95)
		return a.g
	}
	groups := func(m map[string]*acc) []Group {
		out := make([]Group, 0, len(m))
		for _, a := range m {
			out = append(out, finish(a))
		}
		slices.SortFunc(out, func(a, b Group) int {
			return cmp.Or(cmp.Compare(b.Runs, a.Runs), cmp.Compare(a.Name, b.Name))
		})
		return out
	}
	s.Total = finish(total)
	s.ByAgent, s.ByOrigin = groups(agents), groups(origins)

	slices.SortFunc(s.Slowest, func(a, b Run) int {
		return cmp.Compare(b.UpdatedAt.Sub(b.CreatedAt), a.UpdatedAt.Sub(a.CreatedAt))
	})
	if len(s.Slowest) > 5 {
		s.Slowest = s.Slowest[:5]
	}
	return s
}

// percentile returns the nearest-rank percentile of sorted values, or 0 for none.
func percentile(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := (p*len(sorted) + 99) / 100
	return sorted[max(rank, 1)-1]
}

// ForSchedule returns a tenant's runs raised by one schedule, newest first.
func (x *Index) ForSchedule(tenant, schedule string, limit int) []Run {
	x.mu.RLock()
	defer x.mu.RUnlock()
	var out []Run
	for _, id := range slices.Backward(x.order) {
		if r := x.runs[id]; r.Tenant == tenant && r.Schedule == schedule {
			out = append(out, *r)
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out
}
