// Package pick chooses which agent should answer a message when the person
// did not choose one.
//
// It is lexical and explainable on purpose: an agent is picked because words
// in the message appear in its name, its description or its prompt, and the
// pick says which words. Rare words count for more than common ones.
//
// A description is optional. It works the way a skill's description does: a
// line or two written to be found by, so its words count for more than the
// same words buried in a long prompt. An agent with no description is still
// found through its prompt. When nothing matches, or
// two agents match equally, the tenant's baseline agent answers, and the pick
// says that too. No model is asked, so the same message always goes to the same
// agent and anyone can see why.
package pick

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"
)

// ErrNoAgent reports that nothing could be picked: no agent matched and the
// tenant has no baseline.
var ErrNoAgent = errors.New("pick: no agent matched and no baseline agent is set")

// Candidate is an agent that could be picked.
type Candidate struct {
	Name        string
	Description string
	// Prompt is the agent's system prompt. Its words count, at a lower
	// weight than the name's and the description's.
	Prompt string
}

// Weights for where a word was found.
const (
	inHeadline = 3.0
	inPrompt   = 1.0
)

// Choice is the agent picked and why.
type Choice struct {
	Agent string `json:"agent"`
	// Reason is a sentence a person can read.
	Reason string `json:"reason"`
	// Matched is the words that decided it. Empty when the baseline was used.
	Matched []string `json:"matched,omitempty"`
}

// stop is words too common to say anything about which agent fits.
var stop = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true, "by": true,
	"can": true, "do": true, "for": true, "from": true, "how": true, "i": true, "in": true, "is": true,
	"it": true, "me": true, "my": true, "of": true, "on": true, "or": true, "our": true, "please": true,
	"that": true, "the": true, "this": true, "to": true, "we": true, "what": true, "with": true, "you": true,
	"agent": true,
}

// singular folds a plural onto its singular, so "releases" in a description
// matches "release" in a message. It is deliberately this small: a real stemmer
// would also fold words that mean different things.
func singular(w string) string {
	switch {
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		return w[:len(w)-1]
	}
	return w
}

func words(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(w) > 1 && !stop[w] {
			out = append(out, singular(w))
		}
	}
	return out
}

// Agent picks an agent for a message. baseline may be empty.
func Agent(message string, candidates []Candidate, baseline string) (Choice, error) {
	if len(candidates) == 0 {
		return Choice{}, ErrNoAgent
	}
	// How many agents mention each word. A word every agent mentions cannot
	// tell them apart, and is weighted to nothing.
	vocab := make([]map[string]float64, len(candidates))
	df := map[string]int{}
	for i, c := range candidates {
		vocab[i] = map[string]float64{}
		add := func(text string, weight float64) {
			for _, w := range words(text) {
				if _, seen := vocab[i][w]; !seen {
					df[w]++
				}
				vocab[i][w] = max(vocab[i][w], weight)
			}
		}
		add(c.Prompt, inPrompt)
		add(c.Name+" "+c.Description, inHeadline)
	}

	query := words(message)
	best, bestScore, tied := -1, 0.0, false
	matched := make([][]string, len(candidates))
	for i := range candidates {
		var score float64
		for _, w := range query {
			if weight := vocab[i][w]; weight > 0 && !slices.Contains(matched[i], w) {
				matched[i] = append(matched[i], w)
				score += weight * math.Log(1+float64(len(candidates))/float64(df[w]))
			}
		}
		switch {
		case score > bestScore+1e-9:
			best, bestScore, tied = i, score, false
		case score > 0 && math.Abs(score-bestScore) <= 1e-9:
			tied = true
		}
	}

	if best >= 0 && !tied {
		return Choice{
			Agent:   candidates[best].Name,
			Matched: matched[best],
			Reason:  fmt.Sprintf("Picked for: %s.", strings.Join(matched[best], ", ")),
		}, nil
	}
	if baseline == "" || !slices.ContainsFunc(candidates, func(c Candidate) bool { return c.Name == baseline }) {
		return Choice{}, ErrNoAgent
	}
	reason := "No agent's description matched, so the baseline answered."
	if tied {
		reason = "More than one agent matched equally, so the baseline answered."
	}
	return Choice{Agent: baseline, Reason: reason}, nil
}
