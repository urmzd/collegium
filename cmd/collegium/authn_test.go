package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/urmzd/collegium/internal/httpapi"
	"github.com/urmzd/collegium/internal/platform"
	"github.com/urmzd/collegium/pkg/auth"
	"github.com/urmzd/collegium/pkg/call"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// server starts the platform behind -auth=mock exactly as serve wires it, and
// returns its public URL.
func mockServer(t *testing.T, policyFile string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	public := "http://" + ln.Addr().String()

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	groups := addGroupFlags(fs)
	pol, groupRoles, err := loadPolicy(fs, groups, env(map[string]string{policyEnv: policyFile}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	authn, stop, err := newAuthn(ctx, authConfig{
		mode: "mock", addr: ln.Addr().String(), publicURL: public,
		tenant: "acme", groups: "groups", policy: pol, groupRoles: groupRoles,
		mockAddr: "127.0.0.1:0", getenv: env(nil), log: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := platform.New(platform.Config{Workspace: t.TempDir(), Limits: call.Limits{MaxDepth: 2, MaxFanOut: 4}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	srv := &http.Server{Handler: (&httpapi.Server{P: p, Authn: authn, Log: slog.New(slog.DiscardHandler)}).Handler()}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		stop()
		cancel()
		<-done
	})
	return public
}

type me struct {
	Principal struct {
		Subject string      `json:"subject"`
		Name    string      `json:"name"`
		Tenant  string      `json:"tenant"`
		Kind    string      `json:"kind"`
		Roles   []auth.Role `json:"roles"`
	} `json:"principal"`
	Permissions []auth.Permission `json:"permissions"`
}

// signIn drives a browser through the server's own sign-in: /auth/login,
// the provider's persona page, the callback and the session cookie.
func signIn(t *testing.T, public, persona string) me {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}
	res, err := browser.Get(public + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(page), "Development identity provider") {
		t.Fatalf("login landed on %s (%d)", res.Request.URL, res.StatusCode)
	}
	form := res.Request.URL.Query()
	form.Set("persona", persona)
	authorize := *res.Request.URL
	authorize.RawQuery = ""
	res, err = browser.PostForm(authorize.String(), form)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if !strings.HasPrefix(res.Request.URL.String(), public) {
		t.Fatalf("sign-in ended at %s", res.Request.URL)
	}
	res, err = browser.Get(public + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("/api/me %d: %s", res.StatusCode, body)
	}
	var m me
	if err := json.NewDecoder(res.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMockSignInForEachDefaultPersona(t *testing.T) {
	public := mockServer(t, "")
	viewer := []auth.Permission{auth.AgentsRead, auth.RunsRead, auth.SchedulesRead, auth.AlertsRead, auth.KnowledgeRead, auth.EvalsRead, auth.BillingRead, auth.ArtifactsRead, auth.ClientsRead}
	for _, tc := range []struct {
		persona string
		roles   []auth.Role
		want    []auth.Permission
	}{
		{"mock-viewer", []auth.Role{auth.Viewer}, viewer},
		{"mock-operator", []auth.Role{auth.Operator}, auth.DefaultPolicy().Permissions([]auth.Role{auth.Operator})},
		{"mock-admin", []auth.Role{auth.Admin}, auth.Permissions()},
		{"mock-nobody", nil, nil},
	} {
		t.Run(tc.persona, func(t *testing.T) {
			m := signIn(t, public, tc.persona)
			if m.Principal.Subject != tc.persona || m.Principal.Tenant != "acme" || m.Principal.Kind != "user" {
				t.Fatalf("principal %+v", m.Principal)
			}
			if !slices.Equal(m.Principal.Roles, tc.roles) || !slices.Equal(m.Permissions, tc.want) {
				t.Fatalf("roles %v, permissions %v; want %v, %v", m.Principal.Roles, m.Permissions, tc.roles, tc.want)
			}
		})
	}
}

func TestMockSignInUnderAPolicyFile(t *testing.T) {
	path := writeFile(t, "p.authz", `
role viewer  { allow read on * }
role auditor extends viewer { deny read on billing }
grant viewer  to anyone
grant auditor to group "collegium-admins"
`)
	m := signIn(t, mockServer(t, path), "mock-admin")
	// With a policy file, the default group flags no longer map
	// collegium-admins to admin: the file decides.
	if !slices.Equal(m.Principal.Roles, []auth.Role{auth.Viewer, "auditor"}) {
		t.Fatalf("roles %v", m.Principal.Roles)
	}
	if slices.Contains(m.Permissions, auth.BillingRead) || !slices.Contains(m.Permissions, auth.RunsRead) || slices.Contains(m.Permissions, auth.RunsWrite) {
		t.Fatalf("permissions %v", m.Permissions)
	}
}

func TestServeRefusesABadPolicy(t *testing.T) {
	for name, src := range map[string]string{
		"syntax":       "role viewer { allow read on }",
		"unknown role": "role a { allow read on * }\ngrant b to anyone",
		"empty":        "# nothing here",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(policyEnv, writeFile(t, "p.authz", src))
			err := run([]string{"serve", "-auth=dev", "-addr", "127.0.0.1:0"})
			if err == nil || !strings.Contains(err.Error(), policyEnv) {
				t.Fatalf("got %v", err)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		t.Setenv(policyEnv, filepath.Join(t.TempDir(), "missing.authz"))
		if err := run([]string{"serve", "-auth=dev", "-addr", "127.0.0.1:0"}); err == nil {
			t.Fatal("started")
		}
	})
}

func TestGroupFlagsWithAPolicyFile(t *testing.T) {
	path := writeFile(t, "p.authz", "role viewer { allow read on * }")
	for name, tc := range map[string]struct {
		file    string
		args    []string
		want    int
		wantErr bool
	}{
		"without a file, the defaults apply": {"", nil, 3, false},
		"with a file, the defaults do not":   {path, nil, 0, false},
		"with a file, an explicit flag does": {path, []string{"-viewer-group", "readers"}, 1, false},
		"and must name a declared role":      {path, []string{"-admin-group", "ops"}, 1, true},
	} {
		t.Run(name, func(t *testing.T) {
			fs := flag.NewFlagSet("serve", flag.ContinueOnError)
			g := addGroupFlags(fs)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			pol, roles, err := loadPolicy(fs, g, env(map[string]string{policyEnv: tc.file}))
			if err != nil {
				t.Fatal(err)
			}
			if len(roles) != tc.want {
				t.Fatalf("group roles %v, want %d", roles, tc.want)
			}
			if _, err := pol.WithGroupRoles(roles); (err != nil) != tc.wantErr {
				t.Fatalf("err %v", err)
			}
		})
	}
}

func TestMockAndDevRefusals(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	noAdmin, err := auth.ParsePolicy("p", "role viewer { allow read on * }")
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]authConfig{
		"mock on every interface":          {mode: "mock", addr: "0.0.0.0:8080", publicURL: "http://127.0.0.1:8080", mockAddr: "127.0.0.1:0"},
		"mock provider on every interface": {mode: "mock", addr: "127.0.0.1:8080", publicURL: "http://127.0.0.1:8080", mockAddr: "0.0.0.0:0"},
		"mock with a public url":           {mode: "mock", addr: "127.0.0.1:8080", publicURL: "https://collegium.example.com", mockAddr: "127.0.0.1:0"},
		"dev without an admin role":        {mode: "dev", addr: "127.0.0.1:8080", policy: noAdmin},
		"unknown mode":                     {mode: "open"},
	} {
		t.Run(name, func(t *testing.T) {
			if c.policy == nil {
				c.policy = auth.DefaultPolicy()
			}
			c.tenant, c.groups, c.getenv, c.log = "acme", "groups", env(nil), log
			_, stop, err := newAuthn(ctx, c)
			stop()
			if err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestMockPersonasFile(t *testing.T) {
	path := writeFile(t, "people.json", `{"people": [{"subject": "s1", "name": "Sam", "groups": ["collegium-admins"]}]}`)
	people, machines, err := readPersonas(path)
	if err != nil || len(people) != 1 || people[0].Subject != "s1" || len(machines) != 0 {
		t.Fatalf("got %v %v %v", people, machines, err)
	}
	for name, src := range map[string]string{
		"unknown field": `{"people": [{"subject": "s1", "password": "x"}]}`,
		"two values":    `{} {}`,
		"not json":      `people:`,
	} {
		if _, _, err := readPersonas(writeFile(t, "p.json", src)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAuthzCommand(t *testing.T) {
	path := writeFile(t, "p.authz", `
role viewer  { allow read on * }
role auditor extends viewer { deny read on billing }
grant viewer  to anyone
grant auditor to claim department = "finance"
`)
	var out bytes.Buffer
	if err := runAuthz([]string{"check", path}, &out, env(nil)); err != nil || !strings.Contains(out.String(), "ok, roles viewer, auditor, 2 grants") {
		t.Fatalf("check: %v %s", err, out.String())
	}
	if err := runAuthz([]string{"check", writeFile(t, "bad.authz", "role a {")}, &out, env(nil)); err == nil {
		t.Fatal("check accepted a bad policy")
	}

	claims := writeFile(t, "claims.json", `{"sub": "00u1", "department": "finance"}`)
	out.Reset()
	err := runAuthz([]string{"explain", "-claims", claims, "billing:read"}, &out, env(map[string]string{policyEnv: path}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"billing:read: denied", "deny read on billing", `grant auditor to claim department = "finance"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("explain is missing %q:\n%s", want, out.String())
		}
	}
	if err := runAuthz([]string{"explain", "rockets:launch"}, &out, env(nil)); err == nil {
		t.Fatal("explain accepted an unknown permission")
	}

	// With no policy file it explains the built-in policy and the group flags.
	admin := writeFile(t, "admin.json", `{"sub": "00u2", "groups": ["collegium-admins"]}`)
	out.Reset()
	if err := runAuthz([]string{"explain", "-claims", admin, "agents:write"}, &out, env(nil)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "agents:write: allowed") || !strings.Contains(out.String(), `grant admin to group "collegium-admins"`) {
		t.Fatalf("explain:\n%s", out.String())
	}
}
