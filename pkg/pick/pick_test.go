package pick

import (
	"errors"
	"slices"
	"testing"
)

var team = []Candidate{
	{Name: "lead", Description: "Takes any request and coordinates the work."},
	{Name: "releaser", Description: "Cuts releases, tags versions and writes changelogs."},
	{Name: "oncall", Description: "Triages incidents and pages, and writes the incident summary."},
}

func TestPicksByDescription(t *testing.T) {
	c, err := Agent("Can you tag the next release?", team, "lead")
	if err != nil {
		t.Fatal(err)
	}
	if c.Agent != "releaser" || len(c.Matched) == 0 {
		t.Fatalf("got %+v", c)
	}
}

func TestPicksByName(t *testing.T) {
	c, _ := Agent("ask oncall what happened", team, "lead")
	if c.Agent != "oncall" {
		t.Fatalf("got %+v", c)
	}
}

func TestBaselineWhenNothingMatches(t *testing.T) {
	c, err := Agent("What is the weather like?", team, "lead")
	if err != nil || c.Agent != "lead" || len(c.Matched) != 0 {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestBaselineOnATie(t *testing.T) {
	c, err := Agent("writes", team, "lead")
	if err != nil || c.Agent != "lead" {
		t.Fatalf("a tie did not fall to the baseline: %+v, %v", c, err)
	}
}

func TestNoBaselineIsAnError(t *testing.T) {
	for _, baseline := range []string{"", "ghost"} {
		if _, err := Agent("What is the weather like?", team, baseline); !errors.Is(err, ErrNoAgent) {
			t.Errorf("baseline %q: got %v", baseline, err)
		}
	}
	if _, err := Agent("anything", nil, "lead"); !errors.Is(err, ErrNoAgent) {
		t.Errorf("no candidates: got %v", err)
	}
}

func TestIsDeterministic(t *testing.T) {
	first, _ := Agent("summarize the incident and page someone", team, "lead")
	for range 20 {
		again, _ := Agent("summarize the incident and page someone", team, "lead")
		if again.Agent != first.Agent || !slices.Equal(again.Matched, first.Matched) {
			t.Fatalf("%+v != %+v", again, first)
		}
	}
	if first.Agent != "oncall" {
		t.Fatalf("got %+v", first)
	}
}

// A word every agent mentions cannot decide between them.
func TestCommonWordsDoNotDecide(t *testing.T) {
	same := []Candidate{
		{Name: "a1", Description: "handles billing questions"},
		{Name: "b1", Description: "handles shipping questions"},
	}
	c, err := Agent("questions about shipping", same, "a1")
	if err != nil || c.Agent != "b1" {
		t.Fatalf("got %+v, %v", c, err)
	}
}

// An agent with no description is still found through its prompt.
func TestThePromptIsSearchedToo(t *testing.T) {
	bare := []Candidate{
		{Name: "a1", Prompt: "You reconcile invoices against purchase orders."},
		{Name: "b1", Prompt: "You draft replies to customer emails."},
	}
	c, err := Agent("reconcile this invoice", bare, "")
	if err != nil || c.Agent != "a1" {
		t.Fatalf("got %+v, %v", c, err)
	}
}

// A word in a description outweighs the same word in a prompt.
func TestADescriptionOutweighsAPrompt(t *testing.T) {
	two := []Candidate{
		{Name: "a1", Prompt: "Mention the refund policy when a customer is upset."},
		{Name: "b1", Description: "Handles every refund."},
	}
	c, err := Agent("I need a refund", two, "")
	if err != nil || c.Agent != "b1" {
		t.Fatalf("got %+v, %v", c, err)
	}
}
