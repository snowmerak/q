package studio

import (
	"crypto/sha256"
	"encoding/hex"
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

func decodeIntegrationRequest(writer http.ResponseWriter, request *http.Request, target any) error {
	return decodeRequest(writer, request, target, maximumIntegrationRequestSize, "decode integration settings")
}
