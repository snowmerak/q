package loom

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
)

const (
	maximumScriptBytes  = 256 << 10
	maximumScriptInput  = 32 << 20
	maximumScriptOutput = 64 << 20
	defaultEvalTimeout  = 2 * time.Second
)

type EvalRequest struct {
	Code   string         `json:"code"`
	Inputs map[string]Ref `json:"inputs,omitempty"`
}

type EvalResult struct {
	Artifact Artifact        `json:"artifact"`
	Value    json.RawMessage `json:"value,omitempty"`
}

type Evaluator interface {
	Evaluate(context.Context, *Store, EvalRequest) (EvalResult, error)
}

type InProcessEvaluator struct{}

func (InProcessEvaluator) Evaluate(ctx context.Context, store *Store, request EvalRequest) (EvalResult, error) {
	if len(request.Code) == 0 {
		return EvalResult{}, errors.New("loom: script code is required")
	}
	if len(request.Code) > maximumScriptBytes {
		return EvalResult{}, fmt.Errorf("loom: script exceeds %d bytes", maximumScriptBytes)
	}
	parents := make([]Ref, 0, len(request.Inputs))
	inputValues := make(map[string]string, len(request.Inputs))
	names := make([]string, 0, len(request.Inputs))
	for name := range request.Inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ref := request.Inputs[name]
		if strings.TrimSpace(name) == "" {
			return EvalResult{}, errors.New("loom: script input name is required")
		}
		parsed, err := ParseRef(ref.String())
		if err != nil {
			return EvalResult{}, err
		}
		if _, err := store.Inspect(ctx, parsed); err != nil {
			return EvalResult{}, fmt.Errorf("loom: inspect input %q: %w", name, err)
		}
		parents = append(parents, parsed)
		inputValues[name] = parsed.String()
	}

	deadline := defaultEvalTimeout
	if value, ok := ctx.Deadline(); ok {
		deadline = min(deadline, max(time.Duration(0), time.Until(value)))
	}
	evalCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	vm := goja.New()
	if err := vm.Set("inputs", inputValues); err != nil {
		return EvalResult{}, fmt.Errorf("loom: expose script inputs: %w", err)
	}
	if err := installAPI(evalCtx, vm, store); err != nil {
		return EvalResult{}, err
	}

	finished := make(chan struct{})
	go func() {
		select {
		case <-evalCtx.Done():
			vm.Interrupt(evalCtx.Err())
		case <-finished:
		}
	}()
	wrapped := "(function() {\n\"use strict\";\n" + request.Code + "\n})()"
	value, err := vm.RunString(wrapped)
	close(finished)
	if err != nil {
		if interrupted, ok := err.(*goja.InterruptedError); ok {
			return EvalResult{}, fmt.Errorf("loom: script interrupted: %v", interrupted.Value())
		}
		return EvalResult{}, fmt.Errorf("loom: execute script: %w", err)
	}
	if goja.IsUndefined(value) {
		return EvalResult{}, errors.New("loom: script must return a JSON value")
	}
	body, err := json.Marshal(value.Export())
	if err != nil {
		return EvalResult{}, fmt.Errorf("loom: encode script result: %w", err)
	}
	if len(body) > maximumScriptOutput {
		return EvalResult{}, fmt.Errorf("loom: script result exceeds %d bytes", maximumScriptOutput)
	}
	scriptDigest := sha256.Sum256([]byte(request.Code))
	artifact, err := store.Put(evalCtx, body, PutOptions{
		Kind: "derived", MediaType: "application/json", Parents: parents,
		Source: map[string]string{"language": "javascript", "script_digest": hex.EncodeToString(scriptDigest[:])},
	})
	if err != nil {
		return EvalResult{}, err
	}
	return EvalResult{Artifact: artifact, Value: body}, nil
}

func installAPI(ctx context.Context, vm *goja.Runtime, store *Store) error {
	api := vm.NewObject()
	if err := api.Set("inspect", func(reference string) (Artifact, error) {
		ref, err := ParseRef(reference)
		if err != nil {
			return Artifact{}, err
		}
		return store.Inspect(ctx, ref)
	}); err != nil {
		return fmt.Errorf("loom: expose inspect: %w", err)
	}
	if err := api.Set("read", func(reference string, offset, limit int64) (map[string]any, error) {
		ref, err := ParseRef(reference)
		if err != nil {
			return nil, err
		}
		result, err := store.Read(ctx, ref, offset, limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"content": string(result.Content), "offset": result.Offset,
			"nextOffset": result.NextOffset, "more": result.More,
		}, nil
	}); err != nil {
		return fmt.Errorf("loom: expose read: %w", err)
	}
	if err := api.Set("get", func(reference string) (any, error) {
		return readJSON(ctx, store, reference, "")
	}); err != nil {
		return fmt.Errorf("loom: expose get: %w", err)
	}
	if err := api.Set("json", func(reference, pointer string) (any, error) {
		return readJSON(ctx, store, reference, pointer)
	}); err != nil {
		return fmt.Errorf("loom: expose json: %w", err)
	}
	if err := vm.Set("loom", api); err != nil {
		return fmt.Errorf("loom: expose API: %w", err)
	}
	return nil
}

func readJSON(ctx context.Context, store *Store, reference, pointer string) (any, error) {
	ref, err := ParseRef(reference)
	if err != nil {
		return nil, err
	}
	body, err := store.ReadAll(ctx, ref, maximumScriptInput)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("loom: artifact is not JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("loom: artifact contains multiple JSON values")
		}
		return nil, fmt.Errorf("loom: decode trailing artifact content: %w", err)
	}
	if pointer == "" {
		return value, nil
	}
	return selectPointer(value, pointer)
}

func selectPointer(value any, pointer string) (any, error) {
	if pointer == "" {
		return value, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("loom: JSON pointer must be empty or start with /")
	}
	current := value
	for _, encoded := range strings.Split(pointer[1:], "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		switch typed := current.(type) {
		case map[string]any:
			next, ok := typed[part]
			if !ok {
				return nil, fmt.Errorf("loom: JSON pointer %q does not exist", pointer)
			}
			current = next
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, fmt.Errorf("loom: JSON pointer %q has invalid array index %q", pointer, part)
			}
			current = typed[index]
		default:
			return nil, fmt.Errorf("loom: JSON pointer %q traverses a scalar", pointer)
		}
	}
	return current, nil
}
