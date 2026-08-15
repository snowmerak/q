package app

import (
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
)

func (m model) configureEmbeddingRuntime(value config.Config, configuredClient chatClient) tea.Cmd {
	archive := m.archiveSearch
	libraryClient := m.libraryClient
	if archive == nil && libraryClient == nil {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		if value.Embedding.Model == "" {
			var archiveErr, libraryErr error
			if archive != nil {
				archiveErr = archive.Disable()
			}
			if libraryClient != nil {
				libraryErr = libraryClient.ConfigureEmbedding(nil, "", 0)
			}
			return archiveEmbeddingConfiguredMsg{err: errors.Join(archiveErr, libraryErr)}
		}
		embedder, ok := configuredClient.(qlibrary.Embedder)
		if !ok {
			return archiveEmbeddingConfiguredMsg{err: errors.New("configured LLM client does not support embeddings")}
		}
		if libraryClient != nil {
			if err := libraryClient.ConfigureEmbedding(
				embedder, value.Embedding.Model, value.Embedding.Dimensions,
			); err != nil {
				return archiveEmbeddingConfiguredMsg{err: err}
			}
		}
		if archive == nil {
			return archiveEmbeddingConfiguredMsg{}
		}
		if err := archive.Configure(embedder, value.Embedding.Model, value.Embedding.Dimensions); err != nil {
			return archiveEmbeddingConfiguredMsg{err: err}
		}
		stats, err := archive.Backfill(ctx)
		return archiveEmbeddingConfiguredMsg{stats: stats, err: err}
	}
}
