package github

import (
	"context"
	"fmt"
	transport "github.com/urmzd/mandatum/pkg/connectors/github"
	"github.com/urmzd/mandatum/pkg/envelope"
	presentation "github.com/urmzd/vultus/go/github"
	"sort"
	"strings"
)

func (c *Connector) guideLogin(agent string) string {
	var names []string
	for login, name := range c.cfg.Bots {
		if name == agent {
			names = append(names, strings.TrimSuffix(login, "[bot]"))
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func (c *Connector) prepareGuide(ctx context.Context, tag *envelope.Tag) error {
	c.guideMu.Lock()
	defer c.guideMu.Unlock()
	login := c.guideLogin(tag.Agent)
	if login == "" {
		return fmt.Errorf("github: no guide bot login for %s", tag.Agent)
	}
	ref, err := c.Reference(tag.Source)
	if err != nil {
		return err
	}
	evidence, err := c.cfg.API.(ContextAPI).ReadContext(ctx, ref, 1)
	if err != nil {
		return err
	}
	head := evidence.Subject.Head.SHA
	if !transport.ValidSHA(head) {
		return fmt.Errorf("github: guide needs a PR head SHA")
	}
	if pinned := tag.Source.Params["head_sha"]; pinned != "" && pinned != head {
		return fmt.Errorf("github: stale PR guide trigger")
	}
	tag.Source = tag.Source.WithParam("head_sha", head)
	tag.Meta["github_head_sha"] = head
	return c.cfg.API.(GuideAPI).UpdateGuide(ctx, ref, Guide{Agent: tag.Agent, BotLogin: login, HeadSHA: head, Body: presentation.GuideBody(evidence, tag.Agent, login, c.cfg.Mutations)})
}
