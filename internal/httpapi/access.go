package httpapi

import (
	"net/http"
	"slices"

	"github.com/urmzd/collegium/pkg/auth"
	"github.com/urmzd/collegium/pkg/authz"
)

// accessDecision is why the caller may or may not do one thing. It is only
// ever about the caller: the route takes a permission and no subject, so
// there is no way to ask about someone else.
type accessDecision struct {
	Permission auth.Permission `json:"permission"`
	Allowed    bool            `json:"allowed"`
	// Outcome is "allowed", "denied" (a rule denies it, which beats every
	// allow), "no match" (no rule mentions it, and the default is deny) or
	// "scope" (a machine's token scope decided, not the policy).
	Outcome string `json:"outcome"`
	// Role is the held role the deciding rule reached the caller through.
	Role string `json:"role,omitempty"`
	// Path runs from Role to the role that declares Rule.
	Path []string `json:"path,omitempty"`
	// Rule is the deciding rule as the policy writes it.
	Rule string `json:"rule,omitempty"`
	// Grant is the grant that gave Role, when the sign-in kept it.
	Grant string `json:"grant,omitempty"`
	// Held is every role the decision considered.
	Held []heldRole `json:"held"`
	// Unknown lists roles the caller holds that the policy does not declare.
	Unknown []string `json:"unknown,omitempty"`
}

// heldRole is one role the caller holds and the grant that gave it.
type heldRole struct {
	Role string `json:"role"`
	// Grant is the grant as the policy writes it, when known.
	Grant string `json:"grant,omitempty"`
	// Group is the group the grant names, when it is a group grant.
	Group string `json:"group,omitempty"`
}

func decisionOf(d auth.Decision) accessDecision {
	out := accessDecision{Permission: d.Permission, Allowed: d.Allowed, Held: []heldRole{}}
	if d.Scope {
		out.Outcome = "scope"
		return out
	}
	e := d.Policy
	out.Outcome, out.Role, out.Path, out.Unknown = string(e.Outcome), e.Role, e.Path, e.Unknown
	if e.Rule != nil {
		out.Rule = e.Rule.String()
	}
	if e.Grant != nil {
		out.Grant = e.Grant.String()
	}
	for _, h := range e.Held {
		r := heldRole{Role: h.Role}
		if h.Grant != nil {
			r.Grant = h.Grant.String()
			if h.Grant.Subject.Kind == authz.SubjectGroup {
				r.Group = h.Grant.Subject.Value
			}
		}
		out.Held = append(out.Held, r)
	}
	return out
}

// explainAccess says why the caller may or may not do one thing.
func (s *Server) explainAccess(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	perm := auth.Permission(r.URL.Query().Get("permission"))
	if !slices.Contains(auth.Permissions(), perm) {
		fail(w, http.StatusBadRequest, "permission must name one permission, such as runs:read")
		return
	}
	ok(w, http.StatusOK, decisionOf(who.Policy().Explain(who, perm)))
}
