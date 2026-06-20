package mlow

import "testing"

// TestGenNoise is the noise-generator KAT placeholder (gennoise_vectors.json — the
// instrumented-C noise-generator vector, already in testdata). All bodies are stubs,
// so this is skipped — enable it when SmplCelpGenNoise and friends are implemented.
func TestGenNoise(t *testing.T) {
	t.Skip("blocked: noise generator bodies are stubs; enable against gennoise_vectors.json when implemented")
}
