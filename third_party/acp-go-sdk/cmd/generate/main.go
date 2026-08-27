package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"

	"github.com/snowmerak/q/third_party/acp-go-sdk/cmd/generate/internal/emit"
	"github.com/snowmerak/q/third_party/acp-go-sdk/cmd/generate/internal/load"
)

var generatedFiles = []string{"constants_gen.go", "types_gen.go", "agent_gen.go", "client_gen.go", "helpers_gen.go"}

func main() {
	var schemaDirFlag string
	var outDirFlag string
	var check bool
	flag.StringVar(&schemaDirFlag, "schema", "", "path to schema directory (defaults to <repo>/schema)")
	flag.StringVar(&outDirFlag, "out", "", "output directory for generated go files (defaults to <repo>)")
	flag.BoolVar(&check, "check", false, "verify checked-in bindings without changing them")
	flag.Parse()

	repoRoot := findRepoRoot()
	schemaDir := schemaDirFlag
	outDir := outDirFlag
	if schemaDir == "" {
		schemaDir = filepath.Join(repoRoot, "schema")
	}
	if outDir == "" {
		outDir = repoRoot
	}
	if check {
		tempDir, err := os.MkdirTemp("", "q-acp-generate-")
		if err != nil {
			panic(err)
		}
		defer os.RemoveAll(tempDir)
		generate(schemaDir, tempDir)
		for _, name := range generatedFiles {
			actual, err := os.ReadFile(filepath.Join(outDir, name))
			if err != nil {
				panic(err)
			}
			expected, err := os.ReadFile(filepath.Join(tempDir, name))
			if err != nil {
				panic(err)
			}
			if !bytes.Equal(bytes.ReplaceAll(actual, []byte("\r\n"), []byte("\n")), expected) {
				panic(fmt.Sprintf("%s is stale; regenerate the ACP bindings", name))
			}
		}
		return
	}
	generate(schemaDir, outDir)
}

func generate(schemaDir, outDir string) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		panic(err)
	}

	meta, err := load.ReadMeta(schemaDir)
	if err != nil {
		panic(err)
	}

	schema, err := load.ReadSchema(schemaDir)
	if err != nil {
		panic(err)
	}

	unstableMeta, unstableMetaFound, err := load.ReadMetaUnstable(schemaDir)
	if err != nil {
		panic(err)
	}
	unstableSchema, unstableSchemaFound, err := load.ReadSchemaUnstable(schemaDir)
	if err != nil {
		panic(err)
	}
	if unstableMetaFound != unstableSchemaFound {
		panic(fmt.Sprintf("unstable schema/meta mismatch: meta found=%v schema found=%v", unstableMetaFound, unstableSchemaFound))
	}
	if unstableMetaFound {
		if err := load.ApplyForkOverlay(schemaDir, unstableSchema); err != nil {
			panic(err)
		}
		mergedMeta, mergedSchema, err := load.MergeStableAndUnstable(meta, schema, unstableMeta, unstableSchema)
		if err != nil {
			panic(err)
		}
		meta = mergedMeta
		schema = mergedSchema
	} else {
		panic("Q's elicitation overlay requires the pinned unstable schema")
	}

	if err := emit.WriteConstantsJen(outDir, meta); err != nil {
		panic(err)
	}

	if err := emit.WriteTypesJen(outDir, schema, meta); err != nil {
		panic(err)
	}
	if err := emit.WriteDispatchJen(outDir, schema, meta); err != nil {
		panic(err)
	}

	// Emit helpers after types so they can reference generated structs.
	if err := emit.WriteHelpersJen(outDir, schema, meta); err != nil {
		panic(err)
	}
	for _, name := range generatedFiles {
		path := filepath.Join(outDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			panic(err)
		}
		formatted, err := format.Source(data)
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(path, formatted, 0o644); err != nil {
			panic(err)
		}
	}
}

func findRepoRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	dir := cwd
	for {
		if hasSchema(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic(fmt.Sprintf("could not locate repository root from %q; ensure schema files exist", cwd))
		}
		dir = parent
	}
}

func hasSchema(dir string) bool {
	if dir == "" {
		return false
	}
	metaPath := filepath.Join(dir, "schema", "meta.json")
	schemaPath := filepath.Join(dir, "schema", "schema.json")
	return fileExists(metaPath) && fileExists(schemaPath)
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	if _, err := os.Stat(path); err == nil {
		return true
	}
	return false
}
