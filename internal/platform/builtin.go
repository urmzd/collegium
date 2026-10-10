package platform

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/urmzd/opentag/pkg/agentspec"
	"github.com/urmzd/opentag/pkg/registry"
)

// Agents the platform provides.
//
// A built-in is an ordinary agent revision that the platform writes instead of
// a person. It runs like any other agent, in a sandbox, under the same limits,
// and another agent reaches it the way it reaches any agent: by naming it in
// its spawn allowlist. What sets it apart is only that nobody edits it, so
// every workspace has the same one.
//
// A built-in needs a model to be of any use, so none exists until the server
// is told which provider and model to give them.

// BuiltinWriter writes a document and publishes it.
const BuiltinWriter = "collegium-writer"

const writerPrompt = `You write documents and publish them as files.

When asked for a document, report, summary, table or dataset:
1. Write it with file_write. Use Markdown (.md) for prose and CSV (.csv) for tables of data. Choose a short, descriptive file name.
2. Publish it with publish_artifact, with a one-line description.
3. Reply in one or two sentences saying what you published. Do not paste the document into your reply.

To change a file you published before, read it with file_read, write the new version to the same path, and publish it again under the same title: that makes a new version.

Write only what you were asked for. If the request does not give you enough to write something true, say what is missing instead of inventing it.`

// Builtins lists the names of the agents the platform provides.
func Builtins() []string { return []string{BuiltinWriter} }

// IsBuiltin reports whether a name belongs to the platform.
func IsBuiltin(name string) bool { return slices.Contains(Builtins(), name) }

func (p *Platform) builtinSpecs() []agentspec.AgentSpec {
	if p.cfg.BuiltinProvider == "" || p.cfg.BuiltinModel == "" {
		return nil
	}
	return []agentspec.AgentSpec{{
		Name:         BuiltinWriter,
		Description:  "Writes documents, reports and tables, and publishes them as files",
		Provider:     p.cfg.BuiltinProvider,
		Model:        p.cfg.BuiltinModel,
		SystemPrompt: writerPrompt,
		Tools:        FileTools(),
	}}
}

// EnsureBuiltins makes sure a tenant has the platform's agents, at the
// definition this server was started with. It is safe to call on every
// request: when nothing has changed it only reads.
func (p *Platform) EnsureBuiltins(ctx context.Context, tenant string) error {
	for _, want := range p.builtinSpecs() {
		have, err := p.Agents.Get(ctx, tenant, want.Name, 0)
		switch {
		case errors.Is(err, registry.ErrNotFound):
			if _, err := p.Agents.Create(ctx, tenant, want, "platform"); err != nil && !errors.Is(err, registry.ErrExists) {
				return fmt.Errorf("platform: built-in %s: %w", want.Name, err)
			}
		case err != nil:
			return err
		case have.Spec.Provider != want.Provider || have.Spec.Model != want.Model || have.Spec.SystemPrompt != want.SystemPrompt ||
			!slices.Equal(have.Spec.Tools, want.Tools):
			if _, err := p.Agents.Revise(ctx, tenant, want, have.Rev, "platform"); err != nil && !errors.Is(err, registry.ErrConflict) {
				return fmt.Errorf("platform: built-in %s: %w", want.Name, err)
			}
		}
	}
	return nil
}
