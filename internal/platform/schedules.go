package platform

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/urmzd/mandatum/pkg/connectors/cron"
)

// Schedule errors.
var (
	ErrScheduleExists   = errors.New("platform: schedule already exists")
	ErrScheduleNotFound = errors.New("platform: schedule not found")
)

// Schedules is the store the cron connector reads. It is in memory, so
// schedules are lost on restart until the Postgres store lands.
type Schedules struct {
	mu     sync.RWMutex
	all    []cron.Schedule
	paused map[string]bool
}

var _ cron.Store = (*Schedules)(nil)

// Schedules implements cron.Store. It returns every tenant's schedules, because
// the connector is the deployment's and each schedule carries its own tenant.
//
// A paused schedule is left out, which is all pausing is: the connector forgets
// a schedule it is no longer shown, and starts it fresh when it reappears, so a
// resumed schedule does not fire for the occurrences it slept through.
func (s *Schedules) Schedules(context.Context) ([]cron.Schedule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]cron.Schedule, 0, len(s.all))
	for _, sc := range s.all {
		if !s.paused[sc.Name] {
			out = append(out, sc)
		}
	}
	return out, nil
}

// Get returns one of a tenant's schedules and whether it is paused.
func (s *Schedules) Get(tenant, name string) (cron.Schedule, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sc := range s.all {
		if sc.Name == name && sc.Tenant == tenant {
			return sc, s.paused[name], nil
		}
	}
	return cron.Schedule{}, false, fmt.Errorf("%w: %q", ErrScheduleNotFound, name)
}

// Paused reports whether a schedule is paused.
func (s *Schedules) Paused(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.paused[name]
}

// SetPaused pauses or resumes a tenant's schedule.
func (s *Schedules) SetPaused(tenant, name string, paused bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sc := range s.all {
		if sc.Name == name && sc.Tenant == tenant {
			if s.paused == nil {
				s.paused = map[string]bool{}
			}
			if paused {
				s.paused[name] = true
			} else {
				delete(s.paused, name)
			}
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrScheduleNotFound, name)
}

// Upcoming returns the next n times an expression fires after a time, in UTC.
func Upcoming(expression string, after time.Time, n int) []time.Time {
	spec, err := cron.Parse(expression)
	if err != nil {
		return nil
	}
	out := make([]time.Time, 0, n)
	t := after.UTC()
	for range n {
		next, ok := spec.Next(t)
		if !ok {
			break
		}
		out = append(out, next)
		t = next
	}
	return out
}

// List returns one tenant's schedules, by name.
func (s *Schedules) List(tenant string) []cron.Schedule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []cron.Schedule
	for _, sc := range s.all {
		if sc.Tenant == tenant {
			out = append(out, sc)
		}
	}
	slices.SortFunc(out, func(a, b cron.Schedule) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Add stores a schedule. Names are unique across tenants, because a schedule's
// name is part of the id of every run it raises.
func (s *Schedules) Add(sc cron.Schedule) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, have := range s.all {
		if have.Name == sc.Name {
			return fmt.Errorf("%w: %q", ErrScheduleExists, sc.Name)
		}
	}
	s.all = append(s.all, sc)
	return nil
}

// Remove deletes a tenant's schedule.
func (s *Schedules) Remove(tenant, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, have := range s.all {
		if have.Name == name && have.Tenant == tenant {
			s.all = slices.Delete(s.all, i, i+1)
			delete(s.paused, name)
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrScheduleNotFound, name)
}
