package app

import "github.com/snowmerak/q/browsercontrol"

// Read only the browser preference at turn submission. Existing sessions keep
// their selected model and provider thread while global browser settings change.
func (m model) browserPreference() string {
	value := m.config.BrowserControl
	if m.store.Dir != "" {
		if latest, err := m.store.Load(); err == nil {
			value = latest.BrowserControl
		}
	}
	return browsercontrol.Prompt(value)
}
