package studio

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/snowmerak/q/internal/fsreplace"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/usagelog"
	"github.com/snowmerak/q/workspacememory"
)

type transferWrite struct {
	path string
	save func() error
}

type transferBackup struct {
	path   string
	data   []byte
	mode   os.FileMode
	exists bool
}

func (service *settingsService) saveTransfer(request *http.Request, state transferState, bundle settingsBundle) error {
	var writes []transferWrite
	if transferTouchesMain(bundle) {
		writes = append(writes, transferWrite{service.main.Path(), func() error { return service.main.Save(state.main) }})
	}
	if len(bundle.Sections["providers"]) > 0 {
		writes = append(writes, transferWrite{service.providers.Path(), func() error { return service.saveProviderConfig(request, state.providers) }})
	}
	if _, ok := bundle.Sections["services"]["gateway"]; ok {
		writes = append(writes, transferWrite{service.gateway.Path(), func() error { return service.gateway.Save(state.gateway) }})
	}
	if _, ok := bundle.Sections["services"]["system-one"]; ok || len(bundle.Sections["system-one"]) > 0 {
		writes = append(writes, transferWrite{service.systemOne.Path(), func() error { return service.systemOne.Save(state.systemOne) }})
	}
	if _, ok := bundle.Sections["services"]["library"]; ok {
		writes = append(writes, transferWrite{service.library.Path(), func() error { return service.library.Save(state.library) }})
	}
	if _, ok := bundle.Sections["services"]["workspace-memory"]; ok {
		store := workspacememory.ConfigStore{Dir: service.main.Dir}
		writes = append(writes, transferWrite{store.Path(), func() error { return store.Save(state.memory) }})
	}
	if _, ok := bundle.Sections["services"]["usage"]; ok {
		store := usagelog.ConfigStore{Dir: service.main.Dir}
		writes = append(writes, transferWrite{store.Path(), func() error { return store.Save(state.usage) }})
	}
	for id := range bundle.Sections["integrations"] {
		if strings.HasPrefix(id, "mcp-") {
			writes = append(writes, transferWrite{service.mcp.Path(), func() error { return service.mcp.Save(state.mcp) }})
			break
		}
	}
	profiles := profileStore(service.main, "")
	for _, id := range slices.Sorted(maps.Keys(bundle.Sections["subagents"])) {
		if name, ok := strings.CutPrefix(id, "profile/"); ok {
			entry := state.profiles[name]
			var original *subagent.ProfileEntry
			path := filepath.Join(profiles.Global, name+".yaml")
			if entry.Path != "" {
				original = &entry
				path = entry.Path
			}
			writes = append(writes, transferWrite{path, func() error { return profiles.Save(entry.Profile, "global", original) }})
		}
	}
	backups := make([]transferBackup, 0, len(writes))
	for _, write := range writes {
		backup := transferBackup{path: write.path, mode: 0600}
		info, err := os.Stat(write.path)
		if err == nil {
			backup.exists, backup.mode = true, info.Mode().Perm()
			backup.data, err = os.ReadFile(write.path)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		backups = append(backups, backup)
	}
	previousProviders, err := service.loadProviderConfig()
	if err != nil {
		return err
	}
	rollback := func(cause error, count int) error {
		var errs []error
		for _, write := range writes[:count] {
			if write.path == service.providers.Path() && service.runtime != nil {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 30*time.Second)
				errs = append(errs, service.runtime.ApplyGateway(ctx, previousProviders))
				cancel()
				break
			}
		}
		for index := count - 1; index >= 0; index-- {
			errs = append(errs, backups[index].restore())
		}
		return errors.Join(fmt.Errorf("import failed; restoring previous settings: %w", cause), errors.Join(errs...))
	}
	for index, write := range writes {
		if err := write.save(); err != nil {
			return rollback(err, index+1)
		}
	}
	return nil
}

func (backup transferBackup) restore() error {
	if !backup.exists {
		err := os.Remove(backup.path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(backup.path), ".transfer-rollback-*")
	if err != nil {
		return err
	}
	path := file.Name()
	defer func() { _ = os.Remove(path) }()
	if err := file.Chmod(backup.mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(backup.data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return fsreplace.Replace(path, backup.path)
}
