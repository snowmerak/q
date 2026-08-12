package lsp

import (
	"os/exec"
	"strings"
)

// DiscoveredServer is an installed, known language-server executable. Path is
// evidence from the current PATH; Config intentionally retains the portable
// command name for the global configuration draft.
type DiscoveredServer struct {
	ID     string
	Path   string
	Config ServerConfig
}

type serverCandidate struct {
	id        string
	command   string
	args      []string
	languages []string
}

var knownServerCandidates = []serverCandidate{
	{id: "gopls", command: "gopls", args: []string{"serve"}, languages: []string{"go"}},
	{id: "rust-analyzer", command: "rust-analyzer", languages: []string{"rust"}},
	{id: "typescript", command: "typescript-language-server", args: []string{"--stdio"}, languages: []string{"typescript", "javascript"}},
	{id: "basedpyright", command: "basedpyright-langserver", args: []string{"--stdio"}, languages: []string{"python"}},
	{id: "pyright", command: "pyright-langserver", args: []string{"--stdio"}, languages: []string{"python"}},
	{id: "pylsp", command: "pylsp", languages: []string{"python"}},
}

// DiscoverServers checks PATH for known language servers. An empty language
// list checks every supported candidate. Alternative servers for one language
// are ordered by preference, so only the first installed alternative is used.
func DiscoverServers(languages []string) []DiscoveredServer {
	return discoverServers(languages, exec.LookPath)
}

func discoverServers(languages []string, lookPath func(string) (string, error)) []DiscoveredServer {
	wanted := make(map[string]struct{})
	for _, language := range languages {
		language = strings.ToLower(strings.TrimSpace(language))
		if language != "" {
			wanted[language] = struct{}{}
		}
	}
	all := len(wanted) == 0
	resolved := make(map[string]struct{})
	var result []DiscoveredServer
	for _, candidate := range knownServerCandidates {
		matched := false
		for _, language := range candidate.languages {
			_, requested := wanted[language]
			_, alreadyResolved := resolved[language]
			if (all || requested) && !alreadyResolved {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		path, err := lookPath(candidate.command)
		if err != nil || strings.TrimSpace(path) == "" {
			continue
		}
		result = append(result, DiscoveredServer{
			ID: candidate.id, Path: path,
			Config: ServerConfig{
				Languages: append([]string(nil), candidate.languages...),
				Command:   candidate.command, Args: append([]string(nil), candidate.args...),
			},
		})
		for _, language := range candidate.languages {
			if all {
				resolved[language] = struct{}{}
				continue
			}
			if _, requested := wanted[language]; requested {
				resolved[language] = struct{}{}
			}
		}
	}
	return result
}
