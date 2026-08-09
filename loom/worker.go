package loom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"time"
)

const ChildCommand = "__loom_worker"

const maximumWorkerRequestBytes = maximumScriptBytes + (16 << 20)

type workerRequest struct {
	WorkspaceRoot string             `json:"workspace_root"`
	Eval          EvalRequest        `json:"eval"`
	Store         workerStoreOptions `json:"store"`
	Roots         []Ref              `json:"roots,omitempty"`
}

type workerStoreOptions struct {
	MaximumArtifactBytes int64         `json:"maximum_artifact_bytes"`
	MaximumStoreBytes    int64         `json:"maximum_store_bytes"`
	AutoGC               bool          `json:"auto_gc"`
	GCTriggerRatio       float64       `json:"gc_trigger_ratio"`
	GCTargetRatio        float64       `json:"gc_target_ratio"`
	GCGracePeriod        time.Duration `json:"gc_grace_period"`
}

type workerResponse struct {
	Result EvalResult `json:"result"`
	Error  string     `json:"error,omitempty"`
}

type ProcessEvaluator struct {
	Executable string
	Timeout    time.Duration
}

func NewProcessEvaluator() ProcessEvaluator {
	return ProcessEvaluator{Timeout: defaultEvalTimeout + time.Second}
}

func (e ProcessEvaluator) Evaluate(ctx context.Context, store *Store, request EvalRequest) (EvalResult, error) {
	executable := e.Executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return EvalResult{}, fmt.Errorf("loom: find worker executable: %w", err)
		}
	}
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = defaultEvalTimeout + time.Second
	}
	workerCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	workerRequest, err := prepareWorkerRequest(workerCtx, store, request)
	if err != nil {
		return EvalResult{}, err
	}
	requestBody, err := json.Marshal(workerRequest)
	if err != nil {
		return EvalResult{}, fmt.Errorf("loom: encode worker request: %w", err)
	}
	if len(requestBody) > maximumWorkerRequestBytes {
		return EvalResult{}, fmt.Errorf("loom: worker request exceeds %d bytes", maximumWorkerRequestBytes)
	}
	command := exec.CommandContext(workerCtx, executable, ChildCommand)
	command.Env = []string{}
	command.Stdin = bytes.NewReader(requestBody)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if workerCtx.Err() != nil {
			return EvalResult{}, fmt.Errorf("loom: script worker stopped: %w", workerCtx.Err())
		}
		if stderr.Len() > 0 {
			return EvalResult{}, fmt.Errorf("loom: script worker failed: %s", stderr.String())
		}
		return EvalResult{}, fmt.Errorf("loom: script worker failed: %w", err)
	}
	var response workerResponse
	decoder := json.NewDecoder(io.LimitReader(&stdout, maximumScriptOutput+(1<<20)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return EvalResult{}, fmt.Errorf("loom: decode worker response: %w", err)
	}
	if response.Error != "" {
		return EvalResult{}, errors.New(response.Error)
	}
	return response.Result, nil
}

func RunChild(ctx context.Context, input io.Reader, output io.Writer) error {
	decoder := json.NewDecoder(io.LimitReader(input, maximumWorkerRequestBytes+1))
	decoder.DisallowUnknownFields()
	var request workerRequest
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("loom: decode worker request: %w", err)
	}
	store, err := OpenWithOptions(request.WorkspaceRoot, request.storeOptions())
	if err != nil {
		return writeWorkerResponse(output, workerResponse{Error: err.Error()})
	}
	result, evalErr := (InProcessEvaluator{}).Evaluate(ctx, store, request.Eval)
	response := workerResponse{Result: result}
	if evalErr != nil {
		response.Error = evalErr.Error()
	}
	return writeWorkerResponse(output, response)
}

func prepareWorkerRequest(ctx context.Context, store *Store, eval EvalRequest) (workerRequest, error) {
	options := store.Options()
	request := workerRequest{
		WorkspaceRoot: store.WorkspaceRoot(), Eval: eval,
		Store: workerStoreOptions{
			MaximumArtifactBytes: options.MaximumArtifactBytes,
			MaximumStoreBytes:    options.MaximumStoreBytes,
			AutoGC:               options.AutoGC, GCTriggerRatio: options.GCTriggerRatio,
			GCTargetRatio: options.GCTargetRatio, GCGracePeriod: options.GCGracePeriod,
		},
	}
	if !options.AutoGC {
		return request, nil
	}
	seen := make(map[Ref]struct{})
	for _, ref := range eval.Inputs {
		if parsed, err := ParseRef(ref.String()); err == nil {
			seen[parsed] = struct{}{}
		}
	}
	if options.Roots != nil {
		roots, err := options.Roots(ctx)
		if err != nil {
			return workerRequest{}, fmt.Errorf("loom: collect worker GC roots: %w", err)
		}
		for _, ref := range roots {
			if parsed, err := ParseRef(ref.String()); err == nil {
				seen[parsed] = struct{}{}
			}
		}
	}
	request.Roots = make([]Ref, 0, len(seen))
	for ref := range seen {
		request.Roots = append(request.Roots, ref)
	}
	sort.Slice(request.Roots, func(i, j int) bool { return request.Roots[i] < request.Roots[j] })
	return request, nil
}

func (request workerRequest) storeOptions() StoreOptions {
	options := StoreOptions{
		MaximumArtifactBytes: request.Store.MaximumArtifactBytes,
		MaximumStoreBytes:    request.Store.MaximumStoreBytes,
		AutoGC:               request.Store.AutoGC, GCTriggerRatio: request.Store.GCTriggerRatio,
		GCTargetRatio: request.Store.GCTargetRatio, GCGracePeriod: request.Store.GCGracePeriod,
	}
	if options.AutoGC {
		roots := append([]Ref(nil), request.Roots...)
		options.Roots = func(context.Context) ([]Ref, error) {
			return append([]Ref(nil), roots...), nil
		}
	}
	return options
}

func writeWorkerResponse(output io.Writer, response workerResponse) error {
	if err := json.NewEncoder(output).Encode(response); err != nil {
		return fmt.Errorf("loom: encode worker response: %w", err)
	}
	return nil
}
