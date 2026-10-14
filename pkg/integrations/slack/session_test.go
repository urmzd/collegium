package slack_test

import (
	"context"
	"errors"
	"testing"

	"github.com/urmzd/collegium/pkg/integrations/slack"
	"github.com/urmzd/mandatum/pkg/address"
	"github.com/urmzd/mandatum/pkg/envelope"
)

var commands = []slack.Command{
	{Name: "create-pr", Description: "Open a pull request", ArgumentHint: "[title]"},
	{Name: "run-tests", Description: "Run the test suite"},
}

func TestReplayedLifecycleDoesNotRegressTheSessionStatus(t *testing.T) {
	api := slack.NewFake()
	c := sessionConnector(t, api, slack.Config{Sessions: true})
	deliver(t, c, target, event(1, envelope.KindAccepted, ""), event(3, envelope.KindCompleted, ""), event(2, envelope.KindStarted, ""), event(3, envelope.KindCompleted, ""))
	statuses := api.Statuses()
	if len(statuses) != 2 || statuses[1].Status != slack.StatusActive {
		t.Fatalf("statuses: %+v", statuses)
	}
}

func sessionConnector(t *testing.T, api slack.API, cfg slack.Config) *slack.Connector {
	t.Helper()
	cfg.API, cfg.Team, cfg.Interval = api, "T01ABCDEF", -1
	c, err := slack.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func deliver(t *testing.T, c *slack.Connector, to address.Address, events ...envelope.Event) {
	t.Helper()
	for _, e := range events {
		if err := c.Deliver(context.Background(), to, e); err != nil {
			t.Fatalf("Deliver(seq %d, %s): %v", e.Seq, e.Kind, err)
		}
	}
}

func TestARunsLifecycleIsReportedAsItsSessionStatus(t *testing.T) {
	fake := slack.NewFake()
	c := sessionConnector(t, fake, slack.Config{Sessions: true})

	deliver(t, c, target,
		event(1, envelope.KindAccepted, ""),
		event(2, envelope.KindStarted, ""),
		textEvent(3, "Which branch?"),
		event(4, envelope.KindParked, ""),
		event(5, envelope.KindResumed, ""),
		textEvent(6, " Done."),
		event(7, envelope.KindCompleted, ""),
	)

	want := []slack.Status{
		slack.StatusProcessing, slack.StatusProcessing,
		slack.StatusSuspended, slack.StatusProcessing, slack.StatusActive,
	}
	got := fake.Statuses()
	if len(got) != len(want) {
		t.Fatalf("slack saw %d status reports, want %d: %+v", len(got), len(want), got)
	}
	for i, s := range got {
		if s.Status != want[i] {
			t.Errorf("report %d = %q, want %q", i, s.Status, want[i])
		}
		if s.Channel != "C02CHANNEL" || s.ThreadTS != "1700000000.000100" {
			t.Errorf("report %d addressed %s/%s, want the run's own thread", i, s.Channel, s.ThreadTS)
		}
	}
}

func TestAFailedRunLeavesItsSessionReadyForTheNextPrompt(t *testing.T) {
	fake := slack.NewFake()
	c := sessionConnector(t, fake, slack.Config{Sessions: true})

	deliver(t, c, target,
		event(1, envelope.KindAccepted, ""),
		event(2, envelope.KindFailed, `{"error":"model overloaded"}`),
	)

	got := fake.Statuses()
	if last := got[len(got)-1].Status; last != slack.StatusActive {
		t.Errorf("status after a failure = %q, want %q so the indicator stops", last, slack.StatusActive)
	}
}

func TestSessionsAreNotReportedUnlessAskedFor(t *testing.T) {
	fake := slack.NewFake()
	c := sessionConnector(t, fake, slack.Config{})

	deliver(t, c, target, event(1, envelope.KindAccepted, ""), event(2, envelope.KindCompleted, ""))

	if got := fake.Statuses(); len(got) != 0 {
		t.Errorf("an app that is not an agent made %d session calls: %+v", len(got), got)
	}
}

func TestACodeChannelsSessionIsTheChannelNotAThread(t *testing.T) {
	fake := slack.NewFake()
	fake.CodeChannel("C02CHANNEL")
	c := sessionConnector(t, fake, slack.Config{Sessions: true})

	// A mention inside a code channel is threaded by the trigger like any
	// other, and Slack refuses a thread there.
	deliver(t, c, target, event(1, envelope.KindAccepted, ""))

	got := fake.Statuses()
	if len(got) != 1 || got[0].ThreadTS != "" || got[0].Status != slack.StatusProcessing {
		t.Errorf("statuses = %+v, want one channel-level processing report", got)
	}
}

func TestASlackRefusalOfStatusDoesNotFailADeliveredMessage(t *testing.T) {
	fake := slack.NewFake()
	c := sessionConnector(t, fake, slack.Config{Sessions: true})
	// A top-level target in an ordinary channel has no session to report on.
	channel := address.MustParse("slack://T01ABCDEF/C02CHANNEL")

	deliver(t, c, channel, event(1, envelope.KindAccepted, ""), textEvent(2, "hello"), event(3, envelope.KindCompleted, ""))

	if n := len(fake.Messages()); n != 1 {
		t.Errorf("slack holds %d messages, want the answer delivered regardless", n)
	}
}

func TestATransportFailureOfStatusIsRetried(t *testing.T) {
	fake := slack.NewFake()
	c := sessionConnector(t, fake, slack.Config{Sessions: true})
	ctx := context.Background()
	if err := c.Deliver(ctx, target, textEvent(1, "hello")); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	fake.Err = errors.New("connection reset")
	if err := c.Deliver(ctx, target, event(2, envelope.KindStarted, "")); err == nil {
		t.Fatal("Deliver succeeded with Slack unreachable; the router would never retry")
	}
}

type statusErrorAPI struct {
	*slack.Fake
	code string
}

func (a *statusErrorAPI) SetStatus(context.Context, slack.Session) error {
	return &slack.Error{Method: "agents.sessions.setStatus", Code: a.code}
}

func TestTransientSlackStatusErrorsRemainRetryableAfterMessageDelivery(t *testing.T) {
	for _, code := range []string{"ratelimited", "internal_error", "fatal_error", "service_unavailable", "missing_scope"} {
		t.Run(code, func(t *testing.T) {
			api := &statusErrorAPI{Fake: slack.NewFake(), code: code}
			c := sessionConnector(t, api, slack.Config{Sessions: true})
			err := c.Deliver(context.Background(), target, event(1, envelope.KindStarted, ""))
			if (err != nil) != (code != "missing_scope") {
				t.Fatalf("status error: %v", err)
			}
			if len(api.Messages()) != 1 {
				t.Fatal("message was not delivered")
			}
		})
	}
}

// countingAPI counts command registrations on their way to the fake.
type countingAPI struct {
	*slack.Fake
	registrations int
}

func (a *countingAPI) SetCommands(ctx context.Context, workspace, channel string, cmds []slack.Command) error {
	a.registrations++
	return a.Fake.SetCommands(ctx, workspace, channel, cmds)
}

func TestCommandsAreOfferedOnceInACodeChannel(t *testing.T) {
	api := &countingAPI{Fake: slack.NewFake()}
	api.CodeChannel("C02CHANNEL")
	c := sessionConnector(t, api, slack.Config{Commands: commands})

	for _, run := range []string{"run_1", "run_2", "run_3"} {
		e := event(1, envelope.KindAccepted, "")
		e.RunID = run
		deliver(t, c, target, e)
	}

	got := api.Commands("C02CHANNEL")
	if len(got) != 2 || got[0].Name != "create-pr" || got[1].Name != "run-tests" {
		t.Errorf("commands = %+v, want the configured set", got)
	}
	if api.registrations != 1 {
		t.Errorf("registered %d times across three runs, want once per channel", api.registrations)
	}
}

func TestAnOrdinaryChannelIsNotOfferedCommands(t *testing.T) {
	fake := slack.NewFake()
	c := sessionConnector(t, fake, slack.Config{Commands: commands})

	deliver(t, c, target, event(1, envelope.KindAccepted, ""), textEvent(2, "hi"), event(3, envelope.KindCompleted, ""))

	if got := fake.Commands("C02CHANNEL"); len(got) != 0 {
		t.Errorf("an ordinary channel holds commands: %+v", got)
	}
	if n := len(fake.Messages()); n != 1 {
		t.Errorf("slack holds %d messages, want the answer delivered regardless", n)
	}
}

func TestNewRejectsCommandsSlackWouldRefuse(t *testing.T) {
	many := make([]slack.Command, slack.MaxCommands+1)
	for i := range many {
		many[i] = slack.Command{Name: "c" + string(rune('a'+i)), Description: "d"}
	}
	cases := map[string][]slack.Command{
		"leading slash":  {{Name: "/create-pr", Description: "d"}},
		"empty name":     {{Description: "d"}},
		"name too long":  {{Name: "abcdefghijklmnopqrstuvwxyz-abcdef", Description: "d"}},
		"whitespace":     {{Name: "create pr", Description: "d"}},
		"no description": {{Name: "create-pr"}},
		"duplicate":      {{Name: "a", Description: "d"}, {Name: "a", Description: "d"}},
		"too many":       many,
	}
	for name, cmds := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := slack.New(slack.Config{API: slack.NewFake(), Commands: cmds}); err == nil {
				t.Errorf("New accepted %+v", cmds)
			}
		})
	}
}
