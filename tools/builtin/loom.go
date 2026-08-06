package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/snowmerak/q/loom"
)

const maximumInlineLoomValue = 32 << 10

type LoomRuntime struct {
	Store     *loom.Store
	Evaluator loom.Evaluator
}

type LoomInspectInput struct {
	Ref string `json:"ref" jsonschema:"Loom artifact reference"`
}

type LoomInspectOutput struct {
	Artifact loom.Artifact `json:"artifact"`
}

type LoomReadInput struct {
	Ref    string `json:"ref" jsonschema:"Loom artifact reference"`
	Offset int64  `json:"offset,omitempty" jsonschema:"Byte offset, starting at zero"`
	Limit  int64  `json:"limit,omitempty" jsonschema:"Maximum bytes to return; defaults to 65536 and cannot exceed 1048576"`
}

type LoomReadOutput struct {
	Ref        loom.Ref `json:"ref"`
	Content    string   `json:"content"`
	Encoding   string   `json:"encoding"`
	Offset     int64    `json:"offset"`
	NextOffset int64    `json:"next_offset"`
	More       bool     `json:"more"`
}

type LoomEvalInput struct {
	Code   string            `json:"code" jsonschema:"JavaScript function body; must return a JSON-serializable value"`
	Inputs map[string]string `json:"inputs,omitempty" jsonschema:"Names mapped to Loom artifact references exposed as the inputs object"`
}

type LoomEvalOutput struct {
	Artifact loom.Artifact `json:"artifact"`
	Value    any           `json:"value,omitempty"`
	Stored   bool          `json:"stored"`
}

func (runtime LoomRuntime) Inspect(ctx context.Context, input LoomInspectInput) (LoomInspectOutput, error) {
	ref, err := loom.ParseRef(input.Ref)
	if err != nil {
		return LoomInspectOutput{}, err
	}
	artifact, err := runtime.Store.Inspect(ctx, ref)
	if err != nil {
		return LoomInspectOutput{}, err
	}
	return LoomInspectOutput{Artifact: artifact}, nil
}

func (runtime LoomRuntime) Read(ctx context.Context, input LoomReadInput) (LoomReadOutput, error) {
	ref, err := loom.ParseRef(input.Ref)
	if err != nil {
		return LoomReadOutput{}, err
	}
	result, err := runtime.Store.Read(ctx, ref, input.Offset, input.Limit)
	if err != nil {
		return LoomReadOutput{}, err
	}
	content := string(result.Content)
	encoding := "utf-8"
	if !utf8.Valid(result.Content) {
		content = base64.StdEncoding.EncodeToString(result.Content)
		encoding = "base64"
	}
	return LoomReadOutput{
		Ref: ref, Content: content, Encoding: encoding, Offset: result.Offset,
		NextOffset: result.NextOffset, More: result.More,
	}, nil
}

func (runtime LoomRuntime) Eval(ctx context.Context, input LoomEvalInput) (LoomEvalOutput, error) {
	if runtime.Evaluator == nil {
		return LoomEvalOutput{}, fmt.Errorf("loom: JavaScript evaluator is unavailable")
	}
	refs := make(map[string]loom.Ref, len(input.Inputs))
	for name, value := range input.Inputs {
		ref, err := loom.ParseRef(value)
		if err != nil {
			return LoomEvalOutput{}, fmt.Errorf("loom: input %q: %w", name, err)
		}
		refs[name] = ref
	}
	result, err := runtime.Evaluator.Evaluate(ctx, runtime.Store, loom.EvalRequest{Code: input.Code, Inputs: refs})
	if err != nil {
		return LoomEvalOutput{}, err
	}
	output := LoomEvalOutput{Artifact: result.Artifact, Stored: true}
	if len(result.Value) <= maximumInlineLoomValue {
		if err := json.Unmarshal(result.Value, &output.Value); err != nil {
			return LoomEvalOutput{}, fmt.Errorf("loom: decode script result: %w", err)
		}
	}
	return output, nil
}
