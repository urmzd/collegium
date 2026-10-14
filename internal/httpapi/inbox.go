package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/auth"
	"github.com/urmzd/collegium/pkg/inbox"
)

// The inbox routes ask only for runs:read, which every role has. What a caller
// sees in the inbox, and what they may do with each part, is decided per
// message by the platform: resolving a part needs the right for the thing it
// does, not the right to open an inbox.
func (s *Server) inboxRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/inbox", s.authed(auth.RunsRead, s.listInbox))
	mux.HandleFunc("GET /api/inbox/count", s.authed(auth.RunsRead, s.countInbox))
	mux.HandleFunc("GET /api/inbox/{id}", s.authed(auth.RunsRead, s.getInbox))
	mux.HandleFunc("POST /api/inbox/{id}/read", s.authed(auth.RunsRead, s.readInbox))
	mux.HandleFunc("POST /api/inbox/{id}/dismiss", s.authed(auth.RunsRead, s.dismissInbox))
	mux.HandleFunc("POST /api/inbox/{id}/snooze", s.authed(auth.RunsRead, s.snoozeInbox))
	mux.HandleFunc("POST /api/inbox/{id}/parts/{part}/resolve", s.authed(auth.RunsRead, s.resolveInbox))
}

// listInbox pages the caller's inbox, newest first.
//
// state is open (the default), resolved, dismissed, snoozed or all. kind,
// unread, alert, limit and before are optional. alert finds the message about
// one alert, in any state. The answer carries next, the cursor
// for the page after this one, or null at the end.
func (s *Server) listInbox(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	q := r.URL.Query()
	query := inbox.Query{State: inbox.State(q.Get("state")), Kind: inbox.Kind(q.Get("kind")), Before: q.Get("before"), Unread: q.Get("unread") == "true", Alert: q.Get("alert")}
	// Asking after one alert means its message, wherever it stands.
	if query.Alert != "" && query.State == "" {
		query.State = inbox.All
	}
	switch query.State {
	case "", inbox.Open, inbox.Resolved, inbox.Dismissed, inbox.Snoozed, inbox.All:
	default:
		fail(w, http.StatusBadRequest, "state is open, resolved, dismissed, snoozed or all")
		return
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			fail(w, http.StatusBadRequest, "limit is a number above zero")
			return
		}
		query.Limit = n
	}
	page, next, err := s.P.InboxPage(who, query)
	if err != nil {
		fail(w, http.StatusBadRequest, "before is not a cursor this server issued")
		return
	}
	var cursor *string
	if next != "" {
		cursor = &next
	}
	open, unread := s.P.InboxCount(who)
	ok(w, http.StatusOK, map[string]any{"messages": page, "next": cursor, "open": open, "unread": unread})
}

func (s *Server) countInbox(w http.ResponseWriter, _ *http.Request, who auth.Principal) {
	open, unread := s.P.InboxCount(who)
	ok(w, http.StatusOK, map[string]int{"open": open, "unread": unread})
}

func (s *Server) getInbox(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	m, err := s.P.InboxGet(who, r.PathValue("id"))
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, map[string]any{"message": m, "history": orEmpty(s.P.Inbox.History(who.Tenant, m.ID))})
}

func (s *Server) readInbox(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	body := struct {
		Read *bool `json:"read"`
	}{}
	if !decode(w, r, &body) {
		return
	}
	m, err := s.P.InboxRead(who, r.PathValue("id"), body.Read == nil || *body.Read)
	s.inboxAnswer(w, m, err)
}

func (s *Server) dismissInbox(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	m, err := s.P.InboxDismiss(who, r.PathValue("id"))
	s.inboxAnswer(w, m, err)
}

func (s *Server) snoozeInbox(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body struct {
		Until time.Time `json:"until"`
	}
	if !decode(w, r, &body) {
		return
	}
	m, err := s.P.InboxSnooze(who, r.PathValue("id"), body.Until)
	s.inboxAnswer(w, m, err)
}

func (s *Server) resolveInbox(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body struct {
		Action string       `json:"action"`
		IDs    []string     `json:"ids"`
		Note   string       `json:"note"`
		Limits inbox.Limits `json:"limits"`
		answerBody
	}
	if !decode(w, r, &body) {
		return
	}
	if !body.valid(w, body.Note) {
		return
	}
	m, err := s.P.InboxResolve(r.Context(), who, r.PathValue("id"), r.PathValue("part"),
		platform.Resolution{
			Action: body.Action, IDs: body.IDs, Note: body.Note, Limits: body.Limits,
			Arguments: body.Arguments, Choice: body.Choice, Text: body.Text, ActAs: body.ActAs,
		})
	s.inboxAnswer(w, m, err)
}

// answerBody is what answers a question, besides the action and a note.
type answerBody struct {
	// Arguments replace the model's on an approval, as a JSON object that
	// satisfies the tool's schema.
	Arguments json.RawMessage `json:"arguments"`
	Choice    string          `json:"choice"`
	Text      string          `json:"text"`
	// ActAs is the person a surface answers for.
	ActAs *platform.ActAs `json:"act_as"`
}

// Bounds on an answer. The runtime clips what it records further.
const (
	maxNote      = 500
	maxReply     = 4000
	maxArguments = 64 << 10
)

func (b answerBody) valid(w http.ResponseWriter, note string) bool {
	switch {
	case len(note) > maxNote:
		fail(w, http.StatusBadRequest, "a note is at most 500 characters")
	case len(b.Text) > maxReply || len(b.Choice) > maxReply:
		fail(w, http.StatusBadRequest, "an answer is at most 4000 characters")
	case len(b.Arguments) > maxArguments:
		fail(w, http.StatusBadRequest, "edited arguments are at most 64 KB")
	default:
		return true
	}
	return false
}

// answerQuestion answers the question a run is parked on, as the inbox's
// resolve does for its message. It is how a surface showing the run answers
// it in place.
func (s *Server) answerQuestion(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	var body struct {
		Action string `json:"action"`
		Note   string `json:"note"`
		answerBody
	}
	if !decode(w, r, &body) {
		return
	}
	if !body.valid(w, body.Note) {
		return
	}
	m, err := s.P.AnswerQuestion(r.Context(), who, r.PathValue("id"), r.PathValue("question"), platform.QuestionAnswer{
		Action: body.Action, Arguments: body.Arguments, Choice: body.Choice, Text: body.Text, Note: body.Note, ActAs: body.ActAs,
	})
	s.inboxAnswer(w, m, err)
}

func (s *Server) inboxAnswer(w http.ResponseWriter, m inbox.Message, err error) {
	if err != nil {
		s.problem(w, err)
		return
	}
	ok(w, http.StatusOK, m)
}
