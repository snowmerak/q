package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func handleWorkflowCommand(ctx context.Context, cfg *Config, args []string) error {
	if len(args) == 0 {
		return workflowUsageError()
	}
	switch strings.ToLower(args[0]) {
	case "run":
		if len(args) < 2 {
			return fmt.Errorf("usage: q workflow run <workflow.yaml> [input]")
		}
		workflow, err := LoadWorkflow(args[1])
		if err != nil {
			return err
		}
		session, err := loadLastSession(cfg)
		if err != nil {
			return err
		}
		run, err := NewWorkflowRun(workflow, args[1], session.Name, strings.Join(args[2:], " "))
		if err != nil {
			return err
		}
		if err := SaveWorkflowRun(run); err != nil {
			return err
		}
		PrintInfo(fmt.Sprintf("Workflow run created: %s", run.ID))
		if err := ExecuteWorkflow(ctx, workflow, run, session); err != nil {
			return fmt.Errorf("workflow %s failed: %w", run.ID, err)
		}
		PrintSuccess(fmt.Sprintf("Workflow completed: %s", run.ID))
		return nil
	case "resume":
		if len(args) != 2 {
			return fmt.Errorf("usage: q workflow resume <run_id>")
		}
		run, err := LoadWorkflowRun(args[1])
		if err != nil {
			return err
		}
		workflow := &run.Definition
		if workflow.Version == 0 {
			var err error
			workflow, err = LoadWorkflow(run.WorkflowPath)
			if err != nil {
				return err
			}
		}
		session, err := LoadSession(run.SessionName)
		if err != nil {
			return err
		}
		if run.Status == "completed" {
			return fmt.Errorf("workflow %s is already completed", run.ID)
		}
		if err := ExecuteWorkflow(ctx, workflow, run, session); err != nil {
			return fmt.Errorf("workflow %s failed: %w", run.ID, err)
		}
		PrintSuccess(fmt.Sprintf("Workflow completed: %s", run.ID))
		return nil
	case "status":
		if len(args) != 2 {
			return fmt.Errorf("usage: q workflow status <run_id>")
		}
		run, err := LoadWorkflowRun(args[1])
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(run, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	case "ls", "list":
		runs, err := ListWorkflowRuns()
		if err != nil {
			return err
		}
		if len(runs) == 0 {
			PrintWarning("No workflow runs found.")
			return nil
		}
		for _, run := range runs {
			fmt.Printf("%s\t%s\t%s\titeration=%d\t%s\n", run.ID, run.WorkflowName, run.Status, run.Iteration, run.UpdatedAt.Format("2006-01-02 15:04:05"))
		}
		return nil
	default:
		return workflowUsageError()
	}
}

func workflowUsageError() error {
	return fmt.Errorf("usage: q workflow <run|resume|status|ls> ...")
}
