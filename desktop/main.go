package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed assets/icon.png
var appIcon []byte

//go:generate go -C .. run ./scripts/studioicons

type desktopShell struct {
	app       *application.App
	host      *engineHost
	directory string
	settings  desktopSettings
	window    *application.WebviewWindow
	tray      *application.SystemTray
	status    *application.MenuItem
	appMenu   *application.Menu
	canHide   bool
	logger    *slog.Logger
	quit      sync.Once
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	background := flag.Bool("background", false, "start without opening a window")
	autostart := flag.Bool("autostart", false, "launched by the user's login item")
	unregister := flag.Bool("unregister-autostart", false, "remove Q Studio's login item")
	quitExisting := flag.Bool("quit", false, "stop the running Q Studio before an update")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flag.Args())
	}
	if *unregister {
		return setAutostart(false)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	directory := filepath.Join(home, ".q", "desktop")
	if err = os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	settings, err := loadSettings(directory)
	if err != nil {
		return fmt.Errorf("load desktop settings: %w", err)
	}
	logPath := filepath.Join(directory, "desktop.log")
	if info, e := os.Stat(logPath); e == nil && info.Size() > 5<<20 {
		_ = os.Remove(logPath + ".1")
		_ = os.Rename(logPath, logPath+".1")
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	logger := slog.New(slog.NewJSONHandler(logFile, nil))
	shell := &desktopShell{directory: directory, settings: settings, canHide: trayAvailable(), logger: logger}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitorDone := make(chan struct{})
	app := application.New(application.Options{
		Name: "Q Studio", Description: "Q workspace coding agent", Icon: windowIcon(), Logger: logger,
		Assets:  application.AssetOptions{Handler: shell, DisableLogging: true},
		Linux:   application.LinuxOptions{ApplicationID: "ne.saturday.q-studio"},
		Mac:     application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: false},
		Windows: application.WindowsOptions{WndClass: "QStudioApplication"},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: singleInstanceID(home, runtime.GOOS),
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				if slices.Contains(data.Args, "--quit") {
					go shell.app.Quit()
					return
				}
				if !slices.Contains(data.Args, "--background") && !slices.Contains(data.Args, "--autostart") {
					application.InvokeAsync(shell.show)
				}
			},
		},
		OnShutdown: func() {
			shell.quit.Do(func() {
				cancel()
				<-monitorDone
				if shell.host != nil {
					shell.host.close()
				}
				logger.Info("desktop.stopped")
			})
		},
	})
	shell.app = app
	if *quitExisting {
		close(monitorDone)
		return nil
	}
	engine, err := bundledEngine()
	if err != nil {
		close(monitorDone)
		return err
	}
	shell.host = newEngineHost(engine, logFile)
	if err = shell.host.connect(ctx); err != nil {
		logger.Error("desktop.engine.start_failed", "error", err)
	}
	shell.tray = app.SystemTray.New()
	shell.tray.SetIcon(appIcon)
	shell.tray.OnClick(func() { application.InvokeAsync(shell.show) })
	shell.tray.OnRightClick(func() { application.InvokeAsync(func() { shell.menu(); shell.tray.OpenMenu() }) })
	shell.menu()
	go func() {
		defer close(monitorDone)
		shell.host.monitor(ctx, func() { application.InvokeAsync(func() { shell.updateStatus() }) })
	}()
	if (!*background && (!*autostart || !settings.StartHidden)) || !shell.canHide {
		shell.show()
	}
	logger.Info("desktop.started", "background", *background || *autostart && settings.StartHidden, "tray_available", shell.canHide)
	err = app.Run()
	// Some platforms terminate from the native loop, so OnShutdown is primary.
	shell.quit.Do(func() { cancel(); <-monitorDone; shell.host.close() })
	return err
}

func windowIcon() []byte {
	if runtime.GOOS == "darwin" {
		if executable, err := os.Executable(); err == nil {
			// Use the bundle's full-size icon for the Dock when it is available.
			icon := filepath.Join(filepath.Dir(executable), "..", "Resources", "icons.icns")
			if info, err := os.Stat(icon); err == nil && info.Mode().IsRegular() {
				return nil
			}
		}
	}
	return appIcon
}

func (s *desktopShell) show() {
	if s.window == nil {
		s.window = s.app.Window.NewWithOptions(application.WebviewWindowOptions{Name: "q-studio", Title: "Q Studio", Width: 1280, Height: 850, MinWidth: 760, MinHeight: 540, URL: "/", DevToolsEnabled: false, UseApplicationMenu: true})
		s.window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
			if s.settings.KeepRunning && s.canHide {
				s.window.Hide()
				e.Cancel()
			} else {
				e.Cancel()
				go s.app.Quit()
			}
		})
	}
	s.window.Show()
	s.window.Restore()
	s.window.Focus()
}

func (s *desktopShell) report(err error) {
	if err != nil {
		s.logger.Error("desktop.action.failed", "error", err)
		s.app.Dialog.Error().SetTitle("Q Studio").SetMessage(err.Error()).Show()
	}
}

func (s *desktopShell) menu() {
	menu := application.NewMenu()
	menu.Add("Open Q Studio").OnClick(func(*application.Context) { application.InvokeAsync(s.show) })
	menu.Add("Open in browser").OnClick(func(*application.Context) {
		go func() {
			url, err := s.host.browserURL(context.Background())
			if err == nil {
				err = s.app.Browser.OpenURL(url)
			}
			application.InvokeAsync(func() { s.report(err) })
		}()
	})
	s.status = menu.Add("Starting engine…").SetEnabled(false)
	menu.AddSeparator()
	keep := menu.AddCheckbox("Keep running when window closes", s.settings.KeepRunning).SetEnabled(s.canHide)
	keep.OnClick(func(*application.Context) {
		application.InvokeAsync(func() {
			candidate := s.settings
			candidate.KeepRunning = !candidate.KeepRunning
			if err := saveSettings(s.directory, candidate); err != nil {
				s.report(err)
			} else {
				s.settings = candidate
			}
			s.menu()
		})
	})
	enabled, err := autostartEnabled()
	if err != nil {
		s.logger.Warn("desktop.autostart.status_failed", "error", err)
	}
	menu.AddCheckbox("Start at login", enabled).OnClick(func(*application.Context) {
		application.InvokeAsync(func() { s.report(setAutostart(!enabled)); s.menu() })
	})
	menu.AddCheckbox("Start hidden at login", s.settings.StartHidden).SetEnabled(s.canHide).OnClick(func(*application.Context) {
		application.InvokeAsync(func() {
			candidate := s.settings
			candidate.StartHidden = !candidate.StartHidden
			if err := saveSettings(s.directory, candidate); err != nil {
				s.report(err)
			} else {
				s.settings = candidate
			}
			s.menu()
		})
	})
	if !s.canHide {
		menu.Add("System tray unavailable: window stays accessible").SetEnabled(false)
	}
	menu.AddSeparator()
	menu.Add("Quit Q Studio (stop owned background work)").OnClick(func(*application.Context) { go s.app.Quit() })
	s.tray.SetMenu(menu)
	s.appMenu = menu
	menubar := application.NewMenu()
	menubar.AddSubmenu("Q Studio").Append(menu)
	s.app.Menu.SetApplicationMenu(menubar)
	s.updateStatus()
}

func (s *desktopShell) updateStatus() {
	label := "Engine reconnecting…"
	if endpoint, ok := s.host.endpoint(); ok {
		label = "Engine running · background tasks active"
		if !endpoint.Owned {
			label = "Connected to existing Q engine"
		}
	}
	if s.status != nil {
		s.status.SetLabel(label)
	}
	if s.appMenu != nil {
		s.appMenu.Update()
	}
}
