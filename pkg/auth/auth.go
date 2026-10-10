// Package auth decides who is calling and what they may do.
//
// # Two kinds of caller, one check
//
// A person signs in through the identity provider and gets roles from their
// groups. A machine presents a client-credentials token and gets exactly the
// scopes it was issued. Both become a Principal, and every handler asks the
// Principal one question: Can(permission).
//
// # Default deny
//
// The zero Principal can do nothing. An unknown role grants nothing, and an
// unknown scope grants nothing. Tenant never comes from the request: it comes
// from the deployment or from a verified claim.
package auth

import (
	"context"
	"errors"
	"slices"
)

// Sentinel errors. Handlers map ErrUnauthenticated to 401 and ErrForbidden to 403.
var (
	ErrUnauthenticated = errors.New("auth: unauthenticated")
	ErrForbidden       = errors.New("auth: forbidden")
)

// Kind separates people from automation, for audit and for the UI.
type Kind string

const (
	User    Kind = "user"
	Machine Kind = "machine"
)

// Permission is one thing a caller may do.
type Permission string

const (
	AgentsRead     Permission = "agents:read"
	AgentsWrite    Permission = "agents:write"
	RunsRead       Permission = "runs:read"
	RunsWrite      Permission = "runs:write"
	SchedulesRead  Permission = "schedules:read"
	SchedulesWrite Permission = "schedules:write"
	AlertsRead     Permission = "alerts:read"
	AlertsWrite    Permission = "alerts:write"
	KnowledgeRead  Permission = "knowledge:read"
	KnowledgeWrite Permission = "knowledge:write"
)

// Permissions lists every permission, for validating configured scopes.
func Permissions() []Permission {
	return []Permission{
		AgentsRead, AgentsWrite, RunsRead, RunsWrite, SchedulesRead, SchedulesWrite,
		AlertsRead, AlertsWrite, KnowledgeRead, KnowledgeWrite,
	}
}

// Role is a named set of permissions granted to a person.
type Role string

const (
	// Viewer reads everything in the tenant and changes nothing.
	Viewer Role = "viewer"
	// Operator runs agents, manages schedules, and acknowledges alerts and
	// sets their rules.
	Operator Role = "operator"
	// Admin also defines and revises agents, and decides what they may be
	// grounded in.
	Admin Role = "admin"
)

var rolePermissions = map[Role][]Permission{
	Viewer:   {AgentsRead, RunsRead, SchedulesRead, AlertsRead, KnowledgeRead},
	Operator: {AgentsRead, RunsRead, RunsWrite, SchedulesRead, SchedulesWrite, AlertsRead, AlertsWrite, KnowledgeRead},
	Admin:    Permissions(),
}

// ParseRole reports whether s names a role.
func ParseRole(s string) (Role, bool) {
	r := Role(s)
	_, ok := rolePermissions[r]
	return r, ok
}

// Principal is an authenticated caller.
type Principal struct {
	// Subject is the provider's stable identifier for the caller.
	Subject string `json:"subject"`
	// Tenant is the authorization scope every read and write is confined to.
	Tenant string `json:"tenant"`
	Kind   Kind   `json:"kind"`
	Name   string `json:"name,omitempty"`
	Email  string `json:"email,omitempty"`
	// Roles apply to people.
	Roles []Role `json:"roles,omitempty"`
	// Scopes apply to machines. A machine has no roles.
	Scopes []Permission `json:"scopes,omitempty"`
}

// Can reports whether the caller holds a permission.
func (p Principal) Can(perm Permission) bool {
	if p.Subject == "" || p.Tenant == "" {
		return false
	}
	if slices.Contains(p.Scopes, perm) {
		return true
	}
	for _, r := range p.Roles {
		if slices.Contains(rolePermissions[r], perm) {
			return true
		}
	}
	return false
}

// Granted lists what the caller may do, for the UI to hide what it cannot.
func (p Principal) Granted() []Permission {
	var out []Permission
	for _, perm := range Permissions() {
		if p.Can(perm) {
			out = append(out, perm)
		}
	}
	return out
}

type ctxKey struct{}

// With returns a context carrying the caller.
func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// From returns the caller, if the request was authenticated.
func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok && p.Subject != ""
}
