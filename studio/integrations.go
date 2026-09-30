package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/mcpconfig"
)

const maximumIntegrationRequestSize = 1 << 20

type integrationService struct {
	mu      *sync.Mutex
	main    config.Store
	mcp     mcpconfig.Store
	runtime embeddingRuntime
}

func newIntegrationService(store config.Store, runner sessionRunner, shared ...*sync.Mutex) *integrationService {
	mutex := &sync.Mutex{}
	if len(shared) > 0 && shared[0] != nil {
		mutex = shared[0]
	}
	service := &integrationService{main: store, mcp: mcpconfig.Store{Dir: store.Dir}, mu: mutex}
	if runtime, ok := runner.(embeddingRuntime); ok {
		service.runtime = runtime
	}
	return service
}

func optionalCanonicalWorkspaceDirectory(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return canonicalWorkspaceDirectory(value)
}

func revision(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func decodeIntegrationRequest(writer http.ResponseWriter, request *http.Request, output any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maximumIntegrationRequestSize)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode integration settings: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode integration settings: multiple JSON values")
		}
		return fmt.Errorf("decode integration settings: %w", err)
	}
	return nil
}
