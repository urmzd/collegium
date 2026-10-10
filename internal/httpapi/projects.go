package httpapi

import (
	"net/http"
	"time"

	"github.com/urmzd/open-agent-platform/internal/platform"
	"github.com/urmzd/open-agent-platform/pkg/auth"
	"github.com/urmzd/open-agent-platform/pkg/memory"
	"github.com/urmzd/open-agent-platform/pkg/runs"
)

// A project gathers what belongs together: conversations, the schedules that
// feed it, and notes every agent working in it is sent. Anyone who can start a
// run can make a project and write to its memory. Reading needs runs:read.
func (s *Server) projectRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects", s.authed(auth.RunsRead, s.listProjects))
	mux.HandleFunc("POST /api/projects", s.authed(auth.RunsWrite, s.createProject))
	mux.HandleFunc("DELETE /api/projects/{name}", s.authed(auth.RunsWrite, s.deleteProject))
	mux.HandleFunc("GET /api/projects/{name}/memory", s.authed(auth.RunsRead, s.projectMemory))
	mux.HandleFunc("POST /api/projects/{name}/memory", s.authed(auth.RunsWrite, s.addProjectMemory))
	mux.HandleFunc("POST /api/projects/{name}/memory/approve", s.authed(auth.RunsWrite, s.approveProjectMemory))
	mux.HandleFunc("DELETE /api/projects/{name}/memory/{id}", s.authed(auth.RunsWrite, s.removeProjectMemory))
}

// projectView is a project and what is in it.
type projectView struct {
	platform.Project
	Conversations int `json:"conversations"`
	// Active is how many of its runs are in flight.
	Active    int `json:"active"`
	Schedules int `json:"schedules"`
	// Notes is how many shared notes are in use, and Proposed how many wait
	// for someone to approve them.
	Notes     int       `json:"notes"`
	Proposed  int       `json:"proposed"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

func (s *Server) listProjects(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	threads := s.P.Runs.Threads(who.Tenant, 0)
	schedules := s.P.Schedules.List(who.Tenant)
	out := []projectView{}
	for _, pr := range s.P.Settings(who.Tenant).Projects {
		v := projectView{Project: pr}
		for _, t := range threads {
			if t.Project != pr.Name {
				continue
			}
			v.Conversations++
			v.Active += t.Active
			if t.UpdatedAt.After(v.UpdatedAt) {
				v.UpdatedAt = t.UpdatedAt
			}
		}
		for _, sc := range schedules {
			if sc.Meta[runs.MetaProject] == pr.Name {
				v.Schedules++
			}
		}
		for _, n := range s.P.Memory.List(who.Tenant, memory.ProjectOwner(pr.Name)) {
			if n.Status == memory.Approved {
				v.Notes++
			} else {
				v.Proposed++
			}
		}
		out = append(out, v)
	}
	ok(w, http.StatusOK, map[string]any{"projects": out})
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body platform.Project
	if !decode(w, r, &body) {
		return
	}
	if err := s.P.CreateProject(r.Context(), who.Tenant, body); err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusCreated, body)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	if err := s.P.DeleteProject(r.Context(), who.Tenant, r.PathValue("name")); err != nil {
		s.problem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// owner resolves the project in the path to its memory owner, or answers 404.
func (s *Server) owner(w http.ResponseWriter, r *http.Request, who auth.Principal) (string, bool) {
	name := r.PathValue("name")
	if !s.P.HasProject(who.Tenant, name) {
		s.problem(w, platform.ErrProjectNotFound)
		return "", false
	}
	return memory.ProjectOwner(name), true
}

func (s *Server) projectMemory(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	owner, found := s.owner(w, r, who)
	if !found {
		return
	}
	ok(w, http.StatusOK, map[string]any{"notes": orEmpty(s.P.Memory.List(who.Tenant, owner))})
}

func (s *Server) addProjectMemory(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &body) {
		return
	}
	owner, found := s.owner(w, r, who)
	if !found {
		return
	}
	note, err := s.P.Memory.Add(who.Tenant, owner, body.Text, memory.Person, memory.Approved, who.Subject, time.Now())
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusCreated, note)
}

func (s *Server) approveProjectMemory(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, &body) {
		return
	}
	owner, found := s.owner(w, r, who)
	if !found {
		return
	}
	n, err := s.P.Memory.Approve(who.Tenant, owner, body.IDs)
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, map[string]int{"approved": n})
}

func (s *Server) removeProjectMemory(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	owner, found := s.owner(w, r, who)
	if !found {
		return
	}
	if err := s.P.Memory.Remove(who.Tenant, owner, r.PathValue("id")); err != nil {
		s.problem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
