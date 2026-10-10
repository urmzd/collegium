package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/urmzd/open-agent-platform/pkg/auth"
	"github.com/urmzd/open-agent-platform/pkg/prefs"
)

// Preferences are a person's own. Every route here acts on the caller and takes
// no subject, so there is no way to name someone else.
func (s *Server) preferenceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/preferences", s.authed("", s.getPreferences))
	mux.HandleFunc("PUT /api/preferences", s.authed("", s.putPreferences))
	mux.HandleFunc("GET /api/preferences/photo", s.authed("", s.getPhoto))
	mux.HandleFunc("PUT /api/preferences/photo", s.authed("", s.putPhoto))
	mux.HandleFunc("DELETE /api/preferences/photo", s.authed("", s.deletePhoto))
}

func (s *Server) getPreferences(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	ok(w, http.StatusOK, s.P.Prefs.Get(who.Tenant, who.Subject))
}

func (s *Server) putPreferences(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body struct {
		DisplayName string      `json:"display_name"`
		Theme       prefs.Theme `json:"theme"`
		Accent      string      `json:"accent"`
	}
	if !decode(w, r, &body) {
		return
	}
	next, err := s.P.Prefs.Set(who.Tenant, who.Subject, prefs.Preferences{
		DisplayName: body.DisplayName, Theme: body.Theme, Accent: body.Accent,
	})
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, next)
}

func (s *Server) putPhoto(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	// One byte over the limit is read so an oversized upload is told apart
	// from one that fits exactly.
	data, err := io.ReadAll(io.LimitReader(r.Body, prefs.MaxPhoto+1))
	if err != nil {
		fail(w, http.StatusBadRequest, "the picture could not be read")
		return
	}
	next, err := s.P.Prefs.SetPhoto(who.Tenant, who.Subject, data)
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, next)
}

func (s *Server) deletePhoto(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	ok(w, http.StatusOK, s.P.Prefs.ClearPhoto(who.Tenant, who.Subject))
}

func (s *Server) getPhoto(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	data, mime, err := s.P.Prefs.Photo(who.Tenant, who.Subject)
	if errors.Is(err, prefs.ErrNoPhoto) {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	h := w.Header()
	// The type is the one the server determined from the bytes, and the
	// browser is told not to second-guess it or run anything from it.
	h.Set("Content-Type", mime)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Content-Disposition", "inline")
	h.Set("Cache-Control", "private, no-cache")
	_, _ = w.Write(data)
}
