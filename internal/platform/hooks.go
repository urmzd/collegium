package platform

import (
	"context"
	"fmt"
	"strings"

	"github.com/urmzd/opentag/pkg/envelope"

	"github.com/urmzd/open-agent-platform/pkg/memory"
	"github.com/urmzd/open-agent-platform/pkg/runs"
)

// Housekeeping the platform does around a conversation, by asking an agent.
//
// A workspace can name an agent to title conversations and an agent to keep
// each agent's memory. After a turn ends, the platform sends that agent a
// request of its own, as a run with origin "system", and files the answer when
// it comes back. They are ordinary runs: they show on the Runs page, they are
// traced, and they can fail without affecting the conversation they serve.
//
// Both are off until a workspace chooses an agent for them, and neither runs on
// the offline provider, whose answer is a notice and not a title or a fact.
const (
	metaHook = "oap.hook"
	metaFor  = "oap.for"

	hookTitle  = "title"
	hookMemory = "memory"
)

// maxTitle bounds a conversation's name.
const maxTitle = 80

const titlePrompt = "Write a title for a conversation that starts with the message below. " +
	"Reply with the title only: at most six words, no quotes, no full stop.\n\nMessage: %s"

const memoryPrompt = "Below is one exchange between a person and the agent %q. " +
	"List what that agent should remember for later conversations: lasting facts about the person, " +
	"their work or their preferences. One per line, at most three lines, each a full sentence. " +
	"Leave out anything that only mattered in this exchange. Reply NONE if there is nothing.\n\n" +
	"Person: %s\n%s: %s"

// ended is called exactly once for every run that ends.
func (p *Platform) ended(ctx context.Context, run runs.Run) {
	p.raise(run)
	switch {
	case run.Origin == runs.OriginSystem:
		p.fileHook(run)
	case run.Origin == runs.OriginAPI && run.Parent == "" && run.Status == runs.Completed:
		p.afterTurn(ctx, run)
	}
}

// afterTurn starts the housekeeping a finished turn calls for.
func (p *Platform) afterTurn(ctx context.Context, run runs.Run) {
	cfg := p.Settings(run.Tenant)
	if cfg.TitleAgent != "" && run.Prev == "" {
		p.startHook(ctx, run, cfg.TitleAgent, hookTitle, run.Thread, fmt.Sprintf(titlePrompt, clip(run.Text, 2000)))
	}
	if cfg.MemoryAgent != "" {
		p.startHook(ctx, run, cfg.MemoryAgent, hookMemory, run.Agent,
			fmt.Sprintf(memoryPrompt, run.Agent, clip(run.Text, 4000), run.Agent, clip(run.Answer, 4000)))
	}
}

func (p *Platform) startHook(ctx context.Context, turn runs.Run, agent, hook, target, prompt string) {
	rev, err := p.Agents.Get(ctx, turn.Tenant, agent, 0)
	if err != nil {
		p.log.Warn("platform: housekeeping agent is missing", "hook", hook, "agent", agent)
		return
	}
	if rev.Spec.Provider == "offline" && !p.cfg.OfflineHooks {
		return
	}
	_, err = p.Invoke(ctx, envelope.Tag{
		ID:     fmt.Sprintf("sys-%s-%s", hook, turn.ID),
		Tenant: turn.Tenant,
		Agent:  agent,
		Origin: runs.OriginSystem,
		Text:   prompt,
		Actor:  envelope.Actor{ID: "system:" + hook, Display: "platform " + hook, Bot: true},
		Meta:   map[string]string{metaHook: hook, metaFor: target},
	})
	if err != nil {
		p.log.Warn("platform: housekeeping run could not start", "hook", hook, "error", err)
	}
}

// fileHook puts a housekeeping run's answer where it belongs. The answer is a
// model's words about text a person supplied, so it is treated as untrusted:
// trimmed to one line, bounded, and stored only as a title or as notes people
// can read and remove.
func (p *Platform) fileHook(run runs.Run) {
	if run.Status != runs.Completed {
		return
	}
	meta := run.Tag().Meta
	target := meta[metaFor]
	switch meta[metaHook] {
	case hookTitle:
		if title := oneLine(run.Answer, maxTitle); title != "" {
			p.Runs.SetTitle(run.Tenant, target, title)
		}
	case hookMemory:
		kept := 0
		for _, line := range strings.Split(run.Answer, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.EqualFold(strings.Trim(line, ".-* "), "none") || kept == 3 {
				continue
			}
			if len(line) > memory.MaxChars {
				continue
			}
			// A proposal waits for a person unless the workspace chose
			// otherwise. Text that carries personal data is not kept at all.
			status := memory.Pending
			if p.Settings(run.Tenant).MemoryAutoApprove {
				status = memory.Approved
			}
			if _, err := p.Memory.Add(run.Tenant, target, line, memory.Consolidated, status, run.ID, run.UpdatedAt); err == nil {
				kept++
			} else {
				p.log.Info("platform: a proposed memory was not kept", "agent", target, "reason", err)
			}
		}
	}
}

// oneLine reduces text to its first line, without wrapping quotes, bounded.
func oneLine(text string, limit int) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	line = strings.Trim(strings.Join(strings.Fields(line), " "), "\"'`.")
	return clip(line, limit)
}

// clip shortens text to a limit in bytes, on a rune boundary.
func clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && s[limit]&0xC0 == 0x80 {
		limit--
	}
	return strings.TrimSpace(s[:limit])
}
