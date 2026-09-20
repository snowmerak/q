// Package agentloop runs q's provider and tool loop without starting the TUI,
// managed provider host, workspace services, or session persistence.
//
// A Run executes one turn synchronously. The caller owns the model client,
// tool runtime, durable history, event persistence, and the lifetime of every
// injected dependency. The same engine is used by q's interactive, ACP, and
// remote hosts.
package agentloop
