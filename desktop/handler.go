package main

import "net/http"

// No Go services or privileged frontend bridge are bound. All native actions
// live in the OS menu; generated preview frames receive only the existing web
// API's sandboxed view, never a shell execution or filesystem binding.
func (s *desktopShell) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.host.ServeHTTP(w, r)
}
