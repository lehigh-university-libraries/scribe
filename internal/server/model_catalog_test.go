package server

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

func TestGetModelCatalogAdvertisesTemperaturePerTranscriptionModel(t *testing.T) {
	previous := config.Get()
	configured := previous
	configured.Config.LLM.Gemini.Model = "gemini-3.5-flash"
	configured.Config.LLM.Gemini.Models = []string{"gemini-3.5-flash", "gemini-2.5-flash"}
	config.Init(configured)
	t.Cleanup(func() { config.Init(previous) })

	response, err := (&Handler{}).GetModelCatalog(
		context.Background(),
		connect.NewRequest(&scribev1.GetModelCatalogRequest{}),
	)
	if err != nil {
		t.Fatalf("GetModelCatalog() error = %v", err)
	}

	want := map[string]bool{
		"gemini-3.5-flash": false,
		"gemini-2.5-flash": true,
	}
	for _, provider := range response.Msg.GetTranscriptionProviders() {
		if provider.GetId() != "gemini" {
			continue
		}
		for _, model := range provider.GetModels() {
			expected, ok := want[model.GetId()]
			if !ok {
				continue
			}
			if model.GetSupportsTemperature() != expected {
				t.Errorf("model %q supports_temperature = %t, want %t", model.GetId(), model.GetSupportsTemperature(), expected)
			}
			delete(want, model.GetId())
		}
	}
	if len(want) != 0 {
		t.Fatalf("GetModelCatalog() omitted Gemini models: %v", want)
	}
}
