// Package qdriver manages task-owned Chromium tabs over an existing CDP
// connection. Attaching never launches a browser, and disconnecting never
// terminates the external browser or closes borrowed tabs.
// Event subscriptions are armed before triggering actions and wait independently
// of the command gate. File paths require a filesystem shared with Chrome;
// enabling downloads explicitly changes the default browser context's policy.
package qdriver
