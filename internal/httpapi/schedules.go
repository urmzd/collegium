package httpapi

import (
	"net/http"
	"time"

	"github.com/urmzd/opentag/pkg/connectors/cron"
	"github.com/urmzd/opentag/pkg/envelope"

	"github.com/urmzd/open-agent-platform/internal/platform"
	"github.com/urmzd/open-agent-platform/pkg/auth"
	"github.com/urmzd/open-agent-platform/pkg/runs"
)

// schedule is the wire form of a cron schedule. Tenant is absent on purpose.
type schedule struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
	Agent      string `json:"agent"`
	Text       string `json:"text"`
	// Project files every run the schedule raises under a project.
	Project string `json:"project,omitempty"`
}

// scheduleView is a schedule with what a person needs to trust it: whether it
// is running, when it fires next, and how its recent occurrences went.
type scheduleView struct {
	schedule
	Paused    bool        `json:"paused"`
	CreatedBy string      `json:"created_by,omitempty"`
	Next      []time.Time `json:"next"`
	Recent    []runs.Run  `json:"recent"`
}

func (s *Server) viewOf(sc cron.Schedule, now time.Time) scheduleView {
	v := scheduleView{
		schedule: schedule{
			Name: sc.Name, Expression: sc.Expression, Agent: sc.Agent, Text: sc.Text,
			Project: sc.Meta[runs.MetaProject],
		},
		Paused:    s.P.Schedules.Paused(sc.Name),
		CreatedBy: sc.Meta["created_by"],
		Next:      []time.Time{},
		Recent:    orEmpty(s.P.Runs.ForSchedule(sc.Tenant, sc.Name, 20)),
	}
	if !v.Paused {
		v.Next = orEmpty(platform.Upcoming(sc.Expression, now, 3))
	}
	return v
}

func (s *Server) listSchedules(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	now := time.Now()
	out := []scheduleView{}
	for _, sc := range s.P.Schedules.List(who.Tenant) {
		out = append(out, s.viewOf(sc, now))
	}
	ok(w, http.StatusOK, map[string]any{"schedules": out})
}

func (s *Server) createSchedule(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body schedule
	if !decode(w, r, &body) {
		return
	}
	// A schedule runs an agent with nobody present, so the agent must exist
	// in the caller's tenant now rather than fail every occurrence later.
	if _, err := s.P.Agents.Get(r.Context(), who.Tenant, body.Agent, 0); err != nil {
		s.problem(w, err)
		return
	}
	meta := map[string]string{"created_by": who.Subject}
	if body.Project != "" {
		if !s.P.HasProject(who.Tenant, body.Project) {
			s.problem(w, platform.ErrProjectNotFound)
			return
		}
		meta[runs.MetaProject] = body.Project
	}
	sc := cron.Schedule{
		Name: body.Name, Expression: body.Expression, Agent: body.Agent, Text: body.Text,
		Tenant: who.Tenant,
		Meta:   meta,
	}
	if err := s.P.Schedules.Add(sc); err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusCreated, s.viewOf(sc, time.Now()))
}

func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	if err := s.P.Schedules.Remove(who.Tenant, r.PathValue("name")); err != nil {
		s.problem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pauseSchedule(paused bool) handler {
	return func(w http.ResponseWriter, r *http.Request, who auth.Principal) {
		name := r.PathValue("name")
		if err := s.P.Schedules.SetPaused(who.Tenant, name, paused); err != nil {
			s.problem(w, err)
			return
		}
		sc, _, err := s.P.Schedules.Get(who.Tenant, name)
		if err != nil {
			s.problem(w, err)
			return
		}
		ok(w, http.StatusOK, s.viewOf(sc, time.Now()))
	}
}

// runSchedule fires a schedule once, now, on a person's say-so. The run is
// attributed to that person, not to the clock, and works while paused.
func (s *Server) runSchedule(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	sc, _, err := s.P.Schedules.Get(who.Tenant, r.PathValue("name"))
	if err != nil {
		s.problem(w, err)
		return
	}
	now := time.Now()
	run, err := s.P.Invoke(r.Context(), envelope.Tag{
		ID:     "manual-" + token()[:22],
		Tenant: who.Tenant,
		Agent:  sc.Agent,
		Origin: runs.OriginManual,
		Text:   sc.Text,
		Actor:  envelope.Actor{ID: who.Subject, Display: who.Name, Bot: who.Kind == auth.Machine},
		Meta: map[string]string{
			"cron_schedule": sc.Name, "cron_expression": sc.Expression,
			runs.MetaProject: sc.Meta[runs.MetaProject],
		},
		At: now,
	})
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusAccepted, run)
}
