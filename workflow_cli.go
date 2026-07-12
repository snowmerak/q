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
			return fmt.Errorf("usage: q workflow run <name|workflow.yaml> [input]")
		}
		workflowPath, err := ResolveWorkflow(args[1])
		if err != nil {
			return err
		}
		workflow, err := LoadWorkflow(workflowPath)
		if err != nil {
			return err
		}
		session, err := loadLastSession(cfg)
		if err != nil {
			return err
		}
		run, err := NewWorkflowRun(workflow, workflowPath, session.Name, strings.Join(args[2:], " "))
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
		definitions, err := ListWorkflowDefinitions()
		if err != nil {
			return err
		}
		if len(definitions) == 0 {
			PrintWarning("No workflow definitions found.")
			return nil
		}
		for _, definition := range definitions {
			fmt.Printf("%s\t%s\t%s\n", definition.Name, definition.Scope, definition.Path)
		}
		return nil
	case "runs":
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
	case "get":
		if len(args) != 3 {
			return fmt.Errorf("usage: q workflow get <name> <download-url>")
		}
		path, err := DownloadWorkflowDefinition(args[1], args[2])
		if err != nil {
			return err
		}
		PrintSuccess(fmt.Sprintf("Workflow downloaded: %s", path))
		return nil
	case "init":
		if len(args) < 2 || len(args) > 3 {
			return fmt.Errorf("usage: q workflow init <name> [--global]")
		}
		global := len(args) == 3 && args[2] == "--global"
		if len(args) == 3 && !global {
			return fmt.Errorf("unknown option %s", args[2])
		}
		path, err := InitWorkflowDefinition(args[1], global)
		if err != nil {
			return err
		}
		PrintSuccess(fmt.Sprintf("Workflow created: %s", path))
		return nil
	case "edit":
		if len(args) < 2 {
			return fmt.Errorf("usage: q workflow edit <name> [--global] [editor]")
		}
		global := false
		var editorParts []string
		for _, arg := range args[2:] {
			if arg == "--global" {
				global = true
			} else {
				editorParts = append(editorParts, arg)
			}
		}
		var path string
		var err error
		if global {
			path, err = WorkflowDefinitionPath(args[1], true)
		} else {
			path, err = ResolveWorkflow(args[1])
		}
		if err != nil {
			return err
		}
		PrintInfo(fmt.Sprintf("Opening workflow: %s", path))
		return openEditor(path, strings.Join(editorParts, " "))
	case "rm", "remove":
		if len(args) < 2 || len(args) > 3 {
			return fmt.Errorf("usage: q workflow rm <name> [--global]")
		}
		global := len(args) == 3 && args[2] == "--global"
		if len(args) == 3 && !global {
			return fmt.Errorf("unknown option %s", args[2])
		}
		path, err := RemoveWorkflowDefinition(args[1], global)
		if err != nil {
			return err
		}
		PrintSuccess(fmt.Sprintf("Workflow removed: %s", path))
		return nil
	case "path":
		project, err := projectWorkflowDir()
		if err != nil {
			return err
		}
		global, err := globalWorkflowDir()
		if err != nil {
			return err
		}
		fmt.Printf("project\t%s\nglobal\t%s\n", project, global)
		return nil
	default:
		return workflowUsageError()
	}
}

func workflowUsageError() error {
	return fmt.Errorf("usage: q workflow <run|resume|status|ls|runs|get|init|edit|rm|path> ...")
}
