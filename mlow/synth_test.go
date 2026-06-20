package mlow

import (
	"encoding/json"
	"os"
	"testing"
)

// TestSmplReconstructNLSF verifies the decoder NLSF reconstruction against the C
// qlsf: quantize each lsf_quant_io.json record (LsfQuant/LsfQuantCond), feed the
// resulting grid/stage2 + threaded prevNLSF to SmplReconstructNLSF, and require the
// result to match the captured qlsf. Mirrors the reference decoder_reconstructs_c_qlsf
// (rec 3 is a near-silence ill-conditioned frame, excluded as in the reference).
func TestSmplReconstructNLSF(t *testing.T) {
	raw, err := os.ReadFile("testdata/lsf_quant_io.json")
	if err != nil {
		t.Fatalf("read lsf_quant_io.json: %v", err)
	}
	var recs []struct {
		Lsf      []float32 `json:"lsf"`
		A        []float32 `json:"A"`
		Voiced   int       `json:"voiced"`
		LowRate  int       `json:"lowRate"`
		Surv     int       `json:"surv"`
		RDwAdj   float32   `json:"RDw_adj"`
		CondCode int       `json:"cond_coding"`
		PrevLsf  []float32 `json:"prev_lsf"`
		Qlsf     []float32 `json:"qlsf"`
	}
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatalf("parse lsf_quant_io.json: %v", err)
	}

	st := LoadSmplSynthTables()
	var prevNLSF []float32
	var worst float32
	for n, r := range recs {
		var res LsfQuantResult
		if r.CondCode != 0 {
			res = LsfQuantCond(r.A, r.Lsf, r.PrevLsf, r.Voiced, r.LowRate, r.RDwAdj, r.Surv)
		} else {
			res = LsfQuant(r.A, r.Lsf, r.Voiced, r.LowRate, r.RDwAdj, r.Surv)
		}
		grid := int(res.Qi[0])
		var stage2 [16]int32
		copy(stage2[:], res.Qi[1:17])
		rec := SmplReconstructNLSF(st, r.Voiced, 0, grid, &stage2, prevNLSF)

		var rd float32
		for k := 0; k < SmplOrder; k++ {
			d := rec[k] - r.Qlsf[k]
			if d < 0 {
				d = -d
			}
			if d > rd {
				rd = d
			}
		}
		if n != 3 && rd >= 1e-3 {
			t.Errorf("rec %d cond=%d grid=%d: reconstruct vs qlsf %.2e", n, r.CondCode, grid, rd)
		}
		if rd > worst {
			worst = rd
		}
		prevNLSF = rec
	}
	if worst >= 2e-3 {
		t.Errorf("worst reconstruct vs C qlsf %.3e", worst)
	}
}

// TestSynth is the full-synthesis KAT placeholder. The frame-synthesis bodies
// (SynthInternalFrame, CelpDecState.SynthFrame, etc.) have no standalone unit
// vector — they are validated end-to-end (e2e_vectors.json) by module #15 decoder.
func TestSynth(t *testing.T) {
	t.Skip("blocked: full-frame synth is validated end-to-end via module #15 decoder (e2e_vectors.json); no standalone unit KAT")
}
