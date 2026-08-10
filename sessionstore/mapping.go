package sessionstore

import (
	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
)

const MappingVersion = 3

func indexMapping() *mapping.IndexMappingImpl {
	index := bleve.NewIndexMapping()
	document := bleve.NewDocumentStaticMapping()

	for _, field := range []string{
		"id", "kind", "run_id", "task_id", "parent_id", "role", "model",
		"effort", "status", "scope", "refs", "tags",
	} {
		value := bleve.NewKeywordFieldMapping()
		value.Name = field
		value.Store = false
		value.DocValues = true
		document.AddFieldMappingsAt(field, value)
	}
	for _, field := range []string{"summary", "content", "search_text"} {
		value := bleve.NewTextFieldMapping()
		value.Name = field
		value.Store = false
		value.IncludeInAll = false
		document.AddFieldMappingsAt(field, value)
	}
	for _, field := range []string{"created_at", "updated_at"} {
		value := bleve.NewDateTimeFieldMapping()
		value.Name = field
		value.Store = false
		value.DocValues = true
		document.AddFieldMappingsAt(field, value)
	}

	index.DefaultMapping = document
	index.DefaultField = "content"
	index.IndexDynamic = false
	index.StoreDynamic = false
	index.DocValuesDynamic = false
	return index
}
