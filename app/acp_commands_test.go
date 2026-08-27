package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

func advertiseTestCommands(t *testing.T, remote *acpRemoteClient, session acp.SessionId, commands ...acp.AvailableCommand) {
	t.Helper()
	if err := remote.SessionUpdate(t.Context(), acp.SessionNotification{
		SessionId: session,
		Update: acp.SessionUpdate{AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{
			AvailableCommands: commands,
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestACPCommandCompletionRetainsEarlyAndIdleAnnouncements(t *testing.T) {
	remote := &acpRemoteClient{connection: &fakeACPRemoteConnection{newSessionID: "session-1"}}
	advertiseTestCommands(t, remote, "session-1", acp.AvailableCommand{
		Name: "review", Description: "Review changes",
		Input: &acp.AvailableCommandInput{Unstructured: &acp.UnstructuredCommandInput{Hint: "scope"}},
	})
	if _, err := remote.newSession(t.Context()); err != nil {
		t.Fatal(err)
	}
	commands := remote.slashCommands()
	if names := completionNames(commands); !slices.Equal(names, []string{"/new", "/review", "/clear"}) || commands[1].arguments != "<scope>" {
		t.Fatalf("lost commands announced before session/new returned: %#v", commands)
	}

	m := newSlashCompletionModel(t)
	m.client = remote
	m.input.SetValue("/")
	if names := completionNames(m.slashCompletionMatches()); !slices.Equal(names, completionNames(commands)) {
		t.Fatalf("ACP client offered local settings commands: %v", names)
	}
	advertiseTestCommands(t, remote, "session-1", acp.AvailableCommand{Name: "status", Description: "Agent status"})
	if names := completionNames(m.slashCompletionMatches()); !slices.Equal(names, []string{"/new", "/status", "/clear"}) {
		t.Fatalf("idle command update did not replace the catalog: %v", names)
	}
	advertiseTestCommands(t, remote, "other-session", acp.AvailableCommand{Name: "wrong-session"})
	if names := completionNames(m.slashCompletionMatches()); slices.Contains(names, "/wrong-session") || !slices.Contains(names, "/status") {
		t.Fatalf("another session replaced active commands: %v", names)
	}
	advertiseTestCommands(t, remote, "session-1")
	if names := completionNames(m.slashCompletionMatches()); !slices.Equal(names, []string{"/new", "/clear"}) {
		t.Fatalf("empty announcement retained removed commands: %v", names)
	}
}

func TestACPCommandCompletionResetsWithSession(t *testing.T) {
	remote := &acpRemoteClient{
		sessionID: "old", connection: &fakeACPRemoteConnection{newSessionID: "new"},
	}
	advertiseTestCommands(t, remote, "old", acp.AvailableCommand{Name: "old-command"})
	advertiseTestCommands(t, remote, "new", acp.AvailableCommand{Name: "new-command"})
	if _, err := remote.resetSession(t.Context()); err != nil {
		t.Fatal(err)
	}
	if names := completionNames(remote.slashCommands()); !slices.Equal(names, []string{"/new", "/new-command", "/clear"}) {
		t.Fatalf("reset did not switch command catalogs: %v", names)
	}
	if len(remote.slashCommandsBySession) != 1 {
		t.Fatal("reset retained obsolete session command catalogs")
	}
}

func TestACPCommandCompletionValidatesAndSanitizesAnnouncements(t *testing.T) {
	remote := &acpRemoteClient{sessionID: "test"}
	advertiseTestCommands(t, remote, "test",
		acp.AvailableCommand{Name: "new", Description: "Remote override"},
		acp.AvailableCommand{Name: "clear", Description: "Remote clear"},
		acp.AvailableCommand{Name: "/review", Description: "\x1b[31mReview\x1b[0m\nchanges\a",
			Input: &acp.AvailableCommandInput{Unstructured: &acp.UnstructuredCommandInput{Hint: "scope\nbranch"}}},
		acp.AvailableCommand{Name: "review", Description: "Duplicate"},
		acp.AvailableCommand{Name: "bad\ncommand"},
		acp.AvailableCommand{Name: "bad command"},
		acp.AvailableCommand{Name: "bad/path"},
		acp.AvailableCommand{Name: "bad\x1b[0m"},
		acp.AvailableCommand{Name: ""},
	)
	commands := remote.slashCommands()
	if names := completionNames(commands); !slices.Equal(names, []string{"/new", "/clear", "/review"}) {
		t.Fatalf("invalid or duplicate completions: %v", names)
	}
	if commands[0].description != "Start a new ACP session." || commands[1].description != "Remote clear" || commands[2].description != "Review changes" || commands[2].arguments != "<scope branch>" {
		t.Fatalf("unsafe or inaccurate command metadata: %#v", commands)
	}
	m := newSlashCompletionModel(t)
	m.client = remote
	m.input.SetValue("/r")
	view := m.renderSlashCompletion(60, 8)
	if strings.Contains(view, "\a") || !strings.Contains(view, "/review <scope branch>") {
		t.Fatalf("unsafe completion rendering: %q", view)
	}
}
