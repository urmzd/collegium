package httpapi

import (
	"net/http"
	"testing"
)

// A person signed in through the browser learns why they may or may not do
// something, down to the grant that gave them the role, and only about
// themselves.
func TestExplainNamesTheCallersGrant(t *testing.T) {
	f := newFixture(t)
	viewer := f.signIn(t, "collegium-viewers")

	res, got := f.do(t, viewer, "GET", "/api/access/explain?permission=runs:read", "", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("explain = %d %v", res.StatusCode, got)
	}
	if got["allowed"] != true || got["outcome"] != "allowed" || got["role"] != "viewer" {
		t.Fatalf("got %v", got)
	}
	if got["rule"] != "allow read on *" || got["grant"] != `grant viewer to group "collegium-viewers"` {
		t.Fatalf("rule %v, grant %v", got["rule"], got["grant"])
	}
	held, _ := got["held"].([]any)
	if len(held) != 1 || held[0].(map[string]any)["group"] != "collegium-viewers" {
		t.Fatalf("held %v", got["held"])
	}

	res, got = f.do(t, viewer, "GET", "/api/access/explain?permission=agents:write", "", "")
	if res.StatusCode != http.StatusOK || got["allowed"] != false || got["outcome"] != "no match" || got["rule"] != nil {
		t.Fatalf("explain agents:write = %d %v", res.StatusCode, got)
	}

	// There is no way to ask about someone else: a subject is ignored, and
	// the answer is still the caller's own.
	admin := f.signIn(t, "collegium-admins")
	_, mine := f.do(t, admin, "GET", "/api/access/explain?permission=agents:write&subject=00u-other", "", "")
	if mine["allowed"] != true || mine["role"] != "admin" {
		t.Fatalf("got %v", mine)
	}

	if res, _ := f.do(t, viewer, "GET", "/api/access/explain", "", ""); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("no permission = %d", res.StatusCode)
	}
	if res, _ := f.do(t, nil, "GET", "/api/access/explain?permission=runs:read", "", ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed out = %d", res.StatusCode)
	}

	// A machine is decided by its token's scopes, and is told so.
	_, m := f.do(t, nil, "GET", "/api/access/explain?permission=runs:read", f.machine(t, "acme", "collegium:runs:read"), "")
	if m["allowed"] != true || m["outcome"] != "scope" {
		t.Fatalf("machine got %v", m)
	}
}
