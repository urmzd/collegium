package clients

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

func slackOf(id, app, team string) Client {
	return Client{ID: id, Name: id, Surface: Slack, Identity: Identity{AppID: app, Team: team}, DefaultAgent: "lead", Enabled: true}
}

func githubOf(id string, owners ...string) Client {
	return Client{ID: id, Name: id, Surface: GitHub, Identity: Identity{Owners: owners}, DefaultAgent: "lead", Enabled: true}
}

// The zero allow-list grants the default agent and nothing else, and no
// default means no agent at all. Any agent has to be asked for by name.
func TestAClientRoutesToNothingItWasNotGiven(t *testing.T) {
	c := Client{}
	for _, a := range []string{"", "lead", "*"} {
		if c.Routes(a) {
			t.Fatalf("the zero client routes to %q", a)
		}
	}
	c.DefaultAgent = "lead"
	if !c.Routes("lead") || c.Routes("ops") || c.Routes("") {
		t.Fatal("a default agent grants itself alone")
	}
	c.Agents = []string{"ops"}
	if !c.Routes("ops") || c.Routes("billing") {
		t.Fatal("an allow-list grants what it lists alone")
	}
	c.Agents = []string{AnyAgent}
	if !c.Routes("billing") {
		t.Fatal(`"*" grants any agent`)
	}
}

func TestValidateRefusesWhatASurfaceCannotBe(t *testing.T) {
	ok := slackOf("docs", "A0DOCS", "T01ABCDEF")
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Client){
		"no team":                  func(c *Client) { c.Identity.Team = "" },
		"owners on slack":          func(c *Client) { c.Identity.Owners = []string{"acme"} },
		"github options on slack":  func(c *Client) { c.GitHub = &GitHubOptions{Checks: true} },
		"bad id":                   func(c *Client) { c.ID = "Docs Bot" },
		"no name":                  func(c *Client) { c.Name = " " },
		"unknown surface":          func(c *Client) { c.Surface = "jira" },
		"star with others":         func(c *Client) { c.Agents = []string{"*", "ops"} },
		"duplicate agent":          func(c *Client) { c.Agents = []string{"ops", "ops"} },
		"bot for a foreign agent":  func(c *Client) { c.Bots = map[string]string{"U08BOT": "billing"} },
		"too many commands":        func(c *Client) { c.Slack = &SlackOptions{Commands: make([]Command, 11)} },
		"command with a slash":     func(c *Client) { c.Slack = &SlackOptions{Commands: []Command{{Name: "/pr", Description: "x"}}} },
		"command with no purpose":  func(c *Client) { c.Slack = &SlackOptions{Commands: []Command{{Name: "pr"}}} },
		"agent name with a space":  func(c *Client) { c.DefaultAgent = "the lead" },
		"too many agents":          func(c *Client) { c.Agents = make([]string, 65) },
		"app id that is not an id": func(c *Client) { c.Identity.AppID = "a b" },
	} {
		c := ok.clone()
		change(&c)
		if err := c.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	gh := githubOf("gh", "acme")
	gh.GitHub = &GitHubOptions{AutoLabels: map[string]map[string]string{"acme/handbook": {"agent:auto": "lead"}}}
	if err := gh.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Client){
		"no owners":                     func(c *Client) { c.Identity.Owners = nil },
		"team on github":                func(c *Client) { c.Identity.Team = "T01ABCDEF" },
		"installation not a number":     func(c *Client) { c.Identity.Installation = "abc" },
		"label for a foreign agent":     func(c *Client) { c.GitHub.AutoLabels["acme/handbook"]["x"] = "billing" },
		"label outside the accounts":    func(c *Client) { c.GitHub.AutoLabels["globex/site"] = map[string]string{"x": "lead"} },
		"guides with no bot":            func(c *Client) { c.GitHub.Guides = true },
		"read repository not a repo":    func(c *Client) { c.GitHub.ReadRepositories = []string{"acme"} },
		"the same account twice":        func(c *Client) { c.Identity.Owners = []string{"acme", "ACME"} },
		"slack options on github":       func(c *Client) { c.Slack = &SlackOptions{} },
		"more accounts than are kept":   func(c *Client) { c.Identity.Owners = strings.Split(strings.Repeat("a,", 33), ",")[:33] },
		"an account that is not a name": func(c *Client) { c.Identity.Owners = []string{"acme corp"} },
	} {
		c := gh.clone()
		change(&c)
		if err := c.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A request is offered to a client only when nothing it states disagrees with
// the client, and a field the request leaves out disagrees with nothing.
func TestClaimsMatchOnlyWhatARequestStates(t *testing.T) {
	s := slackOf("docs", "A0DOCS", "T01ABCDEF")
	for claim, want := range map[Claim]bool{
		{AppID: "A0DOCS", Team: "T01ABCDEF"}: true,
		{Team: "T01ABCDEF"}:                  true,
		{}:                                   true, // url_verification states nothing
		{AppID: "A0OPS", Team: "T01ABCDEF"}:  false,
		{AppID: "A0DOCS", Team: "T02OTHER"}:  false,
	} {
		if got := s.Claims(claim); got != want {
			t.Errorf("slack %+v: %v, want %v", claim, got, want)
		}
	}
	g := githubOf("gh", "acme")
	g.Identity.Installation = "101"
	for claim, want := range map[Claim]bool{
		{Installation: "101", Owner: "Acme"}: true,
		{Owner: "acme"}:                      true,
		{}:                                   true, // ping
		{Installation: "202", Owner: "acme"}: false,
		{Installation: "101", Owner: "labs"}: false,
	} {
		if got := g.Claims(claim); got != want {
			t.Errorf("github %+v: %v, want %v", claim, got, want)
		}
	}
}

func TestAStoreKeepsEachTenantsClientsToItself(t *testing.T) {
	m := NewMemory()
	if _, err := m.Create("acme", slackOf("docs", "A0DOCS", "T01ABCDEF"), DefaultCaps(), t0); err != nil {
		t.Fatal(err)
	}
	// The same id and the same Slack identity in another tenant is its own client.
	if _, err := m.Create("globex", slackOf("docs", "A0DOCS", "T01ABCDEF"), DefaultCaps(), t0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get("initech", "docs"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant read a client: %v", err)
	}
	if err := m.Delete("initech", "docs"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant deleted a client: %v", err)
	}
	if _, err := m.Update("initech", slackOf("docs", "A0DOCS", "T01ABCDEF"), t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant updated a client: %v", err)
	}
	if got := m.List("initech"); len(got) != 0 {
		t.Fatalf("another tenant lists %d clients", len(got))
	}
	if got := m.List("acme"); len(got) != 1 || got[0].Tenant != "acme" {
		t.Fatalf("acme lists %+v", got)
	}
	// What a read returns is a copy: changing it changes nothing kept.
	got, _ := m.Get("acme", "docs")
	got.Agents = append(got.Agents, "*")
	if again, _ := m.Get("acme", "docs"); len(again.Agents) != 0 {
		t.Fatal("a caller changed a stored client through what it was handed")
	}
}

func TestAStoreRefusesTwoClientsARequestCouldNotTellApart(t *testing.T) {
	m := NewMemory()
	must := func(c Client) {
		t.Helper()
		if _, err := m.Create("acme", c, Caps{Max: 50, MaxPerSurface: 50}, t0); err != nil {
			t.Fatal(err)
		}
	}
	must(slackOf("docs", "A0DOCS", "T01ABCDEF"))
	must(slackOf("ops", "A0OPS", "T01ABCDEF"))     // another app, same team
	must(slackOf("docs-eu", "A0DOCS", "T02OTHER")) // same app, another team
	must(githubOf("gh", "acme"))
	must(githubOf("gh-labs", "acme-labs"))
	for name, c := range map[string]Client{
		"the same app and team":           slackOf("again", "A0DOCS", "T01ABCDEF"),
		"any app in a team that has apps": slackOf("any", "", "T01ABCDEF"),
		"an account already served":       githubOf("gh2", "ACME"),
		"an id already used on a surface": slackOf("gh", "A0NEW", "T03THIRD"),
		"the same id":                     slackOf("docs", "A0NEW", "T03THIRD"),
	} {
		if _, err := m.Create("acme", c, Caps{Max: 50, MaxPerSurface: 50}, t0); !errors.Is(err, ErrExists) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// An update may not move a client onto another's identity either.
	moved := slackOf("ops", "A0DOCS", "T01ABCDEF")
	if _, err := m.Update("acme", moved, t0); !errors.Is(err, ErrExists) {
		t.Fatalf("an update took another client's identity: %v", err)
	}
	changed := githubOf("ops", "acme-ops")
	if _, err := m.Update("acme", changed, t0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an update moved a client to another surface: %v", err)
	}
}

// The count and the insert are one step: racing creates never pass the cap.
func TestTheCapHoldsUnderRacingCreates(t *testing.T) {
	m := NewMemory()
	caps := Caps{Max: 7, MaxPerSurface: 5}
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() {
			c := slackOf(fmt.Sprintf("s%d", i), fmt.Sprintf("A0APP%d", i), "T01ABCDEF")
			if i%2 == 1 {
				c = githubOf(fmt.Sprintf("g%d", i), fmt.Sprintf("org%d", i))
			}
			if _, err := m.Create("acme", c, caps, t0); err != nil && !errors.Is(err, ErrLimit) {
				t.Errorf("create: %v", err)
			}
		})
	}
	wg.Wait()
	on := map[Surface]int{}
	for _, c := range m.List("acme") {
		on[c.Surface]++
	}
	if on[Slack]+on[GitHub] != 7 || on[Slack] > 5 || on[GitHub] > 5 {
		t.Fatalf("clients by surface = %v under a cap of 7 and 5 a surface", on)
	}
	// The zero caps allow no client.
	if _, err := NewMemory().Create("acme", slackOf("a", "A0A", "T01ABCDEF"), Caps{}, t0); !errors.Is(err, ErrLimit) {
		t.Fatalf("zero caps allowed a client: %v", err)
	}
}

func TestOnListsOnlyEnabledClientsOldestFirst(t *testing.T) {
	m := NewMemory()
	for i, tenant := range []string{"acme", "globex", "initech"} {
		c := slackOf("docs", "A0DOCS", "T01ABCDEF")
		c.Enabled = tenant != "globex"
		if _, err := m.Create(tenant, c, DefaultCaps(), t0.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	got := m.On(Slack)
	if len(got) != 2 || got[0].Tenant != "acme" || got[1].Tenant != "initech" {
		t.Fatalf("on slack = %+v", got)
	}
	if len(m.On(GitHub)) != 0 {
		t.Fatal("a slack client is on github")
	}
}

// Nothing a record marshals to can carry a secret: the stored type has no
// field for one, and the input type hides its value from formatting.
func TestSecretsHaveNowhereToLeak(t *testing.T) {
	c := slackOf("docs", "A0DOCS", "T01ABCDEF")
	c.Tenant = "acme"
	c.Credentials = Credentials{Token: SecretRef{Set: true}, SigningSecret: SecretRef{Set: true, Ref: "env:COLLEGIUM_SLACK_SIGNING_SECRET"}}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, has := fields["tenant"]; has {
		t.Fatal("a client says which tenant it is in")
	}
	creds := fields["credentials"].(map[string]any)
	for name, v := range creds {
		for key := range v.(map[string]any) {
			if key != "set" && key != "ref" {
				t.Fatalf("credentials.%s has a field %q", name, key)
			}
		}
	}
	in := SecretInput{Value: "xoxb-very-secret"}
	for _, formatted := range []string{fmt.Sprint(in), fmt.Sprintf("%v %+v %#v %s", in, in, in, in)} {
		if strings.Contains(formatted, "very-secret") {
			t.Fatalf("a secret was formatted: %s", formatted)
		}
	}
}

// A reference may name only a variable set aside for collegium, so a client
// cannot be pointed at the server's other secrets.
func TestAnEnvReferenceNamesOnlyCollegiumVariables(t *testing.T) {
	if name, err := EnvRef("env:COLLEGIUM_SLACK_TOKEN_DOCS"); err != nil || name != "COLLEGIUM_SLACK_TOKEN_DOCS" {
		t.Fatalf("%q, %v", name, err)
	}
	for _, ref := range []string{"env:ANTHROPIC_API_KEY", "env:PATH", "env:", "COLLEGIUM_SLACK_TOKEN", "file:/etc/passwd", "env:collegium_x", "env:COLLEGIUM_A B"} {
		if _, err := EnvRef(ref); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q was accepted: %v", ref, err)
		}
	}
}

func TestSecretsAreKeptPerTenantAndClient(t *testing.T) {
	s := NewMemorySecrets()
	s.Put("acme", "docs", SecretToken, "a")
	s.Put("globex", "docs", SecretToken, "b")
	if v, _ := s.Get("acme", "docs", SecretToken); v != "a" {
		t.Fatalf("acme reads %q", v)
	}
	if _, ok := s.Get("acme", "ops", SecretToken); ok {
		t.Fatal("a client read another client's secret")
	}
	s.Drop("acme", "docs")
	if _, ok := s.Get("acme", "docs", SecretToken); ok {
		t.Fatal("a dropped secret is still held")
	}
	if v, _ := s.Get("globex", "docs", SecretToken); v != "b" {
		t.Fatal("dropping one tenant's secrets dropped another's")
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"Docs Bot": "docs-bot", "  GitHub (acme) ": "github-acme", "---": "", "A": "a"} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
