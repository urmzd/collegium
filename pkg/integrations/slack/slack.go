// Package slack composes mandatum's Slack transport with collegium's session
// lifecycle and command-discovery policy. Wire APIs and signed decoding stay upstream.
package slack

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/urmzd/mandatum/pkg/address"
	"github.com/urmzd/mandatum/pkg/connectors/mention"
	transport "github.com/urmzd/mandatum/pkg/connectors/slack"
	"github.com/urmzd/mandatum/pkg/envelope"
)

type Config struct {
	API          transport.API
	Verifier     transport.Verifier
	Team         string
	Bots         map[string]string
	Agents       mention.Set
	DefaultAgent string
	Deliver      []envelope.Route
	Interval     time.Duration
	Clock        func() time.Time
	MaxBody      int64
	Sessions     bool
	Commands     []transport.Command
	Log          *slog.Logger
}
type Connector struct {
	*transport.Connector
	cfg       Config
	offered   offered
	statusMu  sync.Mutex
	statusSeq map[string]uint64
	live      live
}

func New(cfg Config) (*Connector, error) {
	if err := validCommands(cfg.Commands); err != nil {
		return nil, err
	}
	if cfg.Sessions || len(cfg.Commands) > 0 {
		if _, ok := cfg.API.(transport.AgentAPI); !ok {
			return nil, fmt.Errorf("slack: session policy needs AgentAPI")
		}
	}
	cfg.Commands = append([]transport.Command(nil), cfg.Commands...)
	c := &Connector{cfg: cfg, offered: offered{seen: map[string]bool{}}, statusSeq: map[string]uint64{}}
	c.live.open, c.live.asked = map[string]session{}, map[string]bool{}
	base := transport.Config{API: cfg.API, Verifier: cfg.Verifier, Team: cfg.Team, Bots: cfg.Bots, Agents: cfg.Agents, DefaultAgent: cfg.DefaultAgent, Deliver: cfg.Deliver, Interval: cfg.Interval, Clock: cfg.Clock, MaxBody: cfg.MaxBody}
	base.Prepare = func(_ context.Context, tag *envelope.Tag) error {
		if tag.Meta["slack_event"] != "slash_command" {
			return nil
		}
		for _, cmd := range cfg.Commands {
			if cmd.Name == strings.TrimPrefix(tag.Meta["slack_command"], "/") {
				return nil
			}
		}
		return fmt.Errorf("slack: command is not configured")
	}
	base.Stopped = c.stopped
	var err error
	c.Connector, err = transport.New(base)
	return c, err
}
func (c *Connector) Deliver(ctx context.Context, to address.Address, e envelope.Event) error {
	// Everything a run publishes is rendered, a stopped run's last event
	// included: the sink shows what the agent had said, marked stopped.
	stopping := false
	if to.Connector == transport.Name && len(to.Path) == 1 {
		stopping = c.live.track(to, e)
	}
	if err := c.Connector.Deliver(ctx, to, e); err != nil {
		return err
	}
	status, ok := statusFor(e.Kind)
	if !ok {
		return nil
	}
	// A run someone stopped from Slack has had its session set to active.
	// Until it ends, a lifecycle event it was still publishing does not set
	// the session back to working.
	if stopping && status != StatusActive {
		return nil
	}
	// Keep replayed lifecycle events from regressing a completed session.
	// A failed API call does not advance the cursor, so a retry still runs.
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	key := e.RunID + "\x00" + to.String()
	if seq, ok := c.statusSeq[key]; ok && e.Seq <= seq {
		return nil
	}
	if err := c.session(ctx, to, e); err != nil {
		return err
	}
	if len(c.statusSeq) >= maxChannels {
		clear(c.statusSeq)
	}
	c.statusSeq[key] = e.Seq
	return nil
}

type API = transport.API
type Command = transport.Command
type Status = transport.Status
type Session = transport.Session
type Fake = transport.Fake
type FakeMessage = transport.FakeMessage
type Error = transport.Error

const (
	StatusProcessing = transport.StatusProcessing
	StatusSuspended  = transport.StatusSuspended
	StatusActive     = transport.StatusActive
)

var NewFake = transport.NewFake
var NewHTTPAPI = transport.NewHTTPAPI
