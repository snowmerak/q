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
	"time"
)

const ChildCommand = "__loom_worker"

type workerRequest struct {
	WorkspaceRoot string      `json:"workspace_root"`
	Eval          EvalRequest `json:"eval"`
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
	requestBody, err := json.Marshal(workerRequest{WorkspaceRoot: store.WorkspaceRoot(), Eval: request})
	if err != nil {
		return EvalResult{}, fmt.Errorf("loom: encode worker request: %w", err)
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
	decoder := json.NewDecoder(io.LimitReader(input, maximumScriptBytes+(1<<20)))
	decoder.DisallowUnknownFields()
	var request workerRequest
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("loom: decode worker request: %w", err)
	}
	store, err := Open(request.WorkspaceRoot)
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

func writeWorkerResponse(output io.Writer, response workerResponse) error {
	if err := json.NewEncoder(output).Encode(response); err != nil {
		return fmt.Errorf("loom: encode worker response: %w", err)
	}
	return nil
}
