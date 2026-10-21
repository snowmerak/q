package config

import (
	"math"
	"testing"
)

func TestModelSamplingRoundTripPreservesZeroAndOmission(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	value := Default()
	value.Provider.Model = "local/model"
	value.ModelSampling = map[string]SamplingConfig{"local/model": {Temperature: new(float64(0)), TopK: new(0), RepetitionPenalty: new(float64(1))}}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	sampling := loaded.ModelSampling["local/model"]
	if sampling.Temperature == nil || *sampling.Temperature != 0 || sampling.TopK == nil || *sampling.TopK != 0 || sampling.TopP != nil {
		t.Fatalf("sampling = %#v", sampling)
	}
}

func TestModelSamplingRejectsInvalidSettings(t *testing.T) {
	for name, sampling := range map[string]SamplingConfig{
		"temperature":        {Temperature: new(-0.1)},
		"top_p":              {TopP: new(1.1)},
		"top_k":              {TopK: new(-1)},
		"min_p":              {MinP: new(-0.1)},
		"repetition_penalty": {RepetitionPenalty: new(float64(0))},
		"presence_penalty":   {PresencePenalty: new(2.1)},
		"frequency_penalty":  {FrequencyPenalty: new(-2.1)},
		"nan":                {Temperature: new(math.NaN())},
		"infinity":           {Temperature: new(math.Inf(1))},
	} {
		t.Run(name, func(t *testing.T) {
			value := Default()
			value.Provider.Model = "local/model"
			value.ModelSampling = map[string]SamplingConfig{"local/model": sampling}
			if err := value.Validate(); err == nil {
				t.Fatal("accepted invalid sampling")
			}
		})
	}
	for _, model := range []string{"", " local/model", "group/main"} {
		value := Default()
		value.Provider.Model = "local/model"
		value.ModelSampling = map[string]SamplingConfig{model: {}}
		if err := value.Validate(); err == nil {
			t.Fatalf("accepted model %q", model)
		}
	}
}
