package mlow

import "testing"

// TestSynth is the synthesis KAT placeholder. Synth has no standalone unit vector:
// it is validated end-to-end (e2e_vectors.json) once the full decode pipeline is
// wired in module #15 decoder. Skipped until the synth bodies land and #15 drives
// real frames through them.
func TestSynth(t *testing.T) {
	t.Skip("blocked: synth is validated end-to-end via module #15 decoder (e2e_vectors.json); no standalone unit KAT")
}
