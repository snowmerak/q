package builtin

import (
	"runtime"
	"strings"
	"testing"
)

func TestRunCommandStatusAndWait(t *testing.T) {
	fs, err := NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	command := "sleep 0.05; printf 'hello from command'"
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Milliseconds 50; Write-Output 'hello from command'"
	}
	started, err := fs.RunCommand(RunCommandInput{Command: command})
	if err != nil {
		t.Fatal(err)
	}
	if started.CommandID == "" || started.PID <= 0 || started.Workdir != fs.Root {
		t.Fatalf("started command = %#v", started)
	}
	status, err := fs.CommandStatus(CommandInput{CommandID: started.CommandID})
	if err != nil {
		t.Fatal(err)
	}
	if status.Status == "running" && status.NextAction != "wait" {
		t.Fatalf("running status next action = %q; want wait", status.NextAction)
	}
	finished, err := fs.WaitCommand(WaitInput{CommandID: started.CommandID, Offset: status.NextOffset, TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	combined := status.Output + finished.Output
	if finished.Status != "succeeded" || finished.ExitCode == nil || *finished.ExitCode != 0 ||
		finished.NextAction != "" || !strings.Contains(combined, "hello from command") {
		t.Fatalf("status = %#v, output = %q", finished, combined)
	}
}

func TestRunCommandRejectsEscapingWorkdir(t *testing.T) {
	fs, err := NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	if _, err := fs.RunCommand(RunCommandInput{Command: "echo no", Workdir: ".."}); err == nil {
		t.Fatal("run_command accepted a workdir outside the workspace")
	}
	if _, err := fs.CommandStatus(CommandInput{CommandID: "missing"}); err == nil {
		t.Fatal("cmd_status accepted an unknown command id")
	}
}
