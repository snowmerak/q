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
				if libraryErr == nil {
					_, libraryErr = libraryClient.SyncSkillEmbeddings(ctx)
				}
			}
			return archiveEmbeddingConfiguredMsg{err: errors.Join(archiveErr, libraryErr)}
		}
		embedder, ok := configuredClient.(qlibrary.Embedder)
		if !ok {
			return archiveEmbeddingConfiguredMsg{err: errors.New("configured LLM client does not support embeddings")}
		}
		var libraryStats qlibrary.SkillEmbeddingSyncStats
		var libraryErr error
		if libraryClient != nil {
			if err := libraryClient.ConfigureEmbedding(
				embedder, value.Embedding.Model, value.Embedding.Dimensions,
			); err != nil {
				libraryErr = err
			} else {
				libraryStats, libraryErr = libraryClient.SyncSkillEmbeddings(ctx)
			}
		}
		if archive == nil {
			return archiveEmbeddingConfiguredMsg{globalSkills: libraryStats.Embedded, err: libraryErr}
		}
		if err := archive.Configure(embedder, value.Embedding.Model, value.Embedding.Dimensions); err != nil {
			return archiveEmbeddingConfiguredMsg{globalSkills: libraryStats.Embedded, err: errors.Join(libraryErr, err)}
		}
		stats, err := archive.Backfill(ctx)
		return archiveEmbeddingConfiguredMsg{
			stats: stats, globalSkills: libraryStats.Embedded, err: errors.Join(libraryErr, err),
		}
	}
}
