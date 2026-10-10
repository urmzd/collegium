package platform

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/urmzd/opentag/pkg/envelope"
	"github.com/urmzd/opentag/pkg/registry"

	"github.com/urmzd/open-agent-platform/pkg/pick"
	"github.com/urmzd/open-agent-platform/pkg/runs"
)

// Auto asks the platform to pick the agent.
const Auto = "auto"

// maxTranscript bounds how much of a conversation is replayed to the next
// agent, in bytes. Older turns are dropped first. This is truncation, not
// summarization: nothing is rewritten, and the dropped turns stay on record.
const maxTranscript = 12_000

// ErrNoBaseline reports a baseline that names no agent in the tenant.
var ErrNoBaseline = errors.New("platform: a setting names an agent that is not in this tenant")

// ErrSettings reports settings that cannot be stored.
var ErrSettings = errors.New("platform: invalid settings")

// History is how much of a conversation the next agent is sent.
type History string

const (
	// HistoryRecent sends the most recent turns that fit the limit. Older
	// turns are dropped, not summarized, and stay on record.
	HistoryRecent History = "recent"
	// HistoryNone sends only the new message.
	HistoryNone History = "none"
)

// Bounds on how much conversation is replayed, in characters.
const (
	MinHistory     = 1_000
	MaxHistory     = 200_000
	DefaultHistory = maxTranscript
)

// Project is a named place to file conversations.
type Project struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

var validProject = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]{0,62}$`)

// Settings is what a tenant has chosen for itself.
type Settings struct {
	// Baseline is the agent that answers when nobody picked one and no
	// agent's description matched. Empty means there is none.
	Baseline string `json:"baseline"`
	// History and HistoryLimit decide how much of a conversation is replayed
	// to the agent answering the next turn.
	History      History   `json:"history"`
	HistoryLimit int       `json:"history_limit"`
	Projects     []Project `json:"projects"`
	// TitleAgent names conversations after their first turn. Empty leaves a
	// conversation shown by its first message.
	TitleAgent string `json:"title_agent"`
	// MemoryAgent reads each finished turn and writes what the answering
	// agent should remember. Empty leaves memory to people.
	MemoryAgent string `json:"memory_agent"`
}

func (s Settings) withDefaults() Settings {
	if s.History == "" {
		s.History = HistoryRecent
	}
	if s.HistoryLimit == 0 {
		s.HistoryLimit = DefaultHistory
	}
	if s.Projects == nil {
		s.Projects = []Project{}
	}
	return s
}

type settings struct {
	mu sync.RWMutex
	by map[string]Settings
}

func (s *settings) get(tenant string) Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.by[tenant].withDefaults()
}

// Settings returns a tenant's settings.
func (p *Platform) Settings(tenant string) Settings { return p.settings.get(tenant) }

// SetSettings replaces a tenant's settings.
func (p *Platform) SetSettings(ctx context.Context, tenant string, next Settings) error {
	next = next.withDefaults()
	for _, name := range []string{next.Baseline, next.TitleAgent, next.MemoryAgent} {
		if name == "" {
			continue
		}
		if _, err := p.Agents.Get(ctx, tenant, name, 0); err != nil {
			return fmt.Errorf("%w: %q", ErrNoBaseline, name)
		}
	}
	switch {
	case next.History != HistoryRecent && next.History != HistoryNone:
		return fmt.Errorf("%w: history is recent or none", ErrSettings)
	case next.HistoryLimit < MinHistory || next.HistoryLimit > MaxHistory:
		return fmt.Errorf("%w: the history limit is from %d to %d characters", ErrSettings, MinHistory, MaxHistory)
	case len(next.Projects) > 100:
		return fmt.Errorf("%w: at most 100 projects", ErrSettings)
	}
	seen := map[string]bool{}
	for _, pr := range next.Projects {
		if !validProject.MatchString(pr.Name) || seen[strings.ToLower(pr.Name)] {
			return fmt.Errorf("%w: a project needs a unique name of letters, digits, spaces and dashes", ErrSettings)
		}
		seen[strings.ToLower(pr.Name)] = true
	}
	p.settings.mu.Lock()
	defer p.settings.mu.Unlock()
	if p.settings.by == nil {
		p.settings.by = map[string]Settings{}
	}
	p.settings.by[tenant] = next
	return nil
}

// Pick chooses an agent for a message without starting anything.
func (p *Platform) Pick(ctx context.Context, tenant, message string) (pick.Choice, error) {
	var candidates []pick.Candidate
	page := registry.Page{Size: 100}
	for {
		revs, next, err := p.Agents.List(ctx, tenant, page)
		if err != nil {
			return pick.Choice{}, err
		}
		for _, r := range revs {
			candidates = append(candidates, pick.Candidate{
				Name: r.Spec.Name, Description: r.Spec.Description, Prompt: r.Spec.SystemPrompt,
			})
		}
		if next == "" {
			break
		}
		page.Token = next
	}
	return pick.Agent(message, candidates, p.Settings(tenant).Baseline)
}

// Turn is one message a person sends.
type Turn struct {
	ID string
	// Agent is who should answer, or Auto.
	Agent string
	Text  string
	// After is the turn this one follows. Empty starts a conversation. Naming
	// a turn that already has a reply starts a branch from that point.
	After string
	// Project files a new conversation. A follow-up keeps its conversation's
	// project, whatever is passed here.
	Project string
	Actor   envelope.Actor
}

// Converse sends a message into a conversation and starts the run that answers.
//
// Any agent can answer any turn. The agent that answers is sent the branch so
// far, so a different agent picking up mid-conversation knows what was said.
func (p *Platform) Converse(ctx context.Context, tenant string, t Turn) (runs.Run, error) {
	cfg := p.Settings(tenant)
	meta := map[string]string{runs.MetaAsk: t.Text}
	prompt := t.Text
	if t.After != "" {
		path, err := p.Runs.Path(tenant, t.After)
		if err != nil {
			return runs.Run{}, err
		}
		meta[runs.MetaThread] = path[0].Thread
		meta[runs.MetaPrev] = t.After
		meta[runs.MetaProject] = path[0].Project
		if cfg.History == HistoryRecent {
			prompt = transcript(path, cfg.HistoryLimit) + "\n\nUser: " + t.Text
		}
	} else if t.Project != "" {
		if !slices.ContainsFunc(cfg.Projects, func(pr Project) bool { return pr.Name == t.Project }) {
			return runs.Run{}, fmt.Errorf("%w: no project called %q", ErrSettings, t.Project)
		}
		meta[runs.MetaProject] = t.Project
	}

	agent := t.Agent
	if agent == "" || agent == Auto {
		choice, err := p.Pick(ctx, tenant, t.Text)
		if err != nil {
			return runs.Run{}, err
		}
		agent = choice.Agent
		meta[runs.MetaPicked] = choice.Reason
	}

	// What this agent remembers goes first, ahead of the conversation.
	if notes := p.Memory.Prompt(tenant, agent); notes != "" {
		prompt = notes + "\n\n" + prompt
	}

	return p.Invoke(ctx, envelope.Tag{
		ID: t.ID, Tenant: tenant, Agent: agent, Origin: runs.OriginAPI,
		Text: prompt, Actor: t.Actor, Meta: meta,
	})
}

// transcript renders a branch for the next agent, newest turns kept.
func transcript(path []runs.Run, limit int) string {
	var turns []string
	size := 0
	for i := len(path) - 1; i >= 0; i-- {
		r := path[i]
		reply := r.Answer
		switch {
		case r.Status == runs.Failed:
			reply = "(this reply failed)"
		case !r.Status.Terminal():
			reply = "(still working)"
		}
		turn := fmt.Sprintf("User: %s\n%s: %s", r.Text, r.Agent, reply)
		if size+len(turn) > limit && len(turns) > 0 {
			break
		}
		size += len(turn)
		turns = append(turns, turn)
	}
	var b strings.Builder
	b.WriteString("Conversation so far:\n")
	if len(turns) < len(path) {
		fmt.Fprintf(&b, "(%d earlier turns omitted)\n", len(path)-len(turns))
	}
	for i := len(turns) - 1; i >= 0; i-- {
		b.WriteString("\n")
		b.WriteString(turns[i])
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
