package main

import (
	"testing"

	"github.com/lehigh-university-libraries/hOCRedit/internal/providers"
)

func TestProviderService(t *testing.T) {
	service := providers.NewService()

	t.Run("ListProviders", func(t *testing.T) {
		providerList := service.ListProviders()
		if len(providerList) == 0 {
			t.Error("Expected at least one provider, got none")
		}
	})

	t.Run("ProviderAvailability", func(t *testing.T) {
		testProviders := []string{"openai", "azure", "gemini", "ollama"}
		for _, provider := range testProviders {
			t.Run(provider, func(t *testing.T) {
				if service.HasProvider(provider) {
					model := service.GetDefaultModel(provider)
					if model == "" {
						t.Errorf("Provider %s is available but has no default model", provider)
					}
					t.Logf("Provider %s available with default model: %s", provider, model)
				} else {
					t.Logf("Provider %s is not available", provider)
				}
			})
		}
	})
}
