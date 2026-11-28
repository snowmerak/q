// Command q-desktop runs the Wails v3 desktop shell around Q v2's in-process
// application core (pkg/app), providing a native WebView window and system tray.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/snowmerak/q/v2/internal/fsutil"
	"github.com/snowmerak/q/v2/pkg/app"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("q-desktop", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dataDir := fs.String("data-dir", "", "global q data directory override (default ~/.q or $Q_DATA_DIR)")
	listen := fs.String("listen", "", "HTTP listen address override")
	startHidden := fs.Bool("start-hidden", false, "start minimized to the system tray")
	noTray := fs.Bool("no-tray", false, "disable system tray and quit directly on window close")
	registerAutostart := fs.Bool("register-autostart", false, "register launch-at-login and exit")
	unregisterAutostart := fs.Bool("unregister-autostart", false, "unregister launch-at-login and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *registerAutostart || *unregisterAutostart {
		wailsApp := application.New(application.Options{
			Name:        "Q",
			Description: "Character-chat-first AI platform",
		})
		if _, err := toggleAutostart(wailsApp.Autostart, *registerAutostart && !*unregisterAutostart); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "q-desktop: autostart configuration failed: %v\n", err)
			return 1
		}
		return 0
	}

	rootCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	instance, err := app.Open(rootCtx, app.Options{
		DataDir:        *dataDir,
		ListenOverride: *listen,
	})
	if err != nil {
		if errors.Is(err, fsutil.ErrLocked) {
			_, _ = fmt.Fprintln(os.Stderr, "q-desktop: another q instance is already running for this data directory")
			return 1
		}
		_, _ = fmt.Fprintf(os.Stderr, "q-desktop: startup failed: %v\n", err)
		return 1
	}
	defer func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		_ = instance.Close(shutdownCtx)
	}()

	addr, serveErrCh, err := instance.StartAsync(rootCtx)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "q-desktop: failed to start HTTP server: %v\n", err)
		return 1
	}

	baseURL := formatLoopbackURL(addr)
	iconBytes := appIconBytes()
	var quitting atomic.Bool

	wailsApp := application.New(application.Options{
		Name:        "Q",
		Description: "Character-chat-first AI platform",
		Icon:        iconBytes,
		OnShutdown: func() {
			cancel()
			shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancelShutdown()
			_ = instance.Close(shutdownCtx)
		},
	})

	mainWin := wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main",
		Title:     "Q",
		Width:     1280,
		Height:    820,
		MinWidth:  960,
		MinHeight: 640,
		Hidden:    *startHidden,
		URL:       baseURL,
	})

	showMainWindow := func() {
		if mainWin == nil {
			return
		}
		mainWin.Show()
		mainWin.Restore()
		mainWin.Focus()
	}

	if !*noTray {
		mainWin.RegisterHook(events.Common.WindowClosing, func(ev *application.WindowEvent) {
			if shouldHideOnClose(*noTray, quitting.Load()) {
				mainWin.Hide()
				ev.Cancel()
			}
		})

		tray := wailsApp.SystemTray.New()
		tray.SetIcon(iconBytes)
		tray.SetTooltip("Q (" + baseURL + ")")
		tray.OnClick(showMainWindow)

		menu := wailsApp.NewMenu()
		menu.Add("Show Q").OnClick(func(*application.Context) {
			showMainWindow()
		})
		menu.Add("Open in Browser").OnClick(func(*application.Context) {
			_ = wailsApp.Browser.OpenURL(baseURL)
		})
		autostartEnabled, _ := wailsApp.Autostart.IsEnabled()
		autoItem := menu.AddCheckbox("Start at Login", autostartEnabled)
		autoItem.OnClick(func(*application.Context) {
			next, toggleErr := toggleAutostart(wailsApp.Autostart, autoItem.Checked())
			if toggleErr != nil {
				_, _ = fmt.Fprintf(os.Stderr, "q-desktop: autostart toggle failed: %v\n", toggleErr)
			}
			autoItem.SetChecked(next)
		})
		menu.AddSeparator()
		menu.Add("Quit Q").OnClick(func(*application.Context) {
			quitting.Store(true)
			wailsApp.Quit()
		})
		tray.SetMenu(menu)
	}

	go func() {
		select {
		case serveErr := <-serveErrCh:
			if serveErr != nil {
				_, _ = fmt.Fprintf(os.Stderr, "q-desktop: server exited unexpectedly: %v\n", serveErr)
			}
			quitting.Store(true)
			wailsApp.Quit()
		case <-rootCtx.Done():
			quitting.Store(true)
			wailsApp.Quit()
		}
	}()

	if err := wailsApp.Run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "q-desktop: GUI error: %v\n", err)
		return 1
	}
	return 0
}

type autostartController interface {
	IsEnabled() (bool, error)
	EnableWithOptions(application.AutostartOptions) error
	Disable() error
}

func toggleAutostart(mgr autostartController, want bool) (bool, error) {
	if mgr == nil {
		return false, errors.New("autostart manager is nil")
	}
	if want {
		if err := mgr.EnableWithOptions(application.AutostartOptions{
			Arguments: []string{"--start-hidden"},
		}); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := mgr.Disable(); err != nil {
		return true, err
	}
	return false, nil
}

func formatLoopbackURL(addr string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return "http://" + strings.TrimSpace(addr)
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func shouldHideOnClose(noTray, quitting bool) bool {
	return !noTray && !quitting
}
