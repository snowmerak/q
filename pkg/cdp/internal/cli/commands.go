package cli

import (
	"flag"
	"io"
	"path/filepath"
	"strings"
	"time"
)

const help = `cdp-q-driver — stateful Chromium control with managed launch or CDP attachment

Usage: cdp-q-driver [--session ID] [--chrome PATH | --endpoint URL] [--headless]
                    [--data-dir DIR] [--input-mode paced|fast]
                    [--timeout 30s] COMMAND ...

Session:
  session ensure             Get or create the selected named session
  session list               List sessions saved in OS application data
  session info               Inspect selected session's saved metadata
  start [--chrome PATH | --endpoint URL] [--headless] [--input-mode paced|fast]
                             Get or create a background named session
  serve [--chrome PATH | --endpoint URL] [--headless] [--input-mode paced|fast]
                             Run that process in the foreground
  status                     Inspect the running session
  stop                       Clean up tasks and stop the control process

JavaScript workflows (Node.js required only for run):
  run [--node PATH] SCRIPT|- [ARG ...]
                             Execute an async function body with session API
                             Return JSON; console logs go to stderr

Tasks and pages:
  task new NAME              Create a task (default task exists at startup)
  task list
  task finish [--keep PAGE,...] NAME
  page new [--task NAME] [--name NAME] [URL]
  page adopt [--task NAME] [--name NAME] TARGET_ID
  page list [--all]           List this session's managed tabs; --all includes external tabs
  page goto PAGE URL
  page title|url|frames|activate|close|release PAGE
  page snapshot [--scope viewport|full_page|subtree] [--root REF]
                [--max-nodes 1000] [--max-bytes 65536] PAGE
  page click SELECTOR PAGE
  page fill SELECTOR PAGE TEXT
  page press SELECTOR PAGE KEY
  page upload SELECTOR PAGE [ABSOLUTE_FILE ...]  (empty list clears)
  page eval PAGE EXPRESSION   Use - as EXPRESSION to read stdin
  page screenshot PAGE FILE  Create a new PNG; never overwrite
  page wait-selector SELECTOR PAGE
  page wait-function PAGE EXPRESSION
  page wait-idle [--idle 500ms] PAGE

SELECTOR: exactly one of --ref REF, --css CSS, --text TEXT,
          --role ROLE [--name ACCESSIBLE_NAME]; optional --frame FRAME_ID
          Use the opaque CLI ref returned by snapshot, not a guessed @N.

Events and files (arm BEFORE triggering an action):
  events arm --kind KIND PAGE       Return a subscription ID
  events next SUBSCRIPTION          Wait for one event; return typed handle ID
  events close SUBSCRIPTION
  popup adopt [--name NAME] HANDLE  Attach expected popup to its opener's task
  dialog current PAGE              Recover a currently open dialog handle
  dialog accept HANDLE [TEXT]
  dialog dismiss HANDLE
  chooser interception PAGE on|off
  chooser set HANDLE [ABSOLUTE_FILE ...]
  chooser cancel HANDLE
  download enable DIRECTORY        Default-context policy change; shared FS
  download info|wait|cancel HANDLE
  download save HANDLE DESTINATION Create a new file; never overwrite
  handle close HANDLE              Forget a handle, not its task resource

KIND: popup, dialog, fileChooser, download, request, requestFinished, requestFailed
All command flags precede positional arguments. Successful commands and errors
are JSON. Help is text. Exit codes: 0 success, 1 operation failure, 2 usage.
The local process retains refs/tasks/subscriptions across invocations. stop
closes owned pages and releases borrowed pages. Crash recovery is not automatic.
New named sessions auto-start on task/page/event commands; first use needs
an installed Chrome/Chromium, --chrome PATH, or --endpoint URL.
Explicit flags and saved settings take precedence; otherwise QDRIVER_CHROME,
PATH, and standard OS installation locations select Chrome automatically.
Later starts reuse saved configuration.
Input defaults to paced: interpolated pointer movement and character keydown/up
with bounded timing jitter. --input-mode fast removes delays/interpolation;
it still sends character key events. Changing mode requires stopping the session.
--chrome launches a visible browser with a dedicated persistent session profile
and an automatic loopback CDP port; --headless opts out of the visible window.
stop also closes a Chrome instance launched by this session; external endpoint
browsers stay running. status/stop/info/list do not
auto-start. --state FILE selects the previous explicit-file/manual-start mode.
run reuses the selected session; browser commands keep their existing semantics.
Its script API is specific to cdp-q-driver, not ego-browser compatible.
`

func parseCommand(args []string, stdin io.Reader) (request, error) {
	var r request
	if len(args) == 0 {
		return r, invalid("missing command; use help")
	}
	if len(args) == 1 && (args[0] == "status" || args[0] == "stop") {
		r.Command = args[0]
		return r, nil
	}
	if len(args) < 2 {
		return r, invalid("command requires a subcommand; use help")
	}
	r.Command = args[0] + "." + args[1]
	f := flag.NewFlagSet(r.Command, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var ref, css, text, role, roleName, frame, keep, kind string
	var idle time.Duration
	selector := false
	switch r.Command {
	case "session.list", "session.info", "session.ensure", "task.new", "task.list", "page.goto", "page.title", "page.url", "page.frames", "page.activate", "page.close", "page.release", "page.eval", "page.screenshot", "page.wait-function", "events.next", "events.close", "dialog.current", "dialog.accept", "dialog.dismiss", "chooser.interception", "chooser.set", "chooser.cancel", "download.enable", "download.info", "download.wait", "download.cancel", "download.save", "handle.close":
	case "page.list":
		f.BoolVar(&r.All, "all", false, "include unmanaged browser tabs")
	case "task.finish":
		f.StringVar(&keep, "keep", "", "comma-separated managed page names to retain")
	case "page.new", "page.adopt":
		f.StringVar(&r.Task, "task", "default", "task name")
		f.StringVar(&r.Name, "name", "", "page name")
	case "page.snapshot":
		f.StringVar(&r.Scope, "scope", "viewport", "snapshot scope")
		f.StringVar(&r.Root, "root", "", "subtree root ref")
		f.IntVar(&r.MaxNodes, "max-nodes", 1000, "maximum nodes")
		f.IntVar(&r.MaxBytes, "max-bytes", 65536, "maximum content bytes")
	case "page.click", "page.fill", "page.press", "page.upload", "page.wait-selector":
		selector = true
	case "page.wait-idle":
		f.DurationVar(&idle, "idle", 500*time.Millisecond, "quiet interval")
	case "events.arm":
		f.StringVar(&kind, "kind", "", "event kind")
	case "popup.adopt":
		f.StringVar(&r.Name, "name", "", "page name")
	default:
		return r, invalid("unknown command %q; use help", strings.Join(args[:2], " "))
	}
	if selector {
		f.StringVar(&ref, "ref", "", "opaque snapshot ref")
		f.StringVar(&css, "css", "", "CSS selector")
		f.StringVar(&text, "text", "", "exact text")
		f.StringVar(&role, "role", "", "accessibility role")
		f.StringVar(&roleName, "name", "", "accessible name")
		f.StringVar(&frame, "frame", "", "frame ID")
	}
	if err := f.Parse(args[2:]); err != nil {
		return r, invalid("%v", err)
	}
	p := f.Args()
	seen := make(map[string]bool)
	f.Visit(func(v *flag.Flag) { seen[v.Name] = true })
	if selector {
		count := 0
		for _, key := range []string{"ref", "css", "text", "role"} {
			if seen[key] {
				count++
				r.Locator.Kind = key
			}
		}
		if count != 1 {
			return r, invalid("specify exactly one of --ref, --css, --text or --role")
		}
		switch r.Locator.Kind {
		case "ref":
			r.Locator.Value = ref
		case "css":
			r.Locator.Value = css
		case "text":
			r.Locator.Value = text
		case "role":
			r.Locator.Value = role
		}
		if seen["name"] && r.Locator.Kind != "role" {
			return r, invalid("--name requires --role")
		}
		if seen["frame"] && r.Locator.Kind == "ref" {
			return r, invalid("refs already carry their frame; omit --frame")
		}
		r.Locator.Name = roleName
		r.Locator.Frame = frame
		r.Locator.FrameSet = seen["frame"]
	}
	need := func(min, max int) error {
		if len(p) < min || (max >= 0 && len(p) > max) {
			return invalid("%s expects %d positional arguments; flags must precede them", r.Command, min)
		}
		return nil
	}
	switch r.Command {
	case "session.list", "session.info", "session.ensure", "task.list", "page.list":
		if err := need(0, 0); err != nil {
			return r, err
		}
	case "page.new":
		if err := need(0, 1); err != nil {
			return r, err
		}
		if len(p) > 0 {
			r.Value = p[0]
		}
	case "page.adopt", "task.new", "task.finish":
		if err := need(1, 1); err != nil {
			return r, err
		}
		if r.Command == "page.adopt" {
			r.ID = p[0]
		} else {
			r.Task = p[0]
		}
		if keep != "" {
			r.Keep = strings.Split(keep, ",")
		}
	case "page.goto", "page.fill", "page.press", "page.eval", "page.screenshot", "page.wait-function", "chooser.interception":
		if err := need(2, 2); err != nil {
			return r, err
		}
		r.Page = p[0]
		r.Value = p[1]
		if r.Command == "page.eval" && r.Value == "-" {
			data, err := io.ReadAll(io.LimitReader(stdin, (1<<20)+1))
			if err != nil {
				return r, err
			}
			if len(data) > 1<<20 {
				return r, invalid("stdin expression exceeds 1 MiB")
			}
			r.Value = string(data)
		}
		if r.Command == "page.screenshot" {
			path, err := filepath.Abs(r.Value)
			if err != nil {
				return r, err
			}
			r.Value = path
		}
	case "page.upload", "chooser.set":
		if err := need(1, -1); err != nil {
			return r, err
		}
		if r.Command == "page.upload" {
			r.Page = p[0]
		} else {
			r.ID = p[0]
		}
		r.Paths = append([]string{}, p[1:]...)
		for _, path := range r.Paths {
			if !filepath.IsAbs(path) {
				return r, invalid("file paths must be absolute: %q", path)
			}
		}
	case "dialog.accept":
		if err := need(1, 2); err != nil {
			return r, err
		}
		r.ID = p[0]
		if len(p) > 1 {
			r.Value = p[1]
		}
	case "download.save":
		if err := need(2, 2); err != nil {
			return r, err
		}
		r.ID = p[0]
		path, err := filepath.Abs(p[1])
		if err != nil {
			return r, err
		}
		r.Value = path
	case "events.next", "events.close", "popup.adopt", "dialog.dismiss", "chooser.cancel", "download.info", "download.wait", "download.cancel", "handle.close":
		if err := need(1, 1); err != nil {
			return r, err
		}
		r.ID = p[0]
	case "download.enable":
		if err := need(1, 1); err != nil {
			return r, err
		}
		path, err := filepath.Abs(p[0])
		if err != nil {
			return r, err
		}
		r.Value = path
	default:
		if err := need(1, 1); err != nil {
			return r, err
		}
		r.Page = p[0]
	}
	if r.Command == "page.wait-idle" {
		if idle < 0 || idle > 24*time.Hour {
			return r, invalid("idle must be between 0 and 24h")
		}
		r.DurationMS = idle.Milliseconds()
	}
	if r.Command == "events.arm" {
		switch kind {
		case "popup", "dialog", "fileChooser", "download", "request", "requestFinished", "requestFailed":
			r.Value = kind
		default:
			return r, invalid("specify a supported --kind")
		}
	}
	if r.Command == "page.snapshot" {
		if r.MaxNodes < 1 || r.MaxNodes > 10000 || r.MaxBytes < 1 || r.MaxBytes > 1<<20 {
			return r, invalid("snapshot limits: 1..10000 nodes and 1..1048576 content bytes")
		}
		switch r.Scope {
		case "viewport", "full_page":
			if r.Root != "" {
				return r, invalid("--root requires --scope subtree")
			}
		case "subtree":
			if r.Root == "" {
				return r, invalid("subtree requires --root")
			}
		default:
			return r, invalid("unknown snapshot scope")
		}
	}
	if r.Command == "chooser.interception" && r.Value != "on" && r.Value != "off" {
		return r, invalid("chooser interception requires on or off")
	}
	return r, nil
}
