package systemone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"

	"github.com/snowmerak/decisions"
)

func (c *Client) listDecisionModels(ctx context.Context) (*ModelsResult, error) {
	models, err := c.decisionModels.ListModels(ctx)
	if err != nil {
		return nil, decisionAPIError(err, nil)
	}
	result := &ModelsResult{Models: make([]Model, 0, len(models))}
	for _, model := range models {
		if !c.allowsModel(model.ID) {
			continue
		}
		if c.decisionConfig.API == decisions.GeminiDecisionAPI && model.SupportedMethods != nil && !slices.Contains(model.SupportedMethods, "createDecision") {
			continue
		}
		var metadata struct {
			ReleaseDate string `json:"release_date"`
		}
		if err := json.Unmarshal(model.Raw, &metadata); err != nil {
			return nil, fmt.Errorf("systemone: decode model release date: %w", err)
		}
		result.Models = append(result.Models, Model{Name: model.ID, Description: model.Description, ReleaseDate: metadata.ReleaseDate})
	}
	result.Raw, err = json.Marshal(result)
	return result, err
}

func (c *Client) evaluateDecision(ctx context.Context, body json.RawMessage, options CallOptions) (*Result, error) {
	var wire struct {
		Model     string                      `json:"model"`
		State     json.RawMessage             `json:"state"`
		Questions map[string]decisionQuestion `json:"questions"`
		Assets    map[string]struct {
			Type       string `json:"type"`
			Format     string `json:"format"`
			DataBase64 string `json:"data_base64"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("%w: invalid System One request fields: %v", decisions.ErrInvalidRequest, err)
	}
	request := decisions.Request{
		Input:     decisions.Input{State: decisionState(wire.State), Assets: make(map[string]decisions.ImageAsset, len(wire.Assets))},
		Questions: make(decisions.Questions, len(wire.Questions)),
	}
	for name, asset := range wire.Assets {
		if asset.Type != "image" {
			return nil, fmt.Errorf("%w: only image assets are supported", decisions.ErrUnsupported)
		}
		request.Input.Assets[name] = decisions.ImageAsset{Format: asset.Format, DataBase64: asset.DataBase64}
	}
	for name, question := range wire.Questions {
		translated, err := question.translate()
		if err != nil {
			return nil, fmt.Errorf("question %q: %w", name, err)
		}
		request.Questions[name] = translated
	}
	transport := c.httpClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	roundTrip := &decisionTransport{upstream: transport, idempotencyKey: options.IdempotencyKey}
	httpClient := *c.httpClient
	httpClient.Transport = roundTrip
	config := c.decisionConfig
	config.Model, config.HTTPClient = wire.Model, &httpClient
	client, err := decisions.NewClient(config)
	if err != nil {
		return nil, err
	}
	response, err := client.Evaluate(ctx, request)
	if err != nil {
		return nil, decisionAPIError(err, roundTrip.header)
	}
	return decisionResult(response, roundTrip.header)
}

func decisionState(raw json.RawMessage) any {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "null"
	}
	switch raw[0] {
	case '"', '{', '[':
		return raw
	default:
		return string(raw)
	}
}

type decisionQuestion struct {
	Type         QuestionType    `json:"type"`
	Instructions json.RawMessage `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria"`
}

func (question decisionQuestion) translate() (decisions.Question, error) {
	instructions := decisionText(question.Instructions)
	switch question.Type {
	case QuestionNoul:
		var criteria *decisions.NoulCriteria
		if len(question.Criteria) > 0 {
			if err := json.Unmarshal(question.Criteria, &criteria); err != nil {
				return nil, fmt.Errorf("%w: noul criteria must name true and false descriptions", decisions.ErrInvalidRequest)
			}
		}
		return decisions.NoulQuestion{Instructions: instructions, Criteria: criteria}, nil
	case QuestionChoice:
		var criteria map[string]json.RawMessage
		if err := json.Unmarshal(question.Criteria, &criteria); err != nil {
			return nil, fmt.Errorf("%w: choice criteria must map names to descriptions", decisions.ErrInvalidRequest)
		}
		choices := make([]decisions.Choice, 0, len(criteria))
		for _, value := range slices.Sorted(maps.Keys(criteria)) {
			choices = append(choices, decisions.Choice{Value: value, Description: decisionText(criteria[value])})
		}
		return decisions.ChoicesQuestion{Instructions: instructions, Choices: choices}, nil
	case QuestionScore:
		var criteria []json.RawMessage
		if err := json.Unmarshal(question.Criteria, &criteria); err != nil {
			return nil, fmt.Errorf("%w: score criteria must be ordered levels", decisions.ErrInvalidRequest)
		}
		levels := make([]decisions.Level, 0, len(criteria))
		for _, value := range criteria {
			var level decisions.Level
			if err := json.Unmarshal(value, &level.Label); err != nil {
				if err := json.Unmarshal(value, &level); err != nil {
					return nil, fmt.Errorf("%w: score levels must be labels or objects with a label", decisions.ErrInvalidRequest)
				}
			}
			levels = append(levels, level)
		}
		return decisions.ScoreQuestion{Instructions: instructions, Levels: levels}, nil
	default:
		return nil, fmt.Errorf("%w: question type %q", decisions.ErrUnsupported, question.Type)
	}
}

func decisionText(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

func decisionResult(response *decisions.Response, header http.Header) (*Result, error) {
	result := &Result{
		Model:   response.Model,
		Answers: make(map[string]Answer, len(response.Answers)),
		Usage:   &Usage{InputTokens: &response.Usage.InputTokens, OutputTokens: &response.Usage.OutputTokens},
		Header:  header,
	}
	for name, answer := range response.Answers {
		switch value := answer.(type) {
		case decisions.NoulAnswer:
			result.Answers[name] = Answer{Type: QuestionNoul, Noul: &value.Probability, Confidence: value.Confidence}
		case decisions.ChoiceAnswer:
			result.Answers[name] = Answer{Type: QuestionChoice, Choice: value.Choice, Confidence: &value.Confidence, Probabilities: value.Probabilities}
		case decisions.ScoreAnswer:
			probabilities := make(map[string]float64, len(value.Probabilities))
			legend := make(map[string]string, len(value.Legend))
			for index, probability := range value.Probabilities {
				probabilities[fmt.Sprint(index)] = probability
			}
			for index, level := range value.Legend {
				legend[fmt.Sprint(index)] = level.Label
			}
			result.Answers[name] = Answer{Type: QuestionScore, Score: &value.Score, Confidence: &value.Confidence, Probabilities: probabilities, Legend: legend}
		case decisions.RefusalAnswer:
			result.Answers[name] = Answer{Type: "refusal"}
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	// Keep each answer's Raw JSON available to the same callers as native results.
	if err := json.Unmarshal(data, result); err != nil {
		return nil, err
	}
	result.Raw = data
	return result, nil
}

type decisionTransport struct {
	upstream       http.RoundTripper
	idempotencyKey string
	header         http.Header
}

func (transport *decisionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport.idempotencyKey != "" {
		request = request.Clone(request.Context())
		request.Header.Set("Idempotency-Key", transport.idempotencyKey)
		request.GetBody = nil
	}
	response, err := transport.upstream.RoundTrip(request)
	if response != nil {
		transport.header = response.Header.Clone()
	}
	return response, err
}

func decisionAPIError(err error, header http.Header) error {
	var response *decisions.APIError
	if !errors.As(err, &response) {
		return err
	}
	return &APIError{
		StatusCode: response.StatusCode, RequestID: response.RequestID, Code: header.Get("X-System-One-Error-Code"),
		Source: header.Get("X-System-One-Error-Source"), RetryAfter: header.Get("Retry-After"), Header: header, Body: response.Body,
	}
}
