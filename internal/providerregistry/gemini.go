package providerregistry

import "strings"

// geminiModelUsesDefaultSampling identifies Gemini models that no longer
// accept caller-controlled sampling parameters. Gemini 3.x is tuned around
// its model default and rejects or degrades requests that retain the legacy
// temperature field.
func geminiModelUsesDefaultSampling(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "gemini-3-") || strings.HasPrefix(model, "gemini-3.")
}

func providerModelSupportsTemperature(descriptor Provider, model string) bool {
	if !descriptor.Capabilities.Temperature {
		return false
	}
	return descriptor.ID != "gemini" || !geminiModelUsesDefaultSampling(model)
}
