package platform

import (
	"context"
	"fmt"
	"time"

	"github.com/urmzd/opentag/pkg/envelope"

	"github.com/urmzd/open-agent-platform/pkg/evals"
	"github.com/urmzd/open-agent-platform/pkg/runs"
)

// OriginEval marks a run raised by an eval, so it can be told apart from work
// a person or a schedule asked for.
const OriginEval = "eval"

// caseTimeout bounds one agent case. A case that outlives it fails, which is a
// result, rather than holding the suite open.
const caseTimeout = 2 * time.Minute

// StartEval begins a run of a suite and returns at once. The cases execute in
// the background, and the stored run is updated as each one finishes.
func (p *Platform) StartEval(ctx context.Context, tenant, name, by string) (evals.Run, error) {
	suite, err := p.Evals.Get(tenant, name)
	if err != nil {
		return evals.Run{}, err
	}
	rev := 0
	switch suite.Target {
	case evals.Agent:
		// Pin the revision now, so the run records what it actually tested
		// even if the agent is revised while the cases are in flight.
		r, err := p.Agents.Get(ctx, tenant, suite.Subject, 0)
		if err != nil {
			return evals.Run{}, fmt.Errorf("the suite's agent: %w", err)
		}
		rev = r.Rev
	case evals.Knowledge:
		if _, err := p.Knowledge.Documents(tenant, suite.Subject); err != nil {
			return evals.Run{}, fmt.Errorf("the suite's source: %w", err)
		}
	}
	run, err := p.Evals.Begin(tenant, suite, rev, by, p.now())
	if err != nil {
		return evals.Run{}, err
	}

	// The run outlives the request that started it, and works on its own
	// copy: the caller is handed the run as it stood when it began.
	started := run
	go func() {
		run := started
		bg := context.WithoutCancel(ctx)
		var results []evals.Result
		var metrics map[string]float64
		if suite.Target == evals.Agent {
			results = p.evalAgent(bg, tenant, suite, &run)
		} else {
			results, metrics = p.evalKnowledge(tenant, suite)
		}
		run.Finish(results, metrics, p.now())
		p.Evals.Save(tenant, run)
		p.log.Info("platform: eval finished", "suite", suite.Name, "status", run.Status, "pass_rate", run.PassRate)
		for _, a := range p.Alerts.EvaluateEval(tenant, run) {
			p.log.Warn("platform: alert", "rule", a.Rule, "severity", a.Severity, "title", a.Title)
		}
	}()
	return started, nil
}

// evalAgent sends each case to the agent as a real run and checks its answer.
// Cases run one after another, so a suite cannot flood the agent it measures.
func (p *Platform) evalAgent(ctx context.Context, tenant string, suite evals.Suite, run *evals.Run) []evals.Result {
	results := make([]evals.Result, 0, len(suite.Cases))
	for _, c := range suite.Cases {
		res := p.evalCase(ctx, tenant, suite, run.ID, c)
		results = append(results, res)
		// Publish progress, so the console can show cases as they land.
		run.Results = results
		p.Evals.Save(tenant, *run)
	}
	return results
}

func (p *Platform) evalCase(ctx context.Context, tenant string, suite evals.Suite, evalID string, c evals.Case) evals.Result {
	failed := func(msg string, r runs.Run) evals.Result {
		return evals.Result{
			CaseID: c.ID, Input: c.Input, RunID: r.ID, Error: msg,
			DurationMs: r.UpdatedAt.Sub(r.CreatedAt).Milliseconds(),
		}
	}
	started, err := p.Invoke(ctx, envelope.Tag{
		ID:     fmt.Sprintf("eval-%s-%s-%s", tenant, evalID, c.ID),
		Tenant: tenant,
		Agent:  suite.Subject,
		Origin: OriginEval,
		Text:   c.Input,
		Actor:  envelope.Actor{ID: "eval:" + suite.Name, Display: "eval " + suite.Name, Bot: true},
		Meta:   map[string]string{"eval_suite": suite.Name, "eval_case": c.ID},
	})
	if err != nil {
		return failed("the run could not be started: "+err.Error(), runs.Run{})
	}

	deadline := time.Now().Add(caseTimeout)
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		r, err := p.Runs.Get(tenant, started.ID)
		switch {
		case err != nil:
			return failed("the run was lost", started)
		case r.Status == runs.Failed:
			return failed("the run failed: "+r.Error, r)
		case r.Status == runs.Completed:
			res, err := evals.ScoreAnswer(ctx, c, r.Answer, r.UpdatedAt.Sub(r.CreatedAt))
			if err != nil {
				return failed("the answer could not be scored: "+err.Error(), r)
			}
			res.RunID = r.ID
			return res
		case time.Now().After(deadline):
			return failed(fmt.Sprintf("the run did not end within %s", caseTimeout), r)
		}
		select {
		case <-ctx.Done():
			return failed("the eval was interrupted", r)
		case <-tick.C:
		}
	}
}

// evalKnowledge asks the source each query and scores what comes back.
func (p *Platform) evalKnowledge(tenant string, suite evals.Suite) ([]evals.Result, map[string]float64) {
	k := suite.K
	if k <= 0 {
		k = evals.DefaultK
	}
	results := make([]evals.Result, 0, len(suite.Cases))
	sums := map[string]float64{}
	for _, c := range suite.Cases {
		began := time.Now()
		// Ask for more passages than k: several can come from one document,
		// and a case is judged on k distinct documents.
		hits, err := p.Knowledge.Search(tenant, []string{suite.Subject}, c.Input, 50, "")
		if err != nil {
			results = append(results, evals.Result{CaseID: c.ID, Input: c.Input, Error: err.Error()})
			continue
		}
		got := make([]evals.Retrieved, len(hits))
		for i, h := range hits {
			got[i] = evals.Retrieved{Title: h.Title, Score: h.Score}
		}
		res, scores := evals.ScoreRetrieval(c, got, k, time.Since(began))
		results = append(results, res)
		sums["hit_rate"] += scores.HitRate
		sums["mrr"] += scores.MRR
		sums["recall"] += scores.ContextRecall
		sums["precision"] += scores.ContextPrecision
		sums["ndcg"] += scores.NDCG
	}
	// Means are over every case, so a case that errored counts as a miss.
	for name := range sums {
		sums[name] /= float64(len(suite.Cases))
	}
	return results, sums
}
