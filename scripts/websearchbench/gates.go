package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/snowmerak/q/client/systemone"
	"github.com/snowmerak/q/webbrowser"
)

// Requirements restate the user questions, without supplying the answers.
var clauses = map[string][]string{
	"go":     {"Why callers should invoke a context CancelFunc", "Whether calling CancelFunc waits for the work to stop"},
	"sqlite": {"Whether WAL works across machines on a network filesystem", "Whether a read-only process can open a WAL database and all required conditions"},
	"python": {"The exception type raised by asyncio.timeout", "Where that exception can be caught", "Whether and how the asyncio.timeout deadline can be rescheduled"},
}

func (r *runner) checkSource(ctx context.Context, goal string, link webbrowser.Link, tr *trial) (bool, error) {
	req := systemone.Request{Model: r.report.SystemOneModel, State: map[string]any{"goal": goal, "candidate": link}, Questions: map[string]systemone.Question{"publisher": {Type: systemone.QuestionChoice, Instructions: "The goal requires official documentation. Classify this URL's publisher, not the content's topical relevance. A tutorial or mirror about the project is not its official publisher.", Criteria: map[string]string{"official": "Official documentation published by the project or organization named in the goal", "external": "Third-party tutorial, mirror, aggregator, or another publisher", "unknown": "Cannot establish that the publisher is official"}}}}
	started := time.Now()
	res, err := r.so.Evaluate(ctx, req, systemone.CallOptions{})
	e := event{Kind: "source-check", Request: req, Response: res, Seconds: time.Since(started).Seconds()}
	tr.SOCalls++
	fmt.Printf("%s source-check systemone %.2fs error=%v\n", tr.Task, e.Seconds, err)
	if err != nil {
		e.Error = err.Error()
		tr.Events = append(tr.Events, e)
		return false, err
	}
	if res.Usage != nil {
		if res.Usage.InputTokens != nil {
			e.Input = *res.Usage.InputTokens
		}
		if res.Usage.OutputTokens != nil {
			e.Output = *res.Usage.OutputTokens
		}
	}
	tr.Events = append(tr.Events, e)
	answer := res.Answers["publisher"].Choice
	if answer != "official" && answer != "external" && answer != "unknown" {
		return false, fmt.Errorf("invalid publisher choice %q", answer)
	}
	return answer == "official", nil
}

func clauseRequest(task string, evidence []evidence) systemone.Request {
	questions := map[string]systemone.Question{}
	for i, requirement := range clauses[task] {
		questions[fmt.Sprintf("clause%d", i)] = systemone.Question{Type: systemone.QuestionChoice, Instructions: "Judge only the collected passages, never your prior knowledge. Is there explicit source evidence answering this requirement completely? Requirement: " + requirement, Criteria: map[string]string{"supported": "The collected source passages explicitly answer this requirement completely, including required qualifications.", "missing": "The requirement is missing, only loosely related, ambiguous, or incompletely supported by the collected passages."}}
	}
	return systemone.Request{Model: "gemma4-e4b", State: map[string]any{"task": task, "evidence": evidence}, Questions: questions}
}

func (r *runner) gate(ctx context.Context, tr *trial) ([]string, error) {
	req := clauseRequest(tr.Task, tr.Evidence)
	started := time.Now()
	res, err := r.so.Evaluate(ctx, req, systemone.CallOptions{})
	e := event{Kind: "coverage-gate", Request: req, Response: res, Seconds: time.Since(started).Seconds()}
	tr.SOCalls++
	fmt.Printf("%s coverage-gate systemone %.2fs error=%v\n", tr.Task, e.Seconds, err)
	if err != nil {
		e.Error = err.Error()
		tr.Events = append(tr.Events, e)
		return nil, err
	}
	if res.Usage != nil {
		if res.Usage.InputTokens != nil {
			e.Input = *res.Usage.InputTokens
		}
		if res.Usage.OutputTokens != nil {
			e.Output = *res.Usage.OutputTokens
		}
	}
	tr.Events = append(tr.Events, e)
	var missing []string
	for i, c := range clauses[tr.Task] {
		a := res.Answers[fmt.Sprintf("clause%d", i)].Choice
		if a != "supported" && a != "missing" {
			return nil, fmt.Errorf("invalid gate answer %q", a)
		}
		if a == "missing" {
			missing = append(missing, c)
		}
	}
	return missing, nil
}

func runGates(ctx context.Context, c *systemone.Client, path, out string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var rep report
	if err = json.Unmarshal(b, &rep); err != nil {
		return err
	}
	var events []event
	for _, tr := range rep.Trials {
		if tr.Arm != "systemone-loop" {
			continue
		}
		req := clauseRequest(tr.Task, tr.Evidence)
		started := time.Now()
		res, e := c.Evaluate(ctx, req, systemone.CallOptions{})
		record := event{Kind: tr.Task, Request: req, Response: res, Seconds: time.Since(started).Seconds()}
		if e != nil {
			record.Error = e.Error()
		} else if res.Usage != nil {
			if res.Usage.InputTokens != nil {
				record.Input = *res.Usage.InputTokens
			}
			if res.Usage.OutputTokens != nil {
				record.Output = *res.Usage.OutputTokens
			}
		}
		events = append(events, record)
		answers, marshalErr := json.Marshal(res)
		if marshalErr != nil {
			return marshalErr
		}
		fmt.Printf("gate %s %.1fs error=%v %s\n", tr.Task, record.Seconds, e, answers)
	}
	b, err = json.MarshalIndent(events, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(out, b, 0644)
}
