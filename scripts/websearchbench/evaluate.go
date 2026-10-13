package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Post-run rubric only. These markers are never sent in a controller request.
// They measure verbatim evidence coverage, not correctness of a generated answer.
func coverage(t trial) map[string]bool {
	var b strings.Builder
	for _, e := range t.Evidence {
		u, err := url.Parse(e.URL)
		if err != nil {
			continue
		}
		h := u.Hostname()
		official := t.Task == "go" && (h == "pkg.go.dev" || h == "go.dev") || t.Task == "sqlite" && (h == "sqlite.org" || h == "www.sqlite.org") || t.Task == "python" && h == "docs.python.org"
		if official {
			b.WriteString(e.Text)
			b.WriteByte('\n')
		}
	}
	s := strings.ToLower(strings.Join(strings.Fields(b.String()), " "))
	has := func(q string) bool { return strings.Contains(s, q) }
	switch t.Task {
	case "go":
		return map[string]bool{"cleanup_and_leak": has("stops any associated timers") && has("leaks the child"), "does_not_wait": has("does not wait for the work to stop")}
	case "sqlite":
		return map[string]bool{"network_unsupported": has("network filesystem") && (has("does not work") || has("will not work")), "version_3_22": has("3.22.0"), "sidecars_exist_and_readable": has("are readable") && has("-shm") && has("-wal"), "directory_writable_for_creation": has("write permission on the directory") && has("files can be created"), "immutable_alternative": has("database is immutable") || has("immutable") && has("database connection")}
	case "python":
		return map[string]bool{"timeout_error": has("timeouterror"), "catch_outside_context": has("outside") && has("context manager"), "deadline_reschedule": has("reschedule") && has("deadline")}
	}
	return nil
}

type scoreRow struct {
	Source, Task, Arm, Status string
	Covered, Required         int
	Facts                     map[string]bool
	Seconds, ModelSeconds     float64
	GenCalls, SOCalls         int
	Input, Output             int64
}

func evaluateFiles(paths string, out string) error {
	var rows []scoreRow
	for _, path := range strings.Split(paths, ",") {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var report report
		if err = json.Unmarshal(b, &report); err != nil {
			return err
		}
		for _, t := range report.Trials {
			if t.Arm == "shared-query-planning" {
				continue
			}
			facts := coverage(t)
			row := scoreRow{Source: path, Task: t.Task, Arm: t.Arm, Status: t.Status, Facts: facts, Required: len(facts), Seconds: t.Seconds, GenCalls: t.GenCalls, SOCalls: t.SOCalls}
			for _, ok := range facts {
				if ok {
					row.Covered++
				}
			}
			for _, e := range t.Events {
				row.ModelSeconds += e.Seconds
				row.Input += e.Input
				row.Output += e.Output
			}
			rows = append(rows, row)
			fmt.Printf("%s %s %d/%d %s gen=%d so=%d model=%.1fs total=%.1fs\n", t.Task, t.Arm, row.Covered, row.Required, t.Status, t.GenCalls, t.SOCalls, row.ModelSeconds, row.Seconds)
		}
	}
	if out != "" {
		b, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0644)
	}
	return nil
}
