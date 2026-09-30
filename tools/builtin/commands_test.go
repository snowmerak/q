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

func TestWaitTimeoutLeavesCommandRunningAndAllowsContinuation(t *testing.T) {
	fs, err := NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	command := "sleep 1; printf 'completed after wait timeout'"
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Milliseconds 1000; Write-Output 'completed after wait timeout'"
	}
	started, err := fs.RunCommand(RunCommandInput{Command: command})
	if err != nil {
		t.Fatal(err)
	}
	first, err := fs.WaitCommand(WaitInput{CommandID: started.CommandID, TimeoutMS: 10})
	if err != nil || first.Status != "running" || first.NextAction != "wait" || first.ExitCode != nil {
		t.Fatalf("timeout result = %#v, %v", first, err)
	}
	last, err := fs.WaitCommand(WaitInput{CommandID: started.CommandID, Offset: first.NextOffset, TimeoutMS: 5000})
	if err != nil || last.Status != "succeeded" || !strings.Contains(first.Output+last.Output, "completed after wait timeout") {
		t.Fatalf("continued result = %#v, %v", last, err)
	}
}

func TestCloseTerminatesRunningCommand(t *testing.T) {
	fs, err := NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	command := "sleep 30"
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Seconds 30"
	}
	started, err := fs.RunCommand(RunCommandInput{Command: command})
	if err != nil {
		t.Fatal(err)
	}
	fs.Close()
	finished, err := fs.WaitCommand(WaitInput{CommandID: started.CommandID, TimeoutMS: 5000})
	if err != nil || finished.Status != "failed" || finished.ExitCode == nil {
		t.Fatalf("closed command = %#v, %v", finished, err)
	}
}

func TestCommandOutputOffsetsReportEvictionAndDrainWithoutDuplicates(t *testing.T) {
	buffer := commandBuffer{limit: 8}
	_, _ = buffer.Write([]byte("abcdefgh"))
	first, cursor, more, truncated := buffer.read(0, 3)
	if first != "abc" || cursor != 3 || !more || truncated {
		t.Fatalf("first page = %q %d %v %v", first, cursor, more, truncated)
	}
	_, _ = buffer.Write([]byte("ijklmn"))
	second, cursor, more, truncated := buffer.read(cursor, 4)
	if second != "ghij" || cursor != 10 || !more || !truncated {
		t.Fatalf("evicted page = %q %d %v %v", second, cursor, more, truncated)
	}
	last, cursor, more, truncated := buffer.read(cursor, 4)
	if last != "klmn" || cursor != 14 || more || truncated {
		t.Fatalf("last page = %q %d %v %v", last, cursor, more, truncated)
	}
	empty, end, more, truncated := buffer.read(cursor, 4)
	if empty != "" || end != cursor || more || truncated {
		t.Fatalf("drained page = %q %d %v %v", empty, end, more, truncated)
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
