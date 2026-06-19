# Datasheet: `mlow/encoder`

The stateful encoder: turns one 60 ms PCM frame into a wire MLow frame — analysis
(PCM → per-frame parameters, including the voiced/unvoiced classifier) followed by
the entropy encoder (parameters → range-coded bitstream), the exact inverse of the
decoder. Media layer; the outbound counterpart of `mlow/decoder`.

**Validation vector:** `sigmode_ground_truth.json` — per-frame
`pitchcorr`/`avg_lag`/`harm`/`lags`/`F2`/`sp_act_prob`, with the C reference
`voicing_strength` and voiced decision, threaded through one classifier state in
stream order. Copy it verbatim into `mlow/testdata/`.

## Reference source (verbatim — authoritative)

### `encode.rs`

```rust
//! MLow ENTROPY ENCODER: the exact inverse of the byte-exact decoder. Given the analyzed
//! `SmplFrameParams`, it reproduces the same range-coder symbol stream the decoder consumes, against
//! the same config=0 runtime tables in the same field order. Ported from the Go reference
//! (`smpl_encode*.go`). Targets the active config=0 path (0x50 frames), p3=4, p4=1. Internal frames
//! are voiced (LTP pitch block) or unvoiced (gains block) per the analysis; both are byte-exact with
//! the Go reference.

use super::analysis::{SmplEncoderState, smpl_analyze_frame_st};
use super::params::{
    SmplFrameParams, SmplGainParams, SmplLsfParams, SmplPitchParams, SmplPulseParams,
};
use super::rangecoder::RangeEncoder;
use super::smpl_decode::{SmplLsfState, SmplTables, load_smpl_tables};
use super::smpl_mem::{SmplMem, load_smpl_mem};
use super::smpl_pulse::mem8_static;

const SMPL_ENCODE_BUF_BYTES: usize = 512;
const OPUS_FRAME_SAMPS: usize = 960; // 60 ms @ 16 kHz

/// Stateful pure-Rust MLow encoder: 60 ms PCM (960 f32 @16 kHz, ~[-1,1]) → a wire MLow frame the
/// WhatsApp peer decodes. Emits active config=0 (`0x50`) frames, choosing voiced (LTP) or unvoiced
/// per internal frame via analysis-by-synthesis — byte-exact with the Go reference full encoder.
pub struct MlowEncoder {
    state: SmplEncoderState,
}

impl Default for MlowEncoder {
    fn default() -> Self {
        Self::new()
    }
}

impl MlowEncoder {
    pub fn new() -> Self {
        MlowEncoder {
            state: SmplEncoderState::default(),
        }
    }

    /// Clear the cross-frame analysis history (call at a stream discontinuity).
    pub fn reset(&mut self) {
        self.state = SmplEncoderState::default();
    }

    /// Encode one 60 ms frame. Expects exactly 960 samples.
    pub fn encode(&mut self, pcm: &[f32]) -> Result<Vec<u8>, &'static str> {
        if pcm.len() != OPUS_FRAME_SAMPS {
            return Err("mlow encode: expected 960 samples (60 ms @16 kHz)");
        }
        // Sanitize: NaN → 0, clamp to [-1,1] (the LPC analysis degenerates on non-finite input).
        let clean: Vec<f32> = pcm
            .iter()
            .map(|&s| if s.is_nan() { 0.0 } else { s.clamp(-1.0, 1.0) })
            .collect();
        let fp = smpl_analyze_frame_st(&mut self.state, &clean);
        encode_smpl_frame(&fp)
    }
}

/// Encode one 60 ms MLow frame from its parameters → `[TOC || range-coded body]`.
pub(crate) fn encode_smpl_frame(fp: &SmplFrameParams) -> Result<Vec<u8>, &'static str> {
    let (p2, p3, p4) = (320i32, 4i32, 1i32);
    let p6 = fp.config as i32;
    let tbl = load_smpl_tables();
    let mem = load_smpl_mem();
    let mut enc = RangeEncoder::new(1 + SMPL_ENCODE_BUF_BYTES);
    let mut st = SmplLsfState::default();
    for f in 0..3 {
        let ip = &fp.internal[f];
        encode_smpl_lsf(&mut enc, tbl, &mut st, fp.config, f, &ip.lsf);
        encode_smpl_pulses(&mut enc, mem, p2, p3, p4, p6, ip.lsf.stage1, &ip.pulses);
        // Voiced internal frames emit a pitch block; unvoiced emit a gains block (never both).
        if ip.lsf.stage1 == 1 {
            encode_smpl_pitch(
                &mut enc,
                mem,
                &mut st,
                p2,
                p3,
                p6,
                ip.pulses.subfr,
                &ip.pitch,
            );
        } else {
            encode_smpl_gains(&mut enc, mem, p3, ip.pulses.subfr, &ip.gains);
        }
    }
    enc.done();
    if enc.err() != 0 {
        return Err("mlow encode: range-encoder buffer overflow");
    }
    let n = enc.consumed_len();
    let body = enc.bytes();
    let mut out = Vec::with_capacity(1 + n);
    out.push(fp.toc);
    out.extend_from_slice(&body[..n]);
    Ok(out)
}

/// Inverse of `decode_smpl_lsf`: mirror the selector/grid/16-residual/extra reads, mutating `st`.
fn encode_smpl_lsf(
    enc: &mut RangeEncoder,
    t: &SmplTables,
    st: &mut SmplLsfState,
    config: usize,
    intf: usize,
    lsf: &SmplLsfParams,
) {
    let sel = if intf == 0 {
        0
    } else if st.prev_stage1 != 0 {
        2
    } else {
        1
    };
    let stage1 = lsf.stage1;
    enc.encode_cdf(stage1, &t.lsf_sel[sel]);

    let enter_match = intf != 0;
    let m = enter_match && (stage1 == st.prev_stage1);
    if !m {
        st.prev_gain_idx = -1;
        st.prev_filt_idx = -1;
        st.prev_lag = -1;
        st.prev_frac_lag = -1;
        st.prev_lagblk = -1;
        st.prev_lagidx = -1;
    }
    st.prev_stage1 = stage1;

    let grid_cdf: &[u16] = if m {
        if stage1 != 0 {
            &t.lsf_grid.match1
        } else {
            &t.lsf_grid.match1_alt
        }
    } else if stage1 != 0 {
        &t.lsf_grid.match0_alt
    } else {
        &t.lsf_grid.match0
    };
    enc.encode_cdf(lsf.grid, grid_cdf);
    st.prev_match = m;
    st.have_prev = true;

    let st2 = &t.lsf_stage2[stage1 as usize][config][lsf.grid as usize];
    for (k, c) in st2.iter().enumerate().take(16) {
        enc.encode_cdf(lsf.stage2[k], c);
    }
    enc.encode_cdf(lsf.extra, &t.lsf_extra);
}

/// Inverse of `decode_smpl_pulses` (config=0 NB count, p3=4): re-derive the count interval and the
/// split symbols from the per-subframe counts, then replay the recorded magnitude/sign symbols.
#[allow(clippy::too_many_arguments)]
fn encode_smpl_pulses(
    enc: &mut RangeEncoder,
    mem: &SmplMem,
    p2: i32,
    p3: i32,
    p4: i32,
    p6: i32,
    s1: i32,
    pp: &SmplPulseParams,
) {
    let g_cc = mem.g_cc;
    let idx = p4 + s1;
    let b_byte = mem8_static(0xe8990u32.wrapping_add((p6 * 3 + idx) as u32)) as i32;
    let frame_len4k = b_byte * p2 / 320;
    let subfr_len16 = frame_len4k / p3;
    let total = pp.total;

    // --- pulse COUNT (NB triangular; config=0) ---
    let l = frame_len4k as u32;
    let tri_t = |k: u32| -> u32 {
        let a = k.wrapping_add(2).wrapping_mul(l.wrapping_add(1));
        let b = k.wrapping_sub(1).wrapping_mul(k.wrapping_add(131070)) >> 1;
        a.wrapping_sub(b) & 0xffff
    };
    let mut ft = tri_t(l);
    if ft == 0 {
        ft = 1;
    }
    let fl = if total > 0 {
        tri_t((total - 1) as u32)
    } else {
        0
    };
    let fh = tri_t(total as u32);
    enc.encode(fl, fh, ft);

    if total == 0 {
        return;
    }

    // --- recursive binary SPLIT ---
    let final_sum = pp.subfr[0] + pp.subfr[1];
    let init_sum = (total - subfr_len16 * 2).max(0);
    let lo = (total - 80).max(0);
    if init_sum < lo {
        return;
    }
    let hi_bound = total - lo;
    if init_sum < hi_bound {
        let cdfp = mem.u32(g_cc.wrapping_add((total as u32) * 8).wrapping_add(0xcd0));
        let off = cdfp
            .wrapping_add((init_sum as u32) * 2)
            .wrapping_sub((lo as u32) * 2);
        let n = ((hi_bound - init_sum) + 2) as usize;
        let cdf = mem.cdf_at(off, n);
        enc.encode_cdf(final_sum - init_sum, &cdf);
    }
    if final_sum > 0 {
        encode_split_3537(
            enc,
            mem,
            final_sum,
            subfr_len16,
            g_cc.wrapping_add(0xcd8),
            pp.subfr[0],
        );
    }
    if final_sum < total {
        encode_split_3537(
            enc,
            mem,
            total - final_sum,
            subfr_len16,
            g_cc.wrapping_add(0xcd8),
            pp.subfr[2],
        );
    }

    // --- MAGNITUDE block: replay recorded run-length symbols through the same loop ---
    let pos_per = p2 / p3;
    let mut mag_idx = 0usize;
    for subfr in 0..p3 {
        let cnt = pp.subfr[subfr as usize];
        if cnt <= 0 {
            continue;
        }
        let mut pos = pos_per;
        let mut c = cnt;
        let mut k = 0;
        while k < cnt {
            let oct = (pos + 7) / 8;
            let mag_base = g_cc.wrapping_add((oct as u32) * 0xa4);
            let c_base_off = mem.u32(mag_base) as i64;
            let cdfp = mem.u32(
                mag_base
                    .wrapping_add(((c - 1) as u32) * 4)
                    .wrapping_sub(0xa0),
            );
            let off = cdfp.wrapping_add(((c_base_off - pos as i64) * 2) as u32);
            let m = pp.mag_runs[mag_idx];
            mag_idx += 1;
            let cdf = mem.cdf_at(off, (pos + 1) as usize);
            enc.encode_cdf(m, &cdf);
            if m > 0 || k == 0 {
                pos -= m;
            }
            c -= 1;
            k += 1;
        }
    }

    // --- SIGN block: replay recorded raw sign symbols ---
    for rs in &pp.sign_syms {
        enc.encode_raw_symbol(rs.sym, rs.nbits);
    }
}

/// Inverse of `smpl_split_3537`: encode the first-half count `s0` against the same CDF.
fn encode_split_3537(
    enc: &mut RangeEncoder,
    mem: &SmplMem,
    count: i32,
    granularity: i32,
    base: u32,
    s0: i32,
) {
    let lo = count.min(granularity);
    let min_split = (count - granularity).max(0);
    if lo < min_split || min_split == lo {
        return;
    }
    let cdfp = mem.u32(base.wrapping_add((count as u32) * 8).wrapping_sub(8));
    let off = cdfp.wrapping_add((min_split as u32) * 2);
    let n = ((lo - min_split) + 2) as usize;
    let cdf = mem.cdf_at(off, n);
    enc.encode_cdf(s0 - min_split, &cdf);
}

/// Inverse of `decode_smpl_gains`: encode main/delta gain, then per-subframe nrgres with the same
/// gain-derived address shift.
fn encode_smpl_gains(
    enc: &mut RangeEncoder,
    mem: &SmplMem,
    p3: i32,
    subfr_counts: [i32; 4],
    gp: &SmplGainParams,
) {
    let g_nrg = mem.g_nrg;
    let gain_main = gp.gain_main;
    let gain_delta = gp.gain_delta;
    enc.encode_cdf(gain_main, &mem.cdf_at(g_nrg.wrapping_add(0x1362), 85));
    enc.encode_cdf(gain_delta, &mem.cdf_at(g_nrg.wrapping_add(0x1098), 99));
    let cfg_sel = 2i32;

    let gain_tab_addr: u32 = if p3 == 4 { 0xf35f0 } else { 0xf3970 };
    let off6 = p3 * gain_delta;
    let base7 =
        gain_main * (mem.i16(0xf35e0u32.wrapping_add((cfg_sel as u32) * 2)) as i32) - 0x154000;
    let mut gain_q = [0i32; 4];
    for (sf, gq) in gain_q.iter_mut().enumerate().take(p3 as usize) {
        let cbv = mem.i16(gain_tab_addr.wrapping_add(((sf as i32 + off6) as u32) * 2)) as i32;
        *gq = base7 + (cbv << 4);
    }

    let nrg_base = g_nrg.wrapping_add((cfg_sel as u32) * 0x588);
    for (sf, &cnt) in subfr_counts.iter().enumerate().take(p3 as usize) {
        if cnt <= 0 {
            continue;
        }
        let bucket = if cnt >= 30 { 3 } else { (cnt & 0xffff) / 10 };
        let cdfp = nrg_base.wrapping_add((bucket as u32) * 0x162);
        let mut g = (gain_q[sf] + 8192) >> 14;
        if g < -85 {
            g = -85;
        }
        let neg_part = (g >> 31) & g;
        let off = cdfp.wrapping_sub((neg_part << 1) as u32);
        enc.encode_cdf(gp.nrg_res[sf], &mem.cdf_at(off, 92));
    }
}

/// Inverse of `decode_smpl_pitch`: encode the LTP gains, primary lag (abs/delta), optional 64-fine,
/// and the per-segment fractional symbols, mutating the predictor state identically.
#[allow(clippy::too_many_arguments)]
fn encode_smpl_pitch(
    enc: &mut RangeEncoder,
    mem: &SmplMem,
    st: &mut SmplLsfState,
    _p2: i32,
    p3: i32,
    p6: i32,
    subfr_counts: [i32; 4],
    pp: &SmplPitchParams,
) {
    let gp = mem.g_pitch;
    let weight_tab: u32 = if p6 != 0 { 0xe8460 } else { 0xe85b0 };
    let gain_cdf_base = if p6 != 0 { gp + 0xc0 } else { gp + 0x302 };
    let filt_cdf0 = gp + 0xdc4;
    let filt_cdf1 = gp + 0xe4c;

    let mut gain_accum: i32 = 0;
    for (sf, &cnt) in subfr_counts.iter().enumerate().take((p3 as usize).min(4)) {
        let row = gain_cdf_base
            .wrapping_add(st.prev_gain_idx.wrapping_mul(0x22) as u32)
            .wrapping_add(0x22);
        let gi = pp.gain_idx[sf];
        enc.encode_cdf(gi, &mem.cdf_at(row, 17));
        st.prev_gain_idx = gi;
        let w0 = mem.i16(weight_tab.wrapping_add((gi as u32) * 4)) as i32;
        let w2 = mem.i16(weight_tab.wrapping_add((gi as u32) * 4 + 2)) as i32;
        gain_accum += w0 + 2 * w2;
        if cnt > 0 {
            let fi = pp.filt_idx[sf];
            if st.prev_filt_idx == -1 {
                enc.encode_cdf(fi, &mem.cdf_at(filt_cdf0, 35));
            } else {
                enc.encode_cdf(
                    fi,
                    &mem.cdf_at(filt_cdf1.wrapping_sub((st.prev_filt_idx as u32) * 2), 35),
                );
            }
            st.prev_filt_idx = fi;
        }
    }
    let avg_gain = gain_accum / p3;

    // Lag block: write the estimator's chosen contour (`blockseg_idx`) + per-40-block lag indices
    // (`laginds`) via the C `smpl_encode_lags`, NOT the prior single-lag contour-map flattening. The
    // delta-lag CMF `mode` mirrors the C mean-ACB-gain thresholds (`smpl_pitch_acbgain_thr_20_Q14`).
    let mode = if avg_gain < 10007 {
        0
    } else if avg_gain < 14085 {
        1
    } else {
        2
    };
    let tab = super::smpl_pitch_enc::load_pitch_tables();
    super::smpl_pitch_enc::smpl_encode_lags_wire(
        tab,
        enc,
        pp.blockseg_idx,
        &pp.laginds,
        st.prev_lagblk,
        st.prev_lagidx,
        mode,
    );
    let (nblk, nidx) =
        super::smpl_pitch_enc::smpl_lags_predictor_after(tab, pp.blockseg_idx, &pp.laginds);
    st.prev_lagblk = nblk;
    st.prev_lagidx = nidx;
}

#[cfg(test)]
mod tests {
    use super::super::decoder::MlowDecoder;
    use super::*;

    // Isolated voiced pitch-block round-trip: encode the gains + the estimator contour
    // (`blockseg_idx`/`laginds`) then decode them back; the decoder's `block_lags` must equal the
    // encoded `laginds`, proving the wire encode is the inverse of `decode_smpl_pitch`.
    #[test]
    fn pitch_block_round_trips_contour() {
        let mem = load_smpl_mem();
        let cases: &[(usize, [i32; 8], [i32; 4])] = &[
            (142, [128, 129, 129, 118, 118, 121, 121, 123], [5, 2, 2, 2]),
            (142, [128, 129, 129, 118, 118, 121, 121, 123], [5, 6, 2, 2]),
            (59, [123, 123, 123, 123, 128, 128, 132, 132], [2, 6, 6, 6]),
        ];
        for &(bsx, laginds, gains) in cases {
            let mut pp = SmplPitchParams {
                gain_idx: gains,
                filt_idx: [0; 4],
                blockseg_idx: bsx,
                laginds,
            };
            // subfr_counts > 0 so the filt_idx path fires (as in the real voiced encode).
            let subfr = [1i32; 4];
            for sf in 0..4 {
                pp.filt_idx[sf] = 0;
            }
            let mut enc = RangeEncoder::new(64);
            let mut est = SmplLsfState {
                prev_lag: -1,
                prev_frac_lag: -1,
                prev_lagblk: -1,
                prev_lagidx: -1,
                ..Default::default()
            };
            encode_smpl_pitch(&mut enc, mem, &mut est, 320, 4, 0, subfr, &pp);
            enc.done();
            let n = enc.consumed_len();
            let bytes = enc.bytes()[..n].to_vec();
            let mut dec = super::super::rangecoder::RangeDecoder::new(&bytes);
            let mut dst = SmplLsfState {
                prev_lag: -1,
                prev_frac_lag: -1,
                ..Default::default()
            };
            let pr = super::super::smpl_pitch::decode_smpl_pitch(
                &mut dec, mem, &mut dst, 320, 4, 0, subfr,
            );
            assert_eq!(
                pr.block_lags.to_vec(),
                laginds.to_vec(),
                "bsx={bsx}: decoded block_lags != encoded laginds"
            );
        }
    }

    fn corr(a: &[f32], b: &[f32]) -> f64 {
        let (mut sxy, mut sxx, mut syy) = (0f64, 0f64, 0f64);
        for i in 0..a.len() {
            let (x, y) = (a[i] as f64, b[i] as f64);
            sxy += x * y;
            sxx += x * x;
            syy += y * y;
        }
        if sxx < 1e-12 || syy < 1e-12 {
            return 0.0;
        }
        sxy / (sxx * syy).sqrt()
    }

    // Closed-loop: encode a tone and decode it back with the (byte-exact) decoder; the LB-core
    // reconstruction must track the input waveform shape (correlation). Proves the analysis →
    // entropy-encode → decode chain produces a frame that reconstructs the input audio.
    #[test]
    fn encode_round_trips_a_tone() {
        let mut enc = MlowEncoder::new();
        let mut dec = MlowDecoder::new();
        let mut best = 0f64;
        for f in 0..8 {
            let pcm: Vec<f32> = (0..960)
                .map(|i| {
                    let t = (f * 960 + i) as f64 / 16000.0;
                    (0.5 * (2.0 * std::f64::consts::PI * 550.0 * t).sin()) as f32
                })
                .collect();
            let frame = enc.encode(&pcm).expect("encode");
            assert_eq!(frame[0], 0x50, "active frame TOC");
            let out = dec.decode(&frame);
            // The decoder's harmonic postfilter adds 48 samples of group delay; align before correlating.
            const HARM_DELAY: usize = 48;
            best = best.max(corr(&pcm[..pcm.len() - HARM_DELAY], &out[HARM_DELAY..]));
        }
        assert!(
            best > 0.5,
            "encode→decode round-trip correlation too low: {best}"
        );
    }

    // Dev oracle: decode hex frames (MLOW_HEX) through `decode_smpl_pitch`, dumping each voiced frame's
    // reconstructed `block_lags` (the C `laginds` domain) to MLOW_PITCH_DUMP. Used to prove the WASM
    // decoder reconstructs C's `laginds` from C-encoded bytes (representation equivalence check).
    #[test]
    fn dump_decoded_pitch_from_hex() {
        let Ok(hexpath) = std::env::var("MLOW_HEX_PITCH") else {
            return;
        };
        let out = std::env::var("MLOW_PITCH_DUMP").expect("MLOW_PITCH_DUMP path");
        let text = std::fs::read_to_string(&hexpath).expect("read hex");
        let mem = load_smpl_mem();
        let tbl = load_smpl_tables();
        let mut recs: Vec<String> = Vec::new();
        for (pkt, line) in text.lines().enumerate() {
            let line = line.trim();
            if line.is_empty() {
                continue;
            }
            let frame: Vec<u8> = (0..line.len())
                .step_by(2)
                .map(|i| u8::from_str_radix(&line[i..i + 2], 16).expect("hex"))
                .collect();
            if frame.first() != Some(&0x50) {
                continue;
            }
            let config = (frame[0] >> 2) as usize & 1;
            let mut dec = super::super::rangecoder::RangeDecoder::new(&frame[1..]);
            let mut lstate = SmplLsfState::default();
            for f in 0..3 {
                let lsf = super::super::smpl_decode::decode_smpl_lsf(
                    &mut dec,
                    tbl,
                    &mut lstate,
                    config,
                    f,
                );
                let pulses = super::super::smpl_pulse::decode_smpl_pulses(
                    &mut dec,
                    mem,
                    320,
                    4,
                    1,
                    config as i32,
                    lsf.stage1,
                );
                if lsf.stage1 == 1 {
                    let pr = super::super::smpl_pitch::decode_smpl_pitch(
                        &mut dec,
                        mem,
                        &mut lstate,
                        320,
                        4,
                        config as i32,
                        pulses.subfr,
                    );
                    recs.push(format!(
                        "{{\"pkt\":{pkt},\"frame\":{f},\"voiced\":1,\"block_lags\":{:?}}}",
                        pr.block_lags.to_vec()
                    ));
                } else {
                    super::super::smpl_gains::decode_smpl_gains(&mut dec, mem, 4, pulses.subfr);
                    recs.push(format!("{{\"pkt\":{pkt},\"frame\":{f},\"voiced\":0}}"));
                }
            }
        }
        std::fs::write(&out, format!("[{}]", recs.join(","))).expect("write dump");
    }

    // Dev harness: encode an i16 mono 16 kHz raw file (env MLOW_MIC) into 60 ms MLow frames and write
    // them as hex (one per line) to MLOW_OUT, so the reference libopus useSmpl decoder (the peer's
    // algorithm) can round-trip them against the mic. Gated on env vars so the normal suite never
    // touches the (large, machine-local) mic clip.
    #[test]
    fn encode_mic_dump_hex() {
        let Ok(mic) = std::env::var("MLOW_MIC") else {
            return;
        };
        let out = std::env::var("MLOW_OUT").expect("MLOW_OUT path");
        let bytes = std::fs::read(&mic).expect("read mic");
        let samples: Vec<i16> = bytes
            .chunks_exact(2)
            .map(|c| i16::from_le_bytes([c[0], c[1]]))
            .collect();
        let mut enc = MlowEncoder::new();
        let mut lines = String::new();
        for chunk in samples.chunks(OPUS_FRAME_SAMPS) {
            if chunk.len() < OPUS_FRAME_SAMPS {
                break;
            }
            let pcm: Vec<f32> = chunk.iter().map(|&s| s as f32 / 32768.0).collect();
            let frame = enc.encode(&pcm).expect("encode");
            for b in &frame {
                lines.push_str(&format!("{b:02x}"));
            }
            lines.push('\n');
        }
        std::fs::write(&out, lines).expect("write frames");
    }

    // Dev harness: encode the mic (MLOW_MIC) and decode it back through OUR own MlowDecoder, writing
    // the reconstruction as i16 raw to MLOW_SELFDEC_OUT. Lets the codec round-trip be measured against
    // our decoder independently of the reference libopus useSmpl decoder.
    #[test]
    fn encode_mic_selfdecode_raw() {
        let Ok(mic) = std::env::var("MLOW_MIC") else {
            return;
        };
        let out = std::env::var("MLOW_SELFDEC_OUT").expect("MLOW_SELFDEC_OUT path");
        let bytes = std::fs::read(&mic).expect("read mic");
        let samples: Vec<i16> = bytes
            .chunks_exact(2)
            .map(|c| i16::from_le_bytes([c[0], c[1]]))
            .collect();
        let mut enc = MlowEncoder::new();
        let mut dec = MlowDecoder::new();
        let mut pcm_out: Vec<i16> = Vec::new();
        for chunk in samples.chunks(OPUS_FRAME_SAMPS) {
            if chunk.len() < OPUS_FRAME_SAMPS {
                break;
            }
            let pcm: Vec<f32> = chunk.iter().map(|&s| s as f32 / 32768.0).collect();
            let frame = enc.encode(&pcm).expect("encode");
            for s in dec.decode(&frame) {
                pcm_out.push((s * 32768.0).clamp(-32768.0, 32767.0) as i16);
            }
        }
        let mut buf = Vec::with_capacity(pcm_out.len() * 2);
        for s in &pcm_out {
            buf.extend_from_slice(&s.to_le_bytes());
        }
        std::fs::write(&out, buf).expect("write selfdec");
    }

    // Dev oracle: decode hex frames (MLOW_HEX, one per line) through OUR MlowDecoder, write i16 raw to
    // MLOW_DEC_OUT. Used to confirm our decoder reconstructs the reference C-encoded frames (the plan's
    // "C-enc -> our-dec" sanity check), isolating the decoder from the encoder.
    #[test]
    fn decode_hex_frames_raw() {
        let Ok(hexpath) = std::env::var("MLOW_HEX") else {
            return;
        };
        let out = std::env::var("MLOW_DEC_OUT").expect("MLOW_DEC_OUT path");
        let text = std::fs::read_to_string(&hexpath).expect("read hex");
        let mut dec = MlowDecoder::new();
        let mut pcm_out: Vec<i16> = Vec::new();
        for line in text.lines() {
            let line = line.trim();
            if line.is_empty() {
                continue;
            }
            let frame: Vec<u8> = (0..line.len())
                .step_by(2)
                .map(|i| u8::from_str_radix(&line[i..i + 2], 16).expect("hex"))
                .collect();
            for s in dec.decode(&frame) {
                pcm_out.push((s * 32768.0).clamp(-32768.0, 32767.0) as i16);
            }
        }
        let mut buf = Vec::with_capacity(pcm_out.len() * 2);
        for s in &pcm_out {
            buf.extend_from_slice(&s.to_le_bytes());
        }
        std::fs::write(&out, buf).expect("write dec");
    }
}
```

### `analysis.rs`

```rust
//! MLow ENCODER ANALYSIS: PCM → `SmplFrameParams`. The LPC front-end is the faithful C port
//! (`smpl_lpc`): per internal frame it windows the 20 ms `lpcbuf`, FFT-autocorrelates it, derives the
//! bandwidth-expanded `A` and its NLSF (`smpl_A2NLSF_16`), and feeds the bit-exact LSF quantizer
//! (`smpl_lsf_quant`, with the C conditional-coding path); the resulting `grid`/`stage2` map directly
//! onto the wire and the decoder reconstructs the same envelope. The excitation comes from the ported
//! CELP encoder (`smpl_celp` / `smpl_perc`) over the per-subframe interpolated LPC residual. The
//! UNVOICED level is the bit-exact `smpl_quant_nrg_res` floor (the wire gain block IS the nrgres
//! layout), with the per-subframe FCB gain index as `nrg_res`. The VOICED (LTP, stage1=1) path runs
//! the real CELP ACB/LTP encode: pitch comes from a perceptually-weighted (`w_speech`) search and the
//! `smpl_get_signal_mode` classifier; the CELP's `acb_idx`/`fcb_idx`/pulses drive the wire pitch block
//! (decoder-reconstructed lags feed the ACB basis so encode/decode LTP agree). Closed-loop:
//! decode(encode(analyze(pcm))) tracks the input.
#![allow(clippy::needless_range_loop)]

use super::params::{
    SmplGainParams, SmplInternalParams, SmplLsfParams, SmplPitchParams, SmplPulseParams, SmplRawSym,
};
use super::smpl_celp::{CelpEncoder, smpl_distribute_fcb_surv};
use super::smpl_decode::{SmplLsfState, smpl_advance_lsf_state};
use super::smpl_harmcomb::{smpl_filt_arma2, smpl_get_hp_coefs};
use super::smpl_lpc::{
    SMPL_F_LEN, SMPL_LPC_BUF_LEN, smpl_a2nlsf_16, smpl_lpc_analyze_with_f2, smpl_window_lpc20,
};
use super::smpl_lsf_quant::{lsf_quant, lsf_quant_cond};
use super::smpl_mem::{SmplMem, load_smpl_mem};
use super::smpl_perc::{
    BitrateController, BitrateControllerInputs, PercModelState, SMPL_PERC_EMPH_UV,
    SMPL_PERC_EMPH_V, SMPL_PERC_REG, smpl_perc_ac2a, smpl_perc_model,
};
use super::smpl_signal_mode::{VuvMode, smpl_get_signal_mode};
use super::smpl_synth::{
    SMPL_INTF_LEN, SMPL_ORDER, SMPL_SUBFR_COUNT, SMPL_SUBFR_LEN, SMPL_VOICED_NORM_GAIN,
    SmplFrameSynth, SmplPitchSynth, SmplSynthTables, load_smpl_synth_tables, smpl_gain_lin,
    smpl_nlsf2a, smpl_reconstruct_nlsf, synth_internal_frame,
};

/// HP-history samples to carry for the LPC window buffer. The C `lpcbuf` for internal frame 0 reaches
/// 96 samples before the current packet; carrying the full C `lpc_buf_mem` (144) is safe and exact.
const SMPL_LPC_HIST_LEN: usize = 144;
/// `lpcbuf` starts 96 samples before each internal frame (C: `-WINNEXT_WB_LEN + framelen + WINNEXT_WB_LONG_LEN - lpcbuf_len`).
const SMPL_LPC_PRE: usize = 96;
/// `surv = lsf_surv` for complexity 8 (`update_complexity_setting`).
const SMPL_LSF_SURV: usize = 6;
/// 2 ms analysis lookahead (`SMPL_WINNEXT_WB_LEN`); zero at 16 kHz (no band split).
const SMPL_WINNEXT_WB_LEN: usize = 32;
/// `RDw_adj = sqrt(mainBitRate / 14000)` for the HIGH-rate (lowRate=0) path at 20 kbps.
const SMPL_LSF_RDW_ADJ: f32 = 1.1952286;

/// Cross-frame analysis state: only the LPC-analysis input history persists (the decoder rebuilds
/// synthesis state per 60 ms frame).
#[derive(Default)]
pub(crate) struct SmplEncoderState {
    hist: Vec<f64>,
    /// Input high-pass (ARMA2, fcorner 35 Hz) coefficients + carried state, matching the real encoder.
    hp_coefs: Option<([f32; 3], [f32; 3])>,
    hp_state: [f32; 4],
    /// Persistent CELP excitation encoder (acb/zir/prev-idx state carries across subframes & frames).
    celp: Option<CelpEncoder>,
    /// Perceptual-weighting model state (FFT history) for the per-subframe `perc_wght_resp`.
    perc: Option<PercModelState>,
    /// Previous-pair perceptual autocorrelation, for the WB even-subframe interpolation.
    perc_prev: Vec<f32>,
    /// Bitrate controller (per-subframe pulse budget + importance), carried across frames.
    bitrate: Option<BitrateController>,
    /// HP-filtered input history (normalized, [-1,1]) for the LPC window buffer, mirroring the C
    /// `lpc_buf_mem`: the last `SMPL_LPC_HIST_LEN` HP samples of the previous packet.
    lpc_hist: Vec<f32>,
    /// Previous internal frame's committed (reconstructed) NLSF, for conditional LSF coding.
    prev_lsfq: Vec<f32>,
    /// Whether the previous internal frame was voiced (for the cond-coding condition).
    prev_voiced: bool,
    /// SILK VAD: per-internal-frame speech-activity probability + the coded_as_active_voice flag the
    /// bitrate controller and the voiced/unvoiced classifier read (smpl_vad.c).
    vad: Option<super::smpl_vad::SmplVadState>,
    /// Voicing-classifier hysteresis + spectral-tilt background tracker (`VUV_Mode`), per stream.
    vuv: super::smpl_signal_mode::VuvMode,
    /// Last `SMPL_PITCH_LAG_MAX` HP samples of the previous packet, so the first internal frame's
    /// pitch search has real history instead of zeros (the C `xhp_packet_buf` carries this).
    hp_pitch_hist: Vec<f32>,
    /// Persistent perceptually-weighted speech buffer (the C `ltp_buf`, length `MAX_LTP_BUF_LEN`),
    /// shifted left by one internal-frame each call. The full pitch estimator reads its tail.
    ltp_buf: Vec<f32>,
    /// Cross-frame pitch-estimator predictor (`PitchEstimator` non-scratch fields).
    pitch_est: super::smpl_pitch_enc::PitchEstState,
}

/// Assumed encoder bitrate for the active MLow 1:1 config (the recorded capture's main rate is not
/// known a priori; this drives the per-subframe pulse budget via the bitrate controller).
const SMPL_MAIN_BIT_RATE: i32 = 20000;
const SMPL_COMPLEXITY: i32 = 8;

const SMPL_CELP_LOW_RATE: bool = false;
const SMPL_CELP_PERC_RESP_LEN: usize = 32;
const SMPL_CELP_FCB_SUBFRLEN: usize = 80;
/// 12 subframes per 60 ms packet (4 subframes/internal frame x 3 internal frames).
const SMPL_CELP_SUBFR_PER_PACKET: usize = 12;
/// `perc_resp_len + SMPL_PERC_EMPH_V_LEN - 1` (= 33 = SMPL_MAX_L_RESP): the perceptual autocorrelation
/// length the perc model returns and `smpl_perc_ac2a` consumes.
const SMPL_PERC_R_LEN: usize = SMPL_CELP_PERC_RESP_LEN + 1;
/// `smpl_fcb_tot_surv_20ms_max` for complexity 5-8 (the perc_resp_len=32 path). Drives `tot_surv`.
const SMPL_FCB_TOT_SURV_20MS_MAX: i32 = 100;

/// Encoder input high-pass 3 dB corner (`SMPL_ENC_HP_FCORNER_3DB_HZ`).
const SMPL_ENC_HP_FCORNER_HZ: f32 = 35.0;

fn unvoiced_pitch() -> SmplPitchSynth {
    SmplPitchSynth {
        voiced: false,
        lag_subfr: [0.0; 4],
        norm_gain: 0.0,
    }
}

struct Candidate {
    ip: SmplInternalParams,
    stage1: i32,
    grid: i32,
    qsym: [i32; 16],
    pulse_vec: Vec<i32>,
    /// Per-subframe excitation gainQ used by the synthesis (rate-control gain for unvoiced, 0 for
    /// voiced). Must match what `commit_candidate` feeds the shadow synth (warm history).
    gain_q: [i32; 4],
    /// LTP parameters for the synthesis (`voiced=false` for unvoiced).
    pitch: SmplPitchSynth,
    silent: bool,
}

/// Borrowed CELP/perceptual state for one internal frame's excitation analysis.
struct CelpFrameCtx<'a> {
    celp: &'a mut CelpEncoder,
    perc: &'a mut PercModelState,
    perc_prev: &'a mut Vec<f32>,
    bitrate: &'a mut BitrateController,
    /// Full normalized HP frame (960 samples, [-1,1]); the perc model windows slices of it.
    hp_n: &'a [f32],
    /// Internal-frame index (0..3) within the 60 ms packet.
    intf: usize,
    /// SILK VAD speech-activity probability for this internal frame (bitrate controller input).
    sp_act_prob: f32,
    /// Packet-level coded_as_active_voice (BACKGROUND_NOISE frame_type + voiced gating).
    coded_as_active_voice: bool,
    /// LPC power spectrum `F2[0..256]` (the C `lpcbuf_F2`) for the voicing classifier's spectral tilt.
    f2: [f32; SMPL_F_LEN],
    /// This frame's classifier voicing_strength (the C `voicing_strength_buf[numframe]`), fed to the
    /// bitrate controller's importance/pulse-budget computation.
    voicing_strength: f32,
    /// Voicing-classifier hysteresis state, threaded across the whole stream.
    vuv: &'a mut VuvMode,
    /// Previous packet's HP tail (`SMPL_PITCH_LAG_MAX` samples) for the intf=0 pitch history.
    hp_pitch_hist: &'a [f32],
    /// Persistent perceptually-weighted speech buffer (the C `ltp_buf`), carried across frames; the
    /// full pitch estimator reads its tail.
    ltp_buf: &'a mut Vec<f32>,
    /// Cross-frame pitch-estimator predictor.
    pitch_est: &'a mut super::smpl_pitch_enc::PitchEstState,
    /// Per-subframe perceptual autocorrelation (shared CELP + pitch input), computed once per frame.
    perc_corrs: Vec<Vec<f32>>,
    /// Decoder-reconstructed per-block pitch lags (2 per subframe) for the voiced CELP ACB. The CELP
    /// builds its ACB basis from these so the encoder/decoder LTP contributions agree on the wire.
    block_lags: [[f32; 2]; SMPL_SUBFR_COUNT],
}

/// Turn one 60 ms PCM frame (960 f32 @16 kHz, ~[-1,1]) into params, advancing `es`.
pub(crate) fn smpl_analyze_frame_st(
    es: &mut SmplEncoderState,
    pcm: &[f32],
) -> super::params::SmplFrameParams {
    let need = SMPL_INTF_LEN * 3;
    let mut owned;
    let pcm: &[f32] = if pcm.len() < need {
        owned = vec![0f32; need];
        owned[..pcm.len()].copy_from_slice(pcm);
        &owned
    } else {
        pcm
    };
    let synth_t = load_smpl_synth_tables();

    // SILK VAD on the int16 input PCM (the C runs it on the raw API samples, before the encoder HP).
    // Produces the per-internal-frame speech-activity probability + the packet coded_as_active_voice.
    let pcm_i16: Vec<i16> = pcm[..need]
        .iter()
        .map(|&s| (s * 32768.0).round().clamp(-32768.0, 32767.0) as i16)
        .collect();
    let vad = es
        .vad
        .get_or_insert_with(super::smpl_vad::SmplVadState::new)
        .process_packet(&pcm_i16, SMPL_INTF_LEN);
    let sp_act_prob = vad.vad_results;
    let coded_as_active_voice = vad.coded_as_active_voice;

    // Encoder input high-pass (ARMA2, fcorner 35 Hz), matching the real encoder. Removes the
    // low-frequency content the decoder's de-emphasis would otherwise over-amplify; the residual the
    // analysis codes is then in the same band the real codec quantizes.
    let (hp_ma, hp_ar) = *es
        .hp_coefs
        .get_or_insert_with(|| smpl_get_hp_coefs(SMPL_ENC_HP_FCORNER_HZ));
    let pcm_in: Vec<f32> = pcm[..need].to_vec();
    let mut hp = vec![0f32; need];
    smpl_filt_arma2(&pcm_in, need, hp_ma, hp_ar, &mut es.hp_state, &mut hp);

    // int16-scaled input with smplOrder lead samples of history.
    let mut x = vec![0f64; SMPL_ORDER + need];
    if es.hist.len() >= SMPL_ORDER {
        x[..SMPL_ORDER].copy_from_slice(&es.hist[es.hist.len() - SMPL_ORDER..]);
    }
    for i in 0..need {
        x[SMPL_ORDER + i] = hp[i] as f64 * 32768.0;
    }

    let mut shadow = SmplFrameSynth::default();
    let mut prev_nlsf: Vec<f32> = Vec::new();
    // Predictor mirror, fresh per 60 ms frame (mirrors encode_smpl_frame's fresh SmplLsfState),
    // threaded across the 3 internal frames so the voiced abs-vs-delta lag choice matches the
    // entropy encoder.
    let mut lstate = super::smpl_decode::SmplLsfState::default();

    // Lazily build the persistent CELP encoder + perceptual model (their state carries across frames).
    es.celp.get_or_insert_with(|| {
        CelpEncoder::new(
            SMPL_CELP_LOW_RATE,
            SMPL_CELP_PERC_RESP_LEN,
            SMPL_CELP_FCB_SUBFRLEN,
            SMPL_CELP_SUBFR_PER_PACKET,
        )
    });
    es.perc.get_or_insert_with(PercModelState::new);
    es.bitrate.get_or_insert_with(BitrateController::new);
    if es.perc_prev.len() != SMPL_PERC_R_LEN {
        es.perc_prev = vec![0.0; SMPL_PERC_R_LEN];
    }

    // Normalized HP input for the CELP residual (the real encoder works in [-1,1], not int16). The C
    // `xhp_frame` for internal frame 0 starts `SMPL_WINNEXT_WB_LEN` (32) samples BEFORE the packet's
    // first sample (xhp_frame = xhp_packet_buf + SMPL_LPC_BUF_MEM_LEN, while x_in16k =
    // xhp_packet_buf + SMPL_LPC_BUF_MEM_LEN + SMPL_WINNEXT_WB_LEN), so the excitation it codes leads
    // the input by 32 samples. Carry SMPL_ORDER + 32 lead so the residual can read that far back.
    let res_lead: usize = SMPL_ORDER + SMPL_WINNEXT_WB_LEN;
    let mut xn = vec![0f32; res_lead + need];
    if es.hist.len() >= res_lead {
        for i in 0..res_lead {
            xn[i] = (es.hist[es.hist.len() - res_lead + i] / 32768.0) as f32;
        }
    }
    xn[res_lead..res_lead + need].copy_from_slice(&hp[..need]);

    // Full HP-domain buffer the C `lpcbuf` indexes: [history(144)] ++ [current 960 HP] ++ [32 zeros].
    // The 32-sample lookahead tail is zero at 16 kHz (no band split), per the C buffer layout.
    let mut hp_full = vec![0f32; SMPL_LPC_HIST_LEN + need + SMPL_WINNEXT_WB_LEN];
    if es.lpc_hist.len() == SMPL_LPC_HIST_LEN {
        hp_full[..SMPL_LPC_HIST_LEN].copy_from_slice(&es.lpc_hist);
    }
    hp_full[SMPL_LPC_HIST_LEN..SMPL_LPC_HIST_LEN + need].copy_from_slice(&hp[..need]);

    // Snapshot the previous packet's HP tail (pitch history for this packet's intf=0), then refresh it
    // from this packet's tail for the next call.
    let mut hp_pitch_hist = vec![0f32; SMPL_PITCH_LAG_MAX];
    if es.hp_pitch_hist.len() == SMPL_PITCH_LAG_MAX {
        hp_pitch_hist.copy_from_slice(&es.hp_pitch_hist);
    }
    es.hp_pitch_hist = hp[need - SMPL_PITCH_LAG_MAX..need].to_vec();

    // Lazily size the persistent perceptually-weighted speech buffer (the C `ltp_buf`).
    if es.ltp_buf.len() != super::smpl_pitch_enc::MAX_LTP_BUF_LEN {
        es.ltp_buf = vec![0.0f32; super::smpl_pitch_enc::MAX_LTP_BUF_LEN];
    }

    let celp = es.celp.as_mut().expect("celp built above");
    let perc = es.perc.as_mut().expect("perc built above");
    let bitrate = es.bitrate.as_mut().expect("bitrate built above");
    let ltp_buf = &mut es.ltp_buf;
    let pitch_est = &mut es.pitch_est;

    let mut prev_lsfq = es.prev_lsfq.clone();
    let mut prev_voiced = es.prev_voiced;

    let mut internal: [SmplInternalParams; 3] = Default::default();
    for f in 0..3 {
        let base = SMPL_ORDER + f * SMPL_INTF_LEN;
        let win = &x[base - SMPL_ORDER..base + SMPL_INTF_LEN];
        // `win_n` carries res_lead (SMPL_ORDER + res_pre) samples before the internal frame so the
        // residual can start res_pre samples early (matching the C `xhp_frame` vs `x_in16k` offset).
        let nbase = res_lead + f * SMPL_INTF_LEN;
        let win_n = &xn[nbase - res_lead..nbase + SMPL_INTF_LEN];

        // Front-end LPC analysis: the C windows `lpcbuf` (448 samples starting 96 before this frame),
        // FFT-autocorrelates it, and derives `A`/NLSF. `use_long_win` is true except the last frame.
        let lpc_start = SMPL_LPC_HIST_LEN - SMPL_LPC_PRE + f * SMPL_INTF_LEN;
        let mut lpcbuf = [0f32; SMPL_LPC_BUF_LEN];
        lpcbuf.copy_from_slice(&hp_full[lpc_start..lpc_start + SMPL_LPC_BUF_LEN]);
        let windowed = smpl_window_lpc20(&lpcbuf, f < 2);
        let (a, f2) = smpl_lpc_analyze_with_f2(&windowed);
        let nlsf = smpl_a2nlsf_16(&a);

        let mut cs = CelpFrameCtx {
            celp,
            perc,
            perc_prev: &mut es.perc_prev,
            bitrate,
            hp_n: &hp,
            intf: f,
            sp_act_prob: sp_act_prob[f],
            coded_as_active_voice,
            f2,
            voicing_strength: 0.0,
            vuv: &mut es.vuv,
            hp_pitch_hist: &hp_pitch_hist,
            ltp_buf: &mut *ltp_buf,
            pitch_est: &mut *pitch_est,
            perc_corrs: Vec::new(),
            block_lags: [[0.0; 2]; SMPL_SUBFR_COUNT],
        };
        let fe = FrontEndLsf {
            a,
            nlsf,
            prev_lsfq: &prev_lsfq,
            prev_voiced,
            intf: f,
        };
        let (ip, nlsf_out, voiced_out) = smpl_analyze_internal(
            synth_t,
            &mut shadow,
            &mut lstate,
            f,
            win,
            win_n,
            &prev_nlsf,
            &fe,
            &mut cs,
        );
        prev_nlsf = nlsf_out.clone();
        prev_lsfq = nlsf_out;
        prev_voiced = voiced_out;
        internal[f] = ip;
        // The C resets the lag-block predictor after the last internal frame of each packet (and after
        // any unvoiced frame, handled in smpl_analyze_internal), so cond-coding restarts per packet.
        if f == 2 {
            pitch_est.reset_cond();
        }
    }

    // Carry SMPL_ORDER + SMPL_WINNEXT_WB_LEN history so the next packet's residual lead is filled.
    es.hist = x[x.len() - (SMPL_ORDER + SMPL_WINNEXT_WB_LEN)..].to_vec();
    // Carry the last 144 HP samples as next packet's LPC window history (mirrors C `lpc_buf_mem`).
    es.lpc_hist = hp[need - SMPL_LPC_HIST_LEN..need].to_vec();
    es.prev_lsfq = prev_lsfq;
    es.prev_voiced = prev_voiced;
    super::params::SmplFrameParams {
        toc: 0x50,
        config: 0,
        internal,
    }
}

/// Front-end LPC/NLSF analysis result for one internal frame, plus the conditional-coding context.
struct FrontEndLsf<'a> {
    /// Post-BWE monic LPC `A[0..16]` (A[0]=1).
    a: [f32; SMPL_LPC_ORDER + 1],
    /// Analysis NLSF (`smpl_A2NLSF_16(A)`), radians 0..pi.
    nlsf: [f32; SMPL_LPC_ORDER],
    /// Previous internal frame's committed NLSF (for conditional coding).
    prev_lsfq: &'a [f32],
    prev_voiced: bool,
    intf: usize,
}

const SMPL_LPC_ORDER: usize = 16;

impl FrontEndLsf<'_> {
    /// Run the bit-exact LSF quantizer for `voiced` and the C cond-coding condition, returning the
    /// wire grid + stage2 + the committed (decoder-reconstructed) NLSF + the quantized predcoef.
    fn quantize(
        &self,
        synth_t: &SmplSynthTables,
        voiced: usize,
        prev_nlsf: &[f32],
    ) -> (i32, [i32; 16], Vec<f32>, [f32; 17]) {
        let cond = (self.prev_voiced == (voiced != 0)) && self.intf > 0;
        let res = if cond && self.prev_lsfq.len() == SMPL_LPC_ORDER {
            lsf_quant_cond(
                &self.a,
                &self.nlsf,
                self.prev_lsfq,
                voiced,
                0,
                SMPL_LSF_RDW_ADJ,
                SMPL_LSF_SURV,
            )
        } else {
            lsf_quant(
                &self.a,
                &self.nlsf,
                voiced,
                0,
                SMPL_LSF_RDW_ADJ,
                SMPL_LSF_SURV,
            )
        };
        let grid = res.qi[0];
        let mut stage2 = [0i32; 16];
        stage2.copy_from_slice(&res.qi[1..=SMPL_LPC_ORDER]);
        // Committed NLSF = the envelope the decoder rebuilds from the wire (proven == C qlsf).
        let committed =
            smpl_reconstruct_nlsf(synth_t, voiced, 0, grid as usize, &stage2, prev_nlsf);
        let a_vq = smpl_nlsf2a(&committed);
        let mut predcoef = [0.0f32; 17];
        for (i, &c) in a_vq.iter().enumerate().take(17) {
            predcoef[i] = c;
        }
        predcoef[0] = 1.0;
        (grid, stage2, committed, predcoef)
    }
}

fn commit_candidate(
    synth_t: &SmplSynthTables,
    st: &mut SmplFrameSynth,
    cand: &Candidate,
    prev_nlsf: &[f32],
) -> Vec<f32> {
    if cand.silent {
        let nlsf = smpl_reconstruct_nlsf(
            synth_t,
            0,
            0,
            cand.ip.lsf.grid as usize,
            &cand.ip.lsf.stage2,
            prev_nlsf,
        );
        let pulse_vec = vec![0i32; SMPL_INTF_LEN];
        synth_internal_frame(
            synth_t,
            st,
            0,
            0,
            cand.ip.lsf.grid as usize,
            &cand.ip.lsf.stage2,
            prev_nlsf,
            &pulse_vec,
            &cand.gain_q,
            &cand.pitch,
        );
        return nlsf;
    }
    let (_, nlsf) = synth_internal_frame(
        synth_t,
        st,
        cand.stage1 as usize,
        0,
        cand.grid as usize,
        &cand.qsym,
        prev_nlsf,
        &cand.pulse_vec,
        &cand.gain_q,
        &cand.pitch,
    );
    nlsf
}

fn smpl_unvoiced_candidate(
    synth_t: &SmplSynthTables,
    _st: &SmplFrameSynth,
    win: &[f64],
    win_n: &[f32],
    prev_nlsf: &[f32],
    fe: &FrontEndLsf,
    cs: &mut CelpFrameCtx,
) -> Candidate {
    let frame = &win[SMPL_ORDER..];

    let r0 = smpl_autocorr(frame, 0)[0];
    if r0 <= 0.0 {
        // Silent frame: still advance the CELP excitation state (zeros) so it stays in sync.
        let mut flat = [[0.0f32; 17]; SMPL_SUBFR_COUNT];
        for p in &mut flat {
            p[0] = 1.0;
        }
        let perc_corrs = cs.perc_corrs.clone();
        run_celp_subframes(
            cs,
            &flat,
            &[0.0f32; SMPL_INTF_LEN],
            &[[0.0; 2]; SMPL_SUBFR_COUNT],
            &perc_corrs,
            SMPL_PERC_EMPH_UV,
            0,
        );
        return smpl_silent_internal(synth_t);
    }

    // LSF: bit-exact C quantizer fed the faithful front-end NLSF. `grid`/`stage2` map directly onto the
    // wire (grid==16 = the cond centroid); `brec` is the decoder-reconstructed envelope (== C qlsf).
    let (bgrid, bsym, brec, _predcoef) = fe.quantize(synth_t, 0, prev_nlsf);

    // Per-subframe interpolated LPC (smpl_lpc_interpol): early subframes blend the previous frame's
    // committed NLSF with this frame's, smoothing the spectral transition the residual is whitened by.
    // The C `lsf_interpol_search` tries idx 1 too and keeps it when it lowers the residual energy.
    let (predcoefs, res_lpc, interpol_idx) = smpl_lsf_interpol_search(&brec, fe.prev_lsfq, win_n);

    // Run the CELP excitation encoder per subframe (each with its interpolated predcoef).
    let perc_corrs = cs.perc_corrs.clone();
    let celp_out = run_celp_subframes(
        cs,
        &predcoefs,
        &res_lpc,
        &[[0.0; 2]; SMPL_SUBFR_COUNT],
        &perc_corrs,
        SMPL_PERC_EMPH_UV,
        0,
    );

    // Map CELP pulses -> per-position pulse train; collect the per-subframe FCB gain index (= the
    // wire `nrg_res` symbol, which the decoder reads back as `fcbg_idx`).
    let mut pulse_vec = vec![0i32; SMPL_INTF_LEN];
    let mut fcbg_idx = [0i32; 4];
    const MAIN: usize = 1;
    for sf in 0..SMPL_SUBFR_COUNT {
        let out = &celp_out[sf];
        for &v in &out.pulses[MAIN] {
            // Same unpacking as the C: sign = 1 + 2*(v>>15); pos = v*sign - 1; pPulses[pos] += sign.
            let sign = 1 + 2 * ((v as i32) >> 15);
            let pos = (v as i32 * sign) - 1;
            if (0..SMPL_SUBFR_LEN as i32).contains(&pos) {
                pulse_vec[sf * SMPL_SUBFR_LEN + pos as usize] += sign;
            }
        }
        fcbg_idx[sf] = out.gain_idx[MAIN] as i32;
    }

    // Unvoiced LEVEL (`nrgres`): bit-exact `smpl_quant_nrg_res` on the per-subframe residual energy.
    // The wire gain block IS the nrgres layout (gain_main=nrgres_frame_qi, gain_delta=nrgres_shape_qi,
    // gain_tab==nrgres_shape_CB, cb1==step) so the decoder reads `gain_q[sf]` back as `nrgres_dbq_Q14`.
    let mut nrgres = [0f32; 4];
    for (sf, n) in nrgres.iter_mut().enumerate() {
        let res = &res_lpc[sf * SMPL_SUBFR_LEN..(sf + 1) * SMPL_SUBFR_LEN];
        // C `reslpc` (hence `nrgres`) is in the normalized [-1,1] domain (the encoder works in [-1,1]).
        let e: f32 = res.iter().map(|&v| v * v).sum();
        *n = e / SMPL_SUBFR_LEN as f32;
    }
    let nq = super::smpl_nrgres::quant_nrg_res_4(&nrgres);
    let gm = nq.frame_qi;
    let gd = nq.shape_qi;
    // Synthesis `gain_q[sf]` = the reconstructed per-subframe nrgres floor.
    let gain_q = nq.dbq_q14;

    let pp = smpl_build_pulse_params(&pulse_vec);
    let mut gains = SmplGainParams {
        gain_main: gm,
        gain_delta: gd,
        nrg_res: [-1; 4],
    };
    for sf in 0..4 {
        // The wire writes a per-subframe nrg_res (= fcbg_idx) only where pulses exist.
        gains.nrg_res[sf] = if pp.subfr[sf] > 0 { fcbg_idx[sf] } else { -1 };
    }

    Candidate {
        ip: SmplInternalParams {
            lsf: SmplLsfParams {
                stage1: 0,
                grid: bgrid,
                stage2: bsym,
                // lsf_interpol_idx: the decoder interpolates the per-subframe envelope with this, so it
                // must match the index the residual was whitened under.
                extra: interpol_idx,
            },
            pulses: pp,
            has_pitch: false,
            pitch: Default::default(),
            gains,
        },
        stage1: 0,
        grid: bgrid,
        qsym: bsym,
        pulse_vec,
        gain_q,
        pitch: unvoiced_pitch(),
        silent: false,
    }
}

/// Per-subframe perceptual weighting + CELP excitation for one internal frame (4 subframes of 80).
/// Returns the per-subframe CELP outputs; mutates the CELP/perc state so it stays in sync. `lags_subfr`
/// is the per-80-sample-subframe pitch lag in samples (0 = unvoiced); `emph` selects the perceptual
/// emphasis (UV vs V) and `voiced` drives the bitrate controller.
fn run_celp_subframes(
    cs: &mut CelpFrameCtx,
    predcoefs: &[[f32; 17]; SMPL_SUBFR_COUNT],
    res_lpc: &[f32],
    block_lags: &[[f32; 2]; SMPL_SUBFR_COUNT],
    perc_corrs: &[Vec<f32>],
    emph: [f32; 2],
    voiced: i32,
) -> Vec<super::smpl_celp::CelpSubframeOut> {
    let perc_wght = perc_corrs_to_wght(perc_corrs, emph, SMPL_CELP_PERC_RESP_LEN);
    let mut outs = Vec::with_capacity(SMPL_SUBFR_COUNT);

    // Per-subframe weighted target energy (the bitrate controller's `wnrg`). The C uses the
    // perceptually-weighted speech energy; the residual energy in the int16 domain is a faithful proxy
    // for the relative magnitudes the smoothing + importance ratios consume.
    let wnrgs: Vec<f32> = (0..SMPL_SUBFR_COUNT)
        .map(|sf| {
            let res = &res_lpc[sf * SMPL_SUBFR_LEN..(sf + 1) * SMPL_SUBFR_LEN];
            let scale = 32768.0f32;
            res.iter().map(|&v| (v * scale) * (v * scale)).sum::<f32>()
        })
        .collect();

    let enc = BitrateControllerInputs {
        internal_sample_rate: 16000,
        payload_size_ms: 60,
        fec_bit_rate: 0,
        main_bit_rate: SMPL_MAIN_BIT_RATE,
        complexity: SMPL_COMPLEXITY,
        use_fec_rate_compensation: 0,
        use_dtx: 0,
        sub_frame_importance_factor: 1.0,
    };

    for sf in 0..SMPL_SUBFR_COUNT {
        let wnrg = wnrgs[sf];
        let wnrg_next = if sf + 1 < SMPL_SUBFR_COUNT {
            wnrgs[sf + 1]
        } else {
            wnrgs[sf]
        };
        let nonflatness = if voiced != 0 { 0.0 } else { 2.0 };
        // Real classifier voicing_strength (the C `voicing_strength_buf`), negative for unvoiced.
        let voicing_strength = cs.voicing_strength;
        let (max_pulses, importance) = cs.bitrate.control(
            &enc,
            0,
            cs.coded_as_active_voice as i32,
            cs.sp_act_prob,
            nonflatness,
            voicing_strength,
            voiced,
            wnrg,
            wnrg_next,
            0,
            320,
            80,
        );
        let mut numsurv = [1i16; SMPL_MAX_PULSES_PER_SF as usize];
        let tot_surv =
            1000 * (SMPL_FCB_TOT_SURV_20MS_MAX * SMPL_CELP_FCB_SUBFRLEN as i32) / (20 * 16000);
        smpl_distribute_fcb_surv(&mut numsurv, max_pulses[1] as i32, tot_surv);

        // The two 40-sample sub-blocks of this subframe carry their own (decoder-reconstructed) lags;
        // index 2 is read by the encoder as the trailing lag (`lags[n_lags-1]`).
        let lags = [block_lags[sf][0], block_lags[sf][1], block_lags[sf][1]];

        let res = &res_lpc[sf * SMPL_SUBFR_LEN..(sf + 1) * SMPL_SUBFR_LEN];
        let out = cs.celp.encode_subframe(
            res,
            &predcoefs[sf],
            &perc_wght[sf],
            &lags,
            importance,
            max_pulses,
            &numsurv,
        );
        outs.push(out);
    }
    outs
}

const SMPL_MAX_PULSES_PER_SF: i32 = 40;

/// Per-subframe perceptual autocorrelation (the C `perc_corrs_buf`, length `SMPL_PERC_R_LEN`), the
/// shared input to BOTH the CELP weighting and the pitch-perceptual weighting. The WB path computes
/// the autocorrelation for odd subframes over a subframe-pair window and interpolates the even ones.
/// Advances the perc-model state, so it must run EXACTLY ONCE per internal frame.
fn compute_perc_corrs(cs: &mut CelpFrameCtx) -> [Vec<f32>; SMPL_SUBFR_COUNT] {
    let frame_ms = 20i32;
    let shorter = 32usize; // SMPL_WINNEXT_WB_LONG_LEN - SMPL_WINNEXT_WB_LEN
    let mut corrs: [Vec<f32>; SMPL_SUBFR_COUNT] = Default::default();
    let mut sf = 1;
    while sf < SMPL_SUBFR_COUNT {
        let start = cs.intf * SMPL_INTF_LEN + (sf - 1) * SMPL_SUBFR_LEN;
        let xlen = 2 * SMPL_SUBFR_LEN + shorter;
        let mut xsubfr = vec![0.0f32; xlen];
        for i in 0..xlen {
            let idx = start + i;
            xsubfr[i] = if idx < cs.hp_n.len() {
                cs.hp_n[idx]
            } else {
                0.0
            };
        }
        let is_last = (cs.intf == 2 && sf == SMPL_SUBFR_COUNT - 1) as i32;
        let r = smpl_perc_model(cs.perc, &xsubfr, xlen, frame_ms, is_last, SMPL_PERC_R_LEN);
        let mut even = vec![0.0f32; SMPL_PERC_R_LEN];
        for i in 0..SMPL_PERC_R_LEN {
            let prev = cs.perc_prev.get(i).copied().unwrap_or(0.0);
            even[i] = 0.5 * (r[i] + prev);
        }
        corrs[sf - 1] = even;
        *cs.perc_prev = r.clone();
        corrs[sf] = r;
        sf += 2;
    }
    corrs
}

/// Derive the per-subframe `perc_wght_resp` (length perc_resp_len) from precomputed `perc_corrs` for
/// the given emphasis (`smpl_perc_ac2a`, voiced vs unvoiced). Pure (no state).
fn perc_corrs_to_wght(corrs: &[Vec<f32>], emph: [f32; 2], resp_len: usize) -> Vec<Vec<f32>> {
    corrs
        .iter()
        .map(|c| {
            smpl_perc_ac2a(
                c,
                SMPL_PERC_R_LEN,
                emph[if SMPL_CELP_LOW_RATE { 1 } else { 0 }],
                resp_len,
                SMPL_PERC_REG,
            )
        })
        .collect()
}

/// `lsf_interpol_search` (`smpl_core_encoder.c`): the per-subframe residual + interpolated predcoef
/// for `lsf_interpol_idx` 0, and the alternative idx 1 when it lowers the summed per-subframe residual
/// RMS by the C 0.998 margin. Returns (predcoefs, residual, chosen idx). At complexity 5-8 the C runs
/// this search for every active frame.
fn smpl_lsf_interpol_search(
    brec: &[f32],
    prev_lsfq: &[f32],
    win_n: &[f32],
) -> ([[f32; 17]; SMPL_SUBFR_COUNT], Vec<f32>, i32) {
    let residual_for = |idx: usize| -> ([[f32; 17]; SMPL_SUBFR_COUNT], Vec<f32>, f32) {
        let (predcoefs, _ilsf) =
            super::smpl_lpc::smpl_lpc_interpol_idx(brec, prev_lsfq, idx, smpl_nlsf2a);
        let mut res = vec![0f32; SMPL_INTF_LEN];
        let mut sum_rms = 0.0f32;
        for sf in 0..SMPL_SUBFR_COUNT {
            let r = smpl_analysis_residual_subfr(&predcoefs[sf], win_n, sf);
            let nrg: f32 = r.iter().map(|&v| v * v).sum();
            sum_rms += (nrg + 1e-30).sqrt();
            res[sf * SMPL_SUBFR_LEN..(sf + 1) * SMPL_SUBFR_LEN].copy_from_slice(&r);
        }
        (predcoefs, res, sum_rms)
    };

    let (pc0, res0, rms0) = residual_for(0);
    // The C runs the alt interpolation whenever lsf_interpol_search && active && numsubfrs>1.
    let (pc1, res1, rms1) = residual_for(1);
    if rms1 < rms0 * 0.998 {
        (pc1, res1, 1)
    } else {
        (pc0, res0, 0)
    }
}

/// One-subframe residual under that subframe's interpolated predcoef (`smpl_filt_ma16_monic` over the
/// `sf`-th 80-sample block of `win_n`, which carries SMPL_ORDER lead history before the frame).
fn smpl_analysis_residual_subfr(
    a_syn: &[f32; 17],
    win_n: &[f32],
    sf: usize,
) -> [f32; SMPL_SUBFR_LEN] {
    let mut res = [0f32; SMPL_SUBFR_LEN];
    for (n, rn) in res.iter_mut().enumerate() {
        let idx = SMPL_ORDER + sf * SMPL_SUBFR_LEN + n;
        let mut acc = win_n[idx];
        for j in 1..=SMPL_ORDER {
            acc += a_syn[j] * win_n[idx - j];
        }
        *rn = acc;
    }
    res
}

fn smpl_silent_internal(synth_t: &SmplSynthTables) -> Candidate {
    let mut sym = [0i32; 16];
    for (k, s) in sym.iter_mut().enumerate() {
        *s = (synth_t.valtables[0][0][0][k].len() / 2) as i32;
    }
    // Silent frame: lowest encodable gain (no pulses, so the exact value is immaterial).
    let (gm, gd, _) = smpl_rate_control_gains(0.0);
    Candidate {
        ip: SmplInternalParams {
            lsf: SmplLsfParams {
                stage1: 0,
                grid: 0,
                stage2: sym,
                extra: 0,
            },
            pulses: SmplPulseParams::default(),
            has_pitch: false,
            pitch: Default::default(),
            gains: SmplGainParams {
                gain_main: gm,
                gain_delta: gd,
                nrg_res: [-1; 4],
            },
        },
        stage1: 0,
        grid: 0,
        qsym: sym,
        pulse_vec: vec![0i32; SMPL_INTF_LEN],
        gain_q: [0; 4],
        pitch: unvoiced_pitch(),
        silent: true,
    }
}

fn smpl_autocorr(x: &[f64], order: usize) -> Vec<f64> {
    let n = x.len();
    let mut r = vec![0f64; order + 1];
    for (lag, rl) in r.iter_mut().enumerate() {
        let mut s = 0f64;
        for i in lag..n {
            s += x[i] * x[i - lag];
        }
        *rl = s;
    }
    r
}

fn smpl_build_pulse_params(pulse: &[i32]) -> SmplPulseParams {
    const P3: usize = 4;
    let pos_per = SMPL_INTF_LEN / P3; // 80
    let mut pp = SmplPulseParams::default();
    for sf in 0..P3 {
        let mut s = 0i32;
        for n in sf * pos_per..(sf + 1) * pos_per {
            s += pulse[n].abs();
        }
        pp.subfr[sf] = s;
    }
    pp.total = pp.subfr.iter().sum();

    let mut mag_runs: Vec<i32> = Vec::new();
    let mut signs: Vec<i32> = Vec::new();
    for sf in 0..P3 {
        if pp.subfr[sf] <= 0 {
            continue;
        }
        let base_pos = pos_per * sf;
        let mut positions: Vec<(usize, i32)> = Vec::new();
        for n in base_pos..base_pos + pos_per {
            if pulse[n] != 0 {
                positions.push((n, pulse[n]));
            }
        }
        let mut run_pos = base_pos as i32;
        let mut first = true;
        for &(p, magv) in &positions {
            let mag = magv.abs();
            let m = if first {
                p as i32 - base_pos as i32
            } else {
                p as i32 - run_pos
            };
            mag_runs.push(m);
            run_pos = p as i32;
            if mag > 1 {
                mag_runs.resize(mag_runs.len() + (mag - 1) as usize, 0);
            }
            signs.push(if magv < 0 { -1 } else { 1 });
            first = false;
        }
    }
    pp.mag_runs = mag_runs;

    // SIGN block: batch signs into raw symbols (<=15 bits each, MSB-first).
    let num_pos = signs.len();
    let mut sign_syms: Vec<SmplRawSym> = Vec::new();
    let mut p = 0;
    while p < num_pos {
        let nbits = (num_pos - p).min(15);
        let mut sym = 0u32;
        for q in 0..nbits {
            let bit = if signs[p + q] > 0 { 1u32 } else { 0 };
            sym |= bit << (nbits - 1 - q) as u32;
        }
        sign_syms.push(SmplRawSym {
            sym,
            nbits: nbits as u32,
        });
        p += nbits;
    }
    pp.sign_syms = sign_syms;
    pp
}

/// Find the (gainMain, gainDelta, reconstructed gainQ) whose linear gain is closest to `target_linear`.
fn smpl_rate_control_gains(target_linear: f64) -> (i32, i32, i32) {
    let mem = load_smpl_mem();
    let cfg_sel = 2u32;
    let cb1 = mem.i16(0xf35e0u32.wrapping_add(cfg_sel * 2)) as i32;
    let gain_tab_addr = 0xf35f0u32; // p3==4
    let mut best_d = f64::INFINITY;
    let (mut bgm, mut bgd, mut bgq) = (0i32, 0i32, 0i32);
    for gm in 0..84 {
        let base7 = gm * cb1 - 0x154000;
        for gd in 0..98 {
            let cbv = mem.i16(gain_tab_addr.wrapping_add(((4 * gd) as u32) * 2)) as i32;
            let gq = base7 + (cbv << 4);
            let d = (smpl_gain_lin(gq) - target_linear).abs();
            if d < best_d {
                best_d = d;
                bgm = gm;
                bgd = gd;
                bgq = gq;
            }
        }
    }
    (bgm, bgd, bgq)
}

// ===================== voiced (LTP) encode path =====================

/// `smpl_perc_emph_pitch` (smpl_tables.c): the perceptual emphasis the pitch weighting uses.
const SMPL_PERC_EMPH_PITCH: f32 = -0.82;
/// `pitch_perc_resp_len` for complexity 5-8 (the 17-tap monic MA weighting).
const SMPL_PITCH_PERC_RESP_LEN: usize = 17;
/// Pitch search history span in samples (`SMPL_MAXPITCH_LEN`), carried for the intf=0 estimator.
const SMPL_PITCH_LAG_MAX: usize = 320;
/// Pitch estimator lookahead (`SMPL_PITCH_LOOKAHEAD_LEN`).
const SMPL_PITCH_LOOKAHEAD_LEN: usize = 7;

/// Roll the persistent perceptually-weighted speech buffer (the C `ltp_buf`) and write this internal
/// frame's weighted speech into its tail, matching `smpl_core_encoder.c`: shift left by `framelen`,
/// then per CELP subframe `i` apply the 17-tap monic perceptual MA (`smpl_filt_ma16_monic`) of the HP
/// frame under `resp_pitch[i]`, plus the `PITCH_LOOKAHEAD_LEN`-sample lookahead under `resp_pitch[3]`.
/// The HP frame (`xhp_frame`) starts `SMPL_WINNEXT_WB_LEN` samples before the internal frame; the MA
/// reads up to `SMPL_LPC_ORDER` samples of history before that. Built in the normalized HP domain,
/// which is scale-invariant for the estimator's pitchcorr/lag outputs.
fn build_ltp_buf(cs: &mut CelpFrameCtx, perc_corrs: &[Vec<f32>]) {
    let resp_pitch = perc_corrs_to_wght(
        perc_corrs,
        [SMPL_PERC_EMPH_PITCH, SMPL_PERC_EMPH_PITCH],
        SMPL_PITCH_PERC_RESP_LEN,
    );
    let max_len = super::smpl_pitch_enc::MAX_LTP_BUF_LEN; // 659
    let look = SMPL_PITCH_LOOKAHEAD_LEN; // 7
    let framelen = SMPL_INTF_LEN; // 320
    // Shift existing weighted speech left by one internal frame (C memmove).
    let keep = max_len - framelen - look;
    cs.ltp_buf.copy_within(framelen..framelen + keep, 0);
    // HP sample at internal-frame-relative index `idx` (xhp_frame origin), reaching into the previous
    // packet's tail (`hp_pitch_hist`, entry `k` at relative index `k - SMPL_PITCH_LAG_MAX`) for idx<0.
    let frame_start = cs.intf as isize * SMPL_INTF_LEN as isize - SMPL_WINNEXT_WB_LEN as isize;
    let hist = SMPL_PITCH_LAG_MAX as isize;
    let sample = |rel: isize| -> f32 {
        let idx = frame_start + rel;
        if idx >= 0 {
            let u = idx as usize;
            if u < cs.hp_n.len() { cs.hp_n[u] } else { 0.0 }
        } else if cs.hp_pitch_hist.len() == hist as usize {
            let k = idx + hist;
            if k >= 0 {
                cs.hp_pitch_hist[k as usize]
            } else {
                0.0
            }
        } else {
            0.0
        }
    };
    // w_speech write origin in ltp_buf (C: MAX_LTP_BUF_LEN - numsubfrs*subfrlen - lookahead).
    let w_origin = max_len - SMPL_SUBFR_COUNT * SMPL_SUBFR_LEN - look; // 332
    for i in 0..SMPL_SUBFR_COUNT {
        let coef = &resp_pitch[i];
        for n in 0..SMPL_SUBFR_LEN {
            let pos = (i * SMPL_SUBFR_LEN + n) as isize;
            let mut res = sample(pos); // monic coef[0]==1
            for (j, &c) in coef
                .iter()
                .enumerate()
                .take(SMPL_PITCH_PERC_RESP_LEN)
                .skip(1)
            {
                res += c * sample(pos - j as isize);
            }
            cs.ltp_buf[w_origin + i * SMPL_SUBFR_LEN + n] = res;
        }
    }
    // Lookahead tail under the last subframe's response.
    let coef = &resp_pitch[SMPL_SUBFR_COUNT - 1];
    for n in 0..look {
        let pos = (framelen + n) as isize;
        let mut res = sample(pos);
        for (j, &c) in coef
            .iter()
            .enumerate()
            .take(SMPL_PITCH_PERC_RESP_LEN)
            .skip(1)
        {
            res += c * sample(pos - j as isize);
        }
        cs.ltp_buf[max_len - look + n] = res;
    }
}

/// Analyze one internal frame: compute the shared perceptual autocorrelation, build the perceptually-
/// weighted `ltp_buf`, run the faithful multi-stage pitch estimator + the `smpl_get_signal_mode`
/// voicing classifier, then build the voiced (LTP) or unvoiced candidate, commit it to the shadow synth
/// `st`, and advance the entropy predictor mirror.
#[allow(clippy::too_many_arguments)]
fn smpl_analyze_internal(
    synth_t: &SmplSynthTables,
    st: &mut SmplFrameSynth,
    lstate: &mut SmplLsfState,
    intf: usize,
    win: &[f64],
    win_n: &[f32],
    prev_nlsf: &[f32],
    fe: &FrontEndLsf,
    cs: &mut CelpFrameCtx,
) -> (SmplInternalParams, Vec<f32>, bool) {
    let mem = load_smpl_mem();

    // Shared perceptual autocorrelation (advances perc state EXACTLY ONCE per frame); both the pitch
    // weighting and the CELP weighting derive from it (matching the C `perc_corrs_buf`).
    cs.perc_corrs = compute_perc_corrs(cs).to_vec();

    // Roll the persistent perceptually-weighted speech buffer (the C `ltp_buf`) and write this frame's
    // weighted speech + lookahead into its tail, then run the faithful multi-stage pitch estimator.
    build_ltp_buf(cs, &cs.perc_corrs.clone());
    let f2 = cs.f2;
    let ltp_buf = cs.ltp_buf.clone();
    let pr =
        super::smpl_pitch_enc::smpl_pitch(cs.pitch_est, &ltp_buf, &f2, cs.coded_as_active_voice);
    let pitchcorr = pr.pitchcorr;
    let avg_lag = pr.avg_lag;
    let harm = pr.harm_strength;
    let mut lags8 = pr.lags;
    // The single representative lag the voiced encode path uses; the C's wire contour is anchored on
    // the first-subframe lag, so use that as the encode target (the per-block CELP basis is rebuilt
    // from the wire pitch params downstream).
    let lag_samples = pr.lags[0];
    let sp = cs.sp_act_prob;
    let vstr = smpl_get_signal_mode(pitchcorr, &lags8, avg_lag, harm, &f2, sp, cs.vuv);
    cs.voicing_strength = vstr;
    let is_voiced_decision = vstr > 0.0 && cs.coded_as_active_voice;
    lstate.prev_lag_samples = if is_voiced_decision { lag_samples } else { 0.0 };
    // The C resets the lag-block predictor after an unvoiced frame (and after each packet's last frame,
    // handled at the call site); mirror the unvoiced reset here so cond-coding restarts correctly.
    if !is_voiced_decision {
        cs.pitch_est.reset_cond();
        lags8 = [0.0; 8];
    }

    // The CELP excitation encoder advances its per-subframe acb/zir/prev-idx state, so it must run
    // EXACTLY ONCE per internal frame with the lags of the committed decision.
    let mut voiced_lstate = lstate.clone();
    smpl_advance_lsf_state(&mut voiced_lstate, intf, 1);
    let voiced = if is_voiced_decision {
        smpl_voiced_decision_for_lag(pr.blockseg_idx, &pr.laginds, cs, &mut lags8)
    } else {
        None
    };

    let (chosen, chosen_lstate, is_voiced) = match voiced {
        Some(vd) => {
            let cand = smpl_voiced_candidate(synth_t, win, prev_nlsf, fe, cs, &vd);
            (cand, Some(voiced_lstate), true)
        }
        None => (
            smpl_unvoiced_candidate(synth_t, st, win, win_n, prev_nlsf, fe, cs),
            None,
            false,
        ),
    };
    let committed_nlsf = commit_candidate(synth_t, st, &chosen, prev_nlsf);
    if chosen.stage1 == 1 {
        *lstate = chosen_lstate.expect("voiced candidate set its lstate");
        let subfr = chosen.ip.pulses.subfr;
        smpl_replay_pitch_state(mem, lstate, 4, subfr, &chosen.ip.pitch);
    } else {
        smpl_advance_lsf_state(lstate, intf, chosen.stage1);
    }
    (chosen.ip, committed_nlsf, is_voiced)
}

/// Advance the predictor mirror exactly as `encode_smpl_pitch` does, without entropy coding — so the
/// analysis predicts the lag/gain predictor for the next internal frame. Threads the C `ParamsEncoder`
/// lag predictor (`prev_lagblk`/`prev_lagidx`) from the chosen contour + per-block laginds.
fn smpl_replay_pitch_state(
    _mem: &SmplMem,
    st: &mut SmplLsfState,
    p3: i32,
    subfr_counts: [i32; 4],
    pp: &SmplPitchParams,
) {
    for sf in 0..(p3 as usize).min(4) {
        st.prev_gain_idx = pp.gain_idx[sf];
        if subfr_counts[sf] > 0 {
            st.prev_filt_idx = pp.filt_idx[sf];
        }
    }
    let tab = super::smpl_pitch_enc::load_pitch_tables();
    let (nblk, nidx) =
        super::smpl_pitch_enc::smpl_lags_predictor_after(tab, pp.blockseg_idx, &pp.laginds);
    st.prev_lagblk = nblk;
    st.prev_lagidx = nidx;
}

/// The committed voiced decision for one internal frame: the encodable pitch params and the
/// per-subframe synthesis lag carried in `pitch`. The LSF comes from the shared front-end.
struct VoicedDecision {
    pp: SmplPitchParams,
    pitch: SmplPitchSynth,
}

/// Carry the estimator's full per-block contour (`blockseg_idx` + `laginds`) into the voiced decision:
/// the wire pitch encode writes them straight through `smpl_encode_lags`, and the CELP ACB basis uses
/// the SAME per-block lags (`lag = laginds*0.5 + 32`) so the encoder/decoder LTP contributions agree.
/// The gain/filter indices here are placeholders; the voiced candidate overwrites them with the real
/// CELP `acb_idx`/`fcb_idx` per subframe.
fn smpl_voiced_decision_for_lag(
    blockseg_idx: usize,
    laginds: &[i32; 8],
    cs: &mut CelpFrameCtx,
    lags8: &mut [f32; 8],
) -> Option<VoicedDecision> {
    // The decoder maps each 40-block lag index `lag = laginds*0.5 + SMPL_MIN_PITCH_LAG`, clamped ≤320.
    let mut block_lags8 = [0.0f32; 8];
    for b in 0..8 {
        block_lags8[b] = (laginds[b] as f32 * 0.5 + 32.0).min(320.0);
    }
    *lags8 = block_lags8;
    for sf in 0..SMPL_SUBFR_COUNT {
        cs.block_lags[sf] = [block_lags8[2 * sf], block_lags8[2 * sf + 1]];
    }
    let mean_lag = block_lags8.iter().sum::<f32>() / 8.0;

    let pp = SmplPitchParams {
        gain_idx: [5i32; 4],
        filt_idx: [0i32; 4],
        blockseg_idx,
        laginds: *laginds,
    };

    let pitch = SmplPitchSynth {
        voiced: true,
        lag_subfr: [mean_lag as f64; 4],
        norm_gain: SMPL_VOICED_NORM_GAIN,
    };
    Some(VoicedDecision { pp, pitch })
}

/// Build the voiced (stage1=1 + LTP) candidate for one internal frame. The real CELP voiced encoder
/// runs with the decoder-reconstructed per-block lags (so its ACB basis matches the decoder's), and
/// its outputs drive the wire: `pulses[MAIN]` → the pulse train, `acb_idx[MAIN]` → the wire `gain_idx`
/// (ACB/LTP gain), `gain_idx[MAIN]` → the wire `filt_idx` (voiced FCB gain). The decoder then adds the
/// ACB contribution and scales the FCB pulses by the voiced gain table — reproducing the encoder's
/// excitation instead of the prior gainless greedy approximation.
fn smpl_voiced_candidate(
    synth_t: &SmplSynthTables,
    win: &[f64],
    prev_nlsf: &[f32],
    fe: &FrontEndLsf,
    cs: &mut CelpFrameCtx,
    vd: &VoicedDecision,
) -> Candidate {
    let win_n: Vec<f32> = win.iter().map(|&v| (v / 32768.0) as f32).collect();

    let gain_q = [0i32; 4]; // voiced synthesis uses the ACB+FCB excitation, not a gains block

    // Voiced-grid LSF: bit-exact C quantizer fed the faithful front-end NLSF (voiced codebook).
    let (bgrid, bsym, brec, _predcoef) = fe.quantize(synth_t, 1, prev_nlsf);

    // Per-subframe interpolated LPC (same as the unvoiced path).
    let (predcoefs, _ilsf) = super::smpl_lpc::smpl_lpc_interpol(&brec, fe.prev_lsfq, smpl_nlsf2a);
    let mut res_lpc = vec![0f32; SMPL_INTF_LEN];
    for sf in 0..SMPL_SUBFR_COUNT {
        let r = smpl_analysis_residual_subfr(&predcoefs[sf], &win_n, sf);
        res_lpc[sf * SMPL_SUBFR_LEN..(sf + 1) * SMPL_SUBFR_LEN].copy_from_slice(&r);
    }

    // Real voiced CELP: with nonzero lags the encoder runs the ACB/LTP path (calc_acb_gain → d_ltp →
    // FCB deldec on the post-LTP residual → calc_gains_v), producing the pulse set + acb/fcb indices.
    let block_lags = cs.block_lags;
    let perc_corrs = cs.perc_corrs.clone();
    let celp_out = run_celp_subframes(
        cs,
        &predcoefs,
        &res_lpc,
        &block_lags,
        &perc_corrs,
        SMPL_PERC_EMPH_V,
        1,
    );

    // Unpack the MAIN-rate pulses into a per-position train; collect acb/fcb indices per subframe.
    const MAIN: usize = 1;
    let mut pulse_vec = vec![0i32; SMPL_INTF_LEN];
    let mut acbg = [0i32; 4];
    let mut fcbg = [0i32; 4];
    for sf in 0..SMPL_SUBFR_COUNT {
        let out = &celp_out[sf];
        for &v in &out.pulses[MAIN] {
            let sign = 1 + 2 * ((v as i32) >> 15);
            let pos = (v as i32 * sign) - 1;
            if (0..SMPL_SUBFR_LEN as i32).contains(&pos) {
                pulse_vec[sf * SMPL_SUBFR_LEN + pos as usize] += sign;
            }
        }
        // acb_idx is always coded; fcb (filt) only where pulses exist. Clamp to the wire ranges.
        acbg[sf] = (out.acb_idx[MAIN] as i32).clamp(0, 15);
        fcbg[sf] = (out.gain_idx[MAIN] as i32).max(0);
    }
    let pp_pulses = smpl_build_pulse_params(&pulse_vec);
    let subfr = pp_pulses.subfr;
    let mut pp = vd.pp.clone();
    pp.gain_idx = acbg;
    for sf in 0..4 {
        pp.filt_idx[sf] = if subfr[sf] > 0 { fcbg[sf] } else { -1 };
    }

    Candidate {
        ip: SmplInternalParams {
            lsf: SmplLsfParams {
                stage1: 1,
                grid: bgrid,
                stage2: bsym,
                extra: 0,
            },
            pulses: pp_pulses,
            has_pitch: true,
            pitch: pp,
            gains: SmplGainParams::default(),
        },
        stage1: 1,
        grid: bgrid,
        qsym: bsym,
        pulse_vec,
        gain_q,
        pitch: vd.pitch.clone(),
        silent: false,
    }
}
```

### `smpl_pitch_enc.rs`

```rust
//! Faithful port of the SILK-style multi-stage pitch estimator (`smpl_pitch` in `smpl_pitch_util.c`,
//! tables/state in `smpl_pitch.c`). Replaces the prior single-resolution autocorrelation search: it
//! HP-filters and 2x-downsamples the perceptually-weighted `ltp_buf`, runs an open-loop block-track
//! survivor search at the coarse (16 kHz upsampled from 8 kHz) resolution, refines per-block at full
//! resolution around the survivors, and folds in the same rate/prev-lag/spectral-harmonicity biases the
//! C uses. The outputs (`pitchcorr`, per-subframe `lags[8]`, `avg_lag`, `harm_strength`) feed the
//! bit-exact `smpl_get_signal_mode` classifier, so faithful pitchcorr raises the voiced count to match C.
//!
//! The constant tables (blocksegs/blocktracks/CMFs, decoded from the packed C bitstream via `ec_dec` +
//! `dcmf_to_cmf` at load time) are loaded from a committed JSON fixture rather than re-porting the
//! decoders, since they are immutable. Only the 20 ms / 8-subframe config is supported (the active MLow
//! 1:1 path); 10 ms frames never occur here.
#![allow(clippy::needless_range_loop)]

use super::smpl_signal_mode::{build_f2w, harm_strength_at};
use std::sync::OnceLock;

// ---- constants (smpl_defines.h) ----
const FS_KHZ: i32 = 16;
const STAGE1_FS_KHZ: i32 = 8;
const COARSE_FS_KHZ: i32 = 16;
const TOT_INTERP_DELAY: i32 = 6;
pub(crate) const NUM_SUBFRAMES: usize = 8;
const MINPITCH_MS: i32 = 2;
const MAXPITCH_MS: i32 = 20;
const MINPITCH_LEN: i32 = MINPITCH_MS * FS_KHZ; // 32
const MAXPITCH_LEN: i32 = MAXPITCH_MS * FS_KHZ; // 320
const MINPITCH_STAGE1: i32 = MINPITCH_MS * STAGE1_FS_KHZ - TOT_INTERP_DELAY; // 10
const MAXPITCH_STAGE1: i32 = MAXPITCH_MS * STAGE1_FS_KHZ + TOT_INTERP_DELAY; // 166
const PITCH_DELTAWGHT: f32 = 0.1439;
const PITCH_SHORTWGHT1: f32 = 0.04;
const SPEC_HARM_BIAS: f32 = 2.5;
const PREVWGHT: f32 = 0.7981;
const PREVWGHT_SPAN: f32 = 0.15;
const RATEWGHT_HR: f32 = 0.022;
const LAG_SUBFRLEN: i32 = 40;
const LAG_SUBFRLEN_STAGE1: i32 = STAGE1_FS_KHZ * LAG_SUBFRLEN / FS_KHZ; // 20
const PITCHBLOCK_MS: i32 = 2;
const PITCH_LOOKAHEAD_LEN: usize = 7;
pub(crate) const MAX_LTP_BUF_LEN: usize = 659;
const F_LEN: usize = 257;

const PITCH_DOWNSAMP_DELAY: usize = 7;
const PITCH_INTERPOL_DELAY_C: usize = 4;

const PITCH_NUM_BLOCKS: usize = ((MAXPITCH_MS - MINPITCH_MS) / PITCHBLOCK_MS) as usize; // 9
const PITCHBLOCK: usize = (PITCHBLOCK_MS * FS_KHZ) as usize; // 32
const NUM_LAGS_STAGE1: usize = (MAXPITCH_STAGE1 - MINPITCH_STAGE1 + 1) as usize; // 157
const NUMLAGS_COARSE: usize = (COARSE_FS_KHZ * (MAXPITCH_MS - MINPITCH_MS)) as usize; // 288
const NUMLAGS_FS: usize = (FS_KHZ * (MAXPITCH_MS - MINPITCH_MS)) as usize; // 288

/// `numstates1` block-track survivors at complexity 5-8 (`update_complexity_setting`: `pitch_numstates1
/// = 24`). The `smpl_pitch.c` init default of 8 is overridden per-stream by `smpl_update_pitch_params`.
const NUMSTATES1: usize = 24;
/// complexity-8 is NOT low-complexity (numstates1 > 4), so `low_complexity_mode == false`.
const LOW_COMPLEXITY: bool = false;
/// 20 kbps is the HIGH-rate path (`low_rate == false`).
const LOW_RATE: bool = false;

// ---- decoded constant tables (loaded once from the committed blob) ----
#[derive(serde::Serialize, serde::Deserialize)]
struct BlockSeg {
    nblocks: usize,
    blocks: Vec<usize>,
    seglens: Vec<usize>,
}
#[derive(serde::Serialize, serde::Deserialize)]
struct BlockTrack {
    track: [usize; NUM_SUBFRAMES],
    meanblock: f32,
    trackdeltas: f32,
}
#[derive(serde::Serialize, serde::Deserialize)]
pub(crate) struct PitchTables {
    blocksegs: Vec<BlockSeg>,
    blocktracks: Vec<BlockTrack>,
    blocksegs2idx: Vec<usize>,
    blockseg_idx_cmf: Vec<u32>,
    delta_lag_cmfs: Vec<Vec<u32>>,
    blocksegs_ix: Vec<[usize; 2]>,
    firstblock_range: Vec<[usize; 2]>,
    block_transition_cmf: Vec<Vec<u32>>,
}

static TABLES: OnceLock<PitchTables> = OnceLock::new();

pub(crate) fn load_pitch_tables() -> &'static PitchTables {
    TABLES.get_or_init(|| {
        // zlib+protobuf (tables.proto `PitchTables`) so the byte-identical blob loads in Go.
        let pb: PbPitchTables =
            super::smpl_tables_blob::load_blob_prost(include_bytes!("testdata/smpl_pitch_tables.bin"));
        pb_into_pitch(pb)
    })
}

/// Parse the pitch-tables JSON dump into the runtime `PitchTables` (the table generator calls this,
/// so the `Value`-extraction runs once at gen time rather than every load).
#[cfg(test)]
pub(crate) fn parse_pitch_tables_json(s: &str) -> PitchTables {
    let v: serde_json::Value = serde_json::from_str(s).expect("pitch tables json");
    let as_usize = |x: &serde_json::Value| x.as_i64().unwrap() as usize;
    let as_u32 = |x: &serde_json::Value| x.as_i64().unwrap() as u32;
    let blocksegs = v["blocksegs"]
        .as_array()
        .unwrap()
        .iter()
        .map(|s| BlockSeg {
            nblocks: as_usize(&s["nblocks"]),
            blocks: s["blocks"]
                .as_array()
                .unwrap()
                .iter()
                .map(as_usize)
                .collect(),
            seglens: s["seglens"]
                .as_array()
                .unwrap()
                .iter()
                .map(as_usize)
                .collect(),
        })
        .collect();
    let blocktracks = v["blocktracks"]
        .as_array()
        .unwrap()
        .iter()
        .map(|t| {
            let mut track = [0usize; NUM_SUBFRAMES];
            for (i, e) in t["track"].as_array().unwrap().iter().enumerate() {
                track[i] = as_usize(e);
            }
            BlockTrack {
                track,
                meanblock: t["meanblock"].as_f64().unwrap() as f32,
                trackdeltas: t["trackdeltas"].as_f64().unwrap() as f32,
            }
        })
        .collect();
    let blocksegs2idx = v["blocksegs2idx"]
        .as_array()
        .unwrap()
        .iter()
        .map(as_usize)
        .collect();
    let blockseg_idx_cmf = v["blockseg_idx_CMF"]
        .as_array()
        .unwrap()
        .iter()
        .map(as_u32)
        .collect();
    let delta_lag_cmfs = v["delta_lag_CMFs"]
        .as_array()
        .unwrap()
        .iter()
        .map(|row| row.as_array().unwrap().iter().map(as_u32).collect())
        .collect();
    let blocksegs_ix = v["blocksegs_ix"]
        .as_array()
        .unwrap()
        .iter()
        .map(|p| {
            let a = p.as_array().unwrap();
            [as_usize(&a[0]), as_usize(&a[1])]
        })
        .collect();
    let firstblock_range = v["firstblock_range"]
        .as_array()
        .unwrap()
        .iter()
        .map(|p| {
            let a = p.as_array().unwrap();
            [as_usize(&a[0]), as_usize(&a[1])]
        })
        .collect();
    let block_transition_cmf = v["block_transition_CMF"]
        .as_array()
        .unwrap()
        .iter()
        .map(|row| row.as_array().unwrap().iter().map(as_u32).collect())
        .collect();
    PitchTables {
        blocksegs,
        blocktracks,
        blocksegs2idx,
        blockseg_idx_cmf,
        delta_lag_cmfs,
        blocksegs_ix,
        firstblock_range,
        block_transition_cmf,
    }
}

/// Per-stream estimator state (the C `PitchEstimator` non-scratch fields). `prev_lagblk/prev_lagidx`
/// are reset to -1 at frame boundaries by the encoder (`smpl_pitch_reset_cond`).
#[derive(Clone)]
pub(crate) struct PitchEstState {
    pub prev_lag: f32,
    pub prev_pitch_corr: f32,
    pub prev_lagblk: i32,
    pub prev_lagidx: i32,
}

impl Default for PitchEstState {
    fn default() -> Self {
        PitchEstState {
            prev_lag: 0.0,
            prev_pitch_corr: 0.0,
            prev_lagblk: -1,
            prev_lagidx: -1,
        }
    }
}

impl PitchEstState {
    /// `smpl_pitch_reset_cond`: clear the cross-frame lag-block predictor (called after the last frame
    /// of a packet and after any unvoiced frame, so cond-coding restarts).
    pub fn reset_cond(&mut self) {
        self.prev_lagblk = -1;
        self.prev_lagidx = -1;
    }
}

/// Pitch estimator result for one internal frame. `laginds`/`blockseg_idx` are the estimator's chosen
/// per-block lag indices and contour; the classifier consumes `pitchcorr`/`lags`/`avg_lag`/`harm`,
/// while the wire pitch encoder (downstream) can use the contour to carry the per-block lags.
pub(crate) struct PitchResult {
    pub pitchcorr: f32,
    pub lags: [f32; NUM_SUBFRAMES],
    #[allow(dead_code)]
    pub laginds: [i32; NUM_SUBFRAMES],
    pub avg_lag: f32,
    pub harm_strength: f32,
    #[allow(dead_code)]
    pub blockseg_idx: usize,
}

// ---- filters / DSP helpers (faithful to the C) ----

/// `smpl_filt_arma1` with `pitch_hp_b={1,-1}`, `pitch_hp_a={1,-0.96}`, zero state at call start.
/// MA1 (`ma[n] = x[n] - x[n-1]`, `ma[0] = x[0]`) then AR1 in the C's 5-sample unrolled form using
/// precomputed powers of `ar1 = 0.96`, so the float accumulation order matches `smpl_filt_ar1`.
fn pitch_hp_filter(x: &[f32], out: &mut [f32]) {
    let n = x.len();
    // MA1 into `out` (state_ma = x[-1] = 0).
    let mut state_ma = 0.0f32;
    for i in 0..n {
        out[i] = x[i] - state_ma;
        state_ma = x[i];
    }
    // AR1 (`smpl_filt_ar1`): y[n] = ma[n] + 0.96*y[n-1], unrolled by 5 like the C.
    let ar1 = 0.96f32;
    let ar1_2 = ar1 * ar1;
    let ar1_3 = ar1 * ar1_2;
    let ar1_4 = ar1 * ar1_3;
    let ar1_5 = ar1 * ar1_4;
    let mut ytmp = 0.0f32; // y[-1]
    let mut idx = 0usize;
    while idx + 4 < n {
        let x0 = out[idx];
        let x1 = out[idx + 1];
        let x2 = out[idx + 2];
        let x3 = out[idx + 3];
        let x4 = out[idx + 4];
        out[idx + 4] = x4 + ar1 * x3 + ar1_2 * x2 + ar1_3 * x1 + ar1_4 * x0 + ar1_5 * ytmp;
        out[idx] = x0 + ar1 * ytmp;
        out[idx + 1] = x1 + ar1 * x0 + ar1_2 * ytmp;
        out[idx + 2] = x2 + ar1 * x1 + ar1_2 * x0 + ar1_3 * ytmp;
        out[idx + 3] = x3 + ar1 * x2 + ar1_2 * x1 + ar1_3 * x0 + ar1_4 * ytmp;
        ytmp = out[idx + 4];
        idx += 5;
    }
    while idx < n {
        ytmp = out[idx] + ytmp * ar1;
        out[idx] = ytmp;
        idx += 1;
    }
}

const DOWNSAMP_FILT: [f32; 2 * PITCH_DOWNSAMP_DELAY + 1] = [
    -0.045472838,
    0.0,
    0.06366198,
    0.0,
    -0.10610329,
    0.0,
    0.31830987,
    0.5,
    0.31830987,
    0.0,
    -0.10610329,
    0.0,
    0.06366198,
    0.0,
    -0.045472838,
];

/// `smpl_pitch_downsample`: 2x decimating FIR. `ptr_in` has `PITCH_DOWNSAMP_DELAY` lead samples
/// (offset) already written into `ptr_out[0..offset]`; output length is `(L - 2*delay)/2`.
fn pitch_downsample(ptr_in: &[f32], l: usize, ptr_out: &mut [f32]) -> usize {
    let d = PITCH_DOWNSAMP_DELAY;
    let n = (l - 2 * d) / 2;
    for j in 0..n {
        let mut tmp = ptr_in[2 * j + d] * DOWNSAMP_FILT[d];
        let mut i = 0;
        while i < d {
            tmp += (ptr_in[2 * j + i] + ptr_in[2 * j + 2 * d - i]) * DOWNSAMP_FILT[i];
            i += 2;
        }
        ptr_out[j] = tmp;
    }
    n
}

const INTERPOL_FILT_C: [f32; 2 * PITCH_INTERPOL_DELAY_C] = [
    -0.0024414062,
    0.023925781,
    -0.119628906,
    0.59814453,
    0.59814453,
    -0.119628906,
    0.023925783,
    -0.0024414062,
];

/// `upsamp_E_core`: writes `2*len` samples backwards from `y` using `x` (read backwards). Even taps
/// copy `x`, odd taps average adjacent. `y_end`/`x_end` are the indices of the LAST written/read.
fn upsamp_e_core(buf: &mut [f32], x_end: usize, y_end: usize, len: usize) {
    let mut xi = x_end as isize;
    let mut yi = y_end as isize;
    for _ in 0..len {
        let v = (buf[xi as usize] + buf[(xi + 1) as usize]) * 0.5;
        buf[yi as usize] = v;
        yi -= 1;
        buf[yi as usize] = buf[xi as usize];
        yi -= 1;
        xi -= 1;
    }
}

/// `upsamp_C_core`: like upsamp_E but the interpolated sample uses the 8-tap `INTERPOL_FILT_C`.
fn upsamp_c_core(buf: &mut [f32], x_end: usize, y_end: usize, len: usize) {
    let mut xi = x_end as isize;
    let mut yi = y_end as isize;
    for _ in 0..len {
        let mut tmp = 0.0f32;
        for j in 0..PITCH_INTERPOL_DELAY_C {
            let a = buf[(xi + j as isize - (PITCH_INTERPOL_DELAY_C as isize - 1)) as usize];
            let b = buf[(xi + PITCH_INTERPOL_DELAY_C as isize - j as isize) as usize];
            tmp += (a + b) * INTERPOL_FILT_C[j];
        }
        buf[yi as usize] = tmp;
        yi -= 1;
        buf[yi as usize] = buf[xi as usize];
        yi -= 1;
        xi -= 1;
    }
}

#[inline]
fn smpl_nrg(x: &[f32]) -> f32 {
    x.iter().map(|&v| v * v).sum()
}

/// `smpl_get_maxi`: argmax; the C tree-reduction resolves ties to the FIRST index. A simple strict-`>`
/// scan (lowest index wins) matches it (validated by the C TIEPROBE harness on this data).
fn get_maxi(x: &[f32]) -> usize {
    let mut bi = 0usize;
    let mut best = x[0];
    for n in 1..x.len() {
        if x[n] > best {
            best = x[n];
            bi = n;
        }
    }
    bi
}

/// `smpl_get_maxi_K`: K highest-value indices. The C `naive_maxi_k` (ascending masked-max, strict `>`,
/// lowest-index-wins) is the validated equivalent of the production tree selection. Returns them in
/// selection order (descending value).
fn get_maxi_k(x: &[f32], k: usize) -> Vec<usize> {
    let mut taken = vec![false; x.len()];
    let mut out = Vec::with_capacity(k);
    for _ in 0..k {
        let mut bi: isize = -1;
        let mut best = f32::MIN;
        for n in 0..x.len() {
            if !taken[n] && (bi < 0 || x[n] > best) {
                best = x[n];
                bi = n as isize;
            }
        }
        if bi < 0 {
            break;
        }
        taken[bi as usize] = true;
        out.push(bi as usize);
    }
    out
}

// ---- E1 / C / E2 computation (smpl_pitch_util.c) ----

/// `smpl_calc_E1`: running energy of `lag_subfrlen`-length windows ending just before lag `minpitch`,
/// for each of `numlags` lags. `t` is the window-start anchor in `ltpbuf`.
fn calc_e1_inner(
    e1: &mut [f32],
    ltpbuf: &[f32],
    t: usize,
    minpitch: i32,
    maxpitch: i32,
    lag_subfrlen: usize,
) {
    let numlags = (maxpitch - minpitch + 1) as usize;
    let base = (t as i32 - minpitch) as usize; // &ltpbuf[t - minpitch]
    let reg = &ltpbuf[base - (numlags - 1)..]; // reg[-i] for i in 0..numlags valid
    // reg points at ltpbuf[t - minpitch]; we index reg[0], reg[-i], reg[lag_subfrlen - i].
    let reg0 = base; // absolute index of reg[0]
    e1[0] = smpl_nrg(&ltpbuf[reg0..reg0 + lag_subfrlen]).max(1e-9);
    for i in 1..numlags {
        let rm = ltpbuf[reg0 - i];
        let rs = ltpbuf[reg0 + lag_subfrlen - i];
        e1[i] = (e1[i - 1] + rm * rm - rs * rs).max(1e-9);
    }
    let _ = reg;
}

/// `smpl_pitch_calc_E1`: per-subframe E1 by computing an extended E1_ once then offsetting per subframe.
fn calc_e1(
    e1: &mut [f32],
    ltpbuf: &[f32],
    ltpbuf_len: usize,
    numsubfrs: usize,
    minpitch: i32,
    maxpitch: i32,
    lag_subfrlen: usize,
) {
    let numlags = (maxpitch - minpitch + 1) as usize;
    let maxpitch_ = maxpitch + (numsubfrs as i32 - 1) * lag_subfrlen as i32;
    let numlags_ = (maxpitch_ - minpitch + 1) as usize;
    let t = ltpbuf_len - lag_subfrlen;
    let mut e1_ext = vec![0.0f32; numlags_];
    calc_e1_inner(&mut e1_ext, ltpbuf, t, minpitch, maxpitch_, lag_subfrlen);
    let mut offset = (numlags_ - numlags) as isize;
    for sf in 0..numsubfrs {
        for i in 0..numlags {
            e1[sf * numlags + i] = e1_ext[(offset + i as isize) as usize];
        }
        offset -= lag_subfrlen as isize;
    }
}

fn dot_prod(a: &[f32], b: &[f32], n: usize) -> f32 {
    let mut r = 0.0f32;
    for i in 0..n {
        r += a[i] * b[i];
    }
    r
}

/// `smpl_pitch_calc_C_E2`: stage-1 cross-correlation `C` (8-sample dot, NUM_LAGS_STAGE1 lags/subframe)
/// and per-subframe target energy `E2`.
fn calc_c_e2(c: &mut [f32], e2: &mut [f32], ltpbuf: &[f32], ltpbuf_len: usize, numsubfrs: usize) {
    let mut t = ltpbuf_len - LAG_SUBFRLEN_STAGE1 as usize * numsubfrs;
    for sf in 0..numsubfrs {
        let tgt = &ltpbuf[t..t + 20];
        let reg0 = (t as i32 - MINPITCH_STAGE1) as usize;
        for i in 0..NUM_LAGS_STAGE1 {
            // dot_prod_20(tgt, &reg[-i]) where reg=&ltpbuf[reg0]
            let r = &ltpbuf[reg0 - i..reg0 - i + 20];
            c[sf * NUM_LAGS_STAGE1 + i] = dot_prod(tgt, r, 20);
        }
        t += LAG_SUBFRLEN_STAGE1 as usize;
        e2[sf] = dot_prod(tgt, tgt, 20).max(1e-9);
    }
}

/// `smpl_upsamp_E_fast`: in-place 2x upsample of a per-subframe E array, high subframe first.
fn upsamp_e_fast(buf: &mut [f32], numsubfrs: usize, minpitch: &mut i32, numlags: &mut usize) {
    let nin = *numlags;
    let nout = (nin - 1) * 2;
    for sf in (0..numsubfrs).rev() {
        let x_end = sf * nin + nin - 2;
        let y_end = sf * nout + nout - 1;
        upsamp_e_core(buf, x_end, y_end, nin - 1);
    }
    *numlags = nout;
    *minpitch *= 2;
}

/// `smpl_upsamp_C_fast`: in-place 2x upsample of a per-subframe C array using the interpolation filter.
fn upsamp_c_fast(buf: &mut [f32], numsubfrs: usize, minpitch: &mut i32, numlags: &mut usize) {
    let nin = *numlags;
    let nout = (nin - PITCH_INTERPOL_DELAY_C) * 2;
    for sf in (0..numsubfrs).rev() {
        let x_end = sf * nin + nin - 1 - PITCH_INTERPOL_DELAY_C;
        let y_end = sf * nout + nout - 1;
        upsamp_c_core(buf, x_end, y_end, nin - (PITCH_INTERPOL_DELAY_C * 2 - 1));
    }
    *numlags = nout;
    *minpitch *= 2;
}

fn dot_prod_40(a: &[f32], b: &[f32]) -> f32 {
    let mut r = 0.0f32;
    for i in 0..40 {
        r += a[i] * b[i];
    }
    r
}

fn sumdeltas(laginds: &[i32], numsubfrs: usize) -> i32 {
    let mut ret = 0;
    for i in 1..numsubfrs {
        ret += (laginds[i] - laginds[i - 1]).abs();
    }
    ret
}

/// `smpl_encode_lags(.., pEcCtx=NULL, mode)`: the rate (bits) the lag indices would cost, used as a
/// survivor bias. Mirrors the n_bits accumulation of the C (no entropy coding side-effects).
fn encode_lags_bits(
    tab: &PitchTables,
    blocksegs_ix: usize,
    laginds: &[i32],
    prev_lagblk: i32,
    prev_lagidx: i32,
    mode: usize,
) -> f32 {
    let mut n_bits = 0.0f32;
    let ix_julia = tab.blocksegs2idx[blocksegs_ix] as i32;
    let blocksize = PITCHBLOCK_MS * FS_KHZ * 2; // 64
    let pblockseg = &tab.blocksegs[blocksegs_ix];
    let mut prev_lagblk = prev_lagblk;
    let mut prev_lagidx = prev_lagidx;

    if prev_lagblk < 0 {
        let cmf = &tab.blockseg_idx_cmf;
        n_bits += ec_encode_bits(
            cmf[(ix_julia - 1) as usize],
            cmf[ix_julia as usize],
            cmf[tab.blocksegs.len()],
        );
    } else {
        let cmf = &tab.block_transition_cmf[prev_lagblk as usize];
        let b0 = pblockseg.blocks[0];
        n_bits += ec_encode_bits(cmf[b0], cmf[b0 + 1], cmf[PITCH_NUM_BLOCKS]);
        let start_ix = tab.firstblock_range[b0][0] as i32;
        let cmf_len = (tab.firstblock_range[b0][1] - tab.firstblock_range[b0][0] + 1) as i32;
        let cmf = &tab.blockseg_idx_cmf[start_ix as usize..];
        let lo = (ix_julia - start_ix - 1) as usize;
        let hi = (ix_julia - start_ix) as usize;
        n_bits += ec_encode_bits(
            cmf[lo] - cmf[0],
            cmf[hi] - cmf[0],
            cmf[cmf_len as usize] - cmf[0],
        );
    }

    let mut blk = pblockseg.blocks[0] as i32;
    let mut delta_blk = blk - prev_lagblk;
    let mut start_seg = 0usize;
    let mut laginds_ix = 0usize;
    if !((prev_lagblk > -1) && (-1..=2).contains(&delta_blk)) {
        n_bits += 6.0; // uniform first-lag cost (log2 blocksize)
        prev_lagblk = blk;
        prev_lagidx = laginds[laginds_ix];
        laginds_ix += pblockseg.seglens[0];
        start_seg = 1;
    }
    let delta_lag_cmf = &tab.delta_lag_cmfs[mode];
    for k in start_seg..pblockseg.nblocks {
        blk = pblockseg.blocks[k] as i32;
        let idx = laginds[laginds_ix];
        laginds_ix += pblockseg.seglens[k];
        delta_blk = blk - prev_lagblk;
        let delta_idx = idx - prev_lagidx;
        let prev_lagidx_mod = prev_lagidx - prev_lagblk * blocksize;
        let delta_range_start = -prev_lagidx_mod + delta_blk * blocksize;
        let cmf_base = (delta_range_start + 2 * blocksize - 1) as usize;
        let ix = (delta_idx - delta_range_start) as usize;
        let p0 = delta_lag_cmf[cmf_base];
        n_bits += ec_encode_bits(
            delta_lag_cmf[cmf_base + ix] - p0,
            delta_lag_cmf[cmf_base + ix + 1] - p0,
            delta_lag_cmf[cmf_base + blocksize as usize] - p0,
        );
        prev_lagblk = blk;
        prev_lagidx = idx;
    }
    n_bits
}

/// `ec_encode_wrap` with `pEcCtx==NULL`: returns the symbol's bit cost `-log2((fh-fl)/ft)`.
fn ec_encode_bits(fl: u32, fh: u32, ft: u32) -> f32 {
    let p = (fh as f32 - fl as f32) / ft as f32;
    if p <= 0.0 { 0.0 } else { -p.log2() }
}

/// Faithful port of C `smpl_encode_lags` (`pEcCtx != NULL`): write the blockseg selector + the
/// per-40-block lag indices (`laginds`) to the range stream. This IS the voiced lag wire encode, the
/// inverse of `decode_smpl_pitch`'s contour reconstruction. `prev_lagblk`/`prev_lagidx` are the C
/// `ParamsEncoder` lag predictor (-1 on the first frame of a packet / after a no-match); `mode` selects
/// the delta-lag CMF (0/1/2 by the mean ACB gain). The decoder rebuilds the exact per-block contour
/// from these bits, so its voiced ACB basis matches the encoder's.
pub(crate) fn smpl_encode_lags_wire(
    tab: &PitchTables,
    enc: &mut super::rangecoder::RangeEncoder,
    blocksegs_ix: usize,
    laginds: &[i32; NUM_SUBFRAMES],
    prev_lagblk: i32,
    prev_lagidx: i32,
    mode: usize,
) {
    let ix_julia = tab.blocksegs2idx[blocksegs_ix] as i32;
    let blocksize = PITCHBLOCK_MS * FS_KHZ * 2; // 64
    let pblockseg = &tab.blocksegs[blocksegs_ix];
    let mut prev_lagblk = prev_lagblk;
    let mut prev_lagidx = prev_lagidx;

    // Blockseg selector: absolute (CMF over all blocksegs) when no predictor, else block-transition
    // CMF for blocks[0] followed by the first-block-range CMF.
    if prev_lagblk < 0 {
        let cmf = &tab.blockseg_idx_cmf;
        enc.encode(
            cmf[(ix_julia - 1) as usize],
            cmf[ix_julia as usize],
            cmf[tab.blocksegs.len()],
        );
    } else {
        let cmf = &tab.block_transition_cmf[prev_lagblk as usize];
        let b0 = pblockseg.blocks[0];
        enc.encode(cmf[b0], cmf[b0 + 1], cmf[PITCH_NUM_BLOCKS]);
        let start_ix = tab.firstblock_range[b0][0] as i32;
        let cmf_len = (tab.firstblock_range[b0][1] - tab.firstblock_range[b0][0] + 1) as i32;
        let cmf = &tab.blockseg_idx_cmf[start_ix as usize..];
        let lo = (ix_julia - start_ix - 1) as usize;
        let hi = (ix_julia - start_ix) as usize;
        enc.encode(
            cmf[lo] - cmf[0],
            cmf[hi] - cmf[0],
            cmf[cmf_len as usize] - cmf[0],
        );
    }

    let mut blk = pblockseg.blocks[0] as i32;
    let mut delta_blk = blk - prev_lagblk;
    let mut start_seg = 0usize;
    let mut laginds_ix = 0usize;
    if !((prev_lagblk > -1) && (-1..=2).contains(&delta_blk)) {
        // First lag uniform-coded (`ec_encode(idx_mod, idx_mod+1, blocksize)`).
        let idx_mod = (laginds[laginds_ix] - blk * blocksize) as u32;
        enc.encode(idx_mod, idx_mod + 1, blocksize as u32);
        prev_lagblk = blk;
        prev_lagidx = laginds[laginds_ix];
        laginds_ix += pblockseg.seglens[0];
        start_seg = 1;
    }
    let delta_lag_cmf = &tab.delta_lag_cmfs[mode];
    for k in start_seg..pblockseg.nblocks {
        blk = pblockseg.blocks[k] as i32;
        let idx = laginds[laginds_ix];
        laginds_ix += pblockseg.seglens[k];
        delta_blk = blk - prev_lagblk;
        let delta_idx = idx - prev_lagidx;
        let prev_lagidx_mod = prev_lagidx - prev_lagblk * blocksize;
        let delta_range_start = -prev_lagidx_mod + delta_blk * blocksize;
        let cmf_base = (delta_range_start + 2 * blocksize - 1) as usize;
        let ix = (delta_idx - delta_range_start) as usize;
        let p0 = delta_lag_cmf[cmf_base];
        enc.encode(
            delta_lag_cmf[cmf_base + ix] - p0,
            delta_lag_cmf[cmf_base + ix + 1] - p0,
            delta_lag_cmf[cmf_base + blocksize as usize] - p0,
        );
        prev_lagblk = blk;
        prev_lagidx = idx;
    }
}

/// The C `ParamsEncoder` lag predictor after `encode_lb_voiced`: `prev_lagblk = blocks[nblocks-1]`,
/// `prev_lagidx = laginds[numsubfrs-1]`. Exposed so the analysis advances its mirror identically.
pub(crate) fn smpl_lags_predictor_after(
    tab: &PitchTables,
    blocksegs_ix: usize,
    laginds: &[i32; NUM_SUBFRAMES],
) -> (i32, i32) {
    let pblockseg = &tab.blocksegs[blocksegs_ix];
    let last_blk = pblockseg.blocks[pblockseg.nblocks - 1] as i32;
    (last_blk, laginds[NUM_SUBFRAMES - 1])
}

/// `spectral_harmonicity` with a per-survivor cache (keyed by harmonic bin). Reuses the classifier's
/// recompute via `harm_strength_at` for a single value; the loop here threads the cache exactly as C.
fn spectral_harmonicity_cached(
    avg_lag: f32,
    f2w: &[f32; F_LEN],
    cache: &mut [f32],
    reset: bool,
) -> f32 {
    const HARM_UNDEF: f32 = -10000.0;
    if reset {
        for c in cache.iter_mut() {
            *c = HARM_UNDEF;
        }
    }
    let inv_f2_step_hz = 2.0 * (F_LEN - 1) as f32 / 16000.0;
    let harm_hz = 16000.0 / avg_lag;
    let harm_ix = (harm_hz * 2.0 * inv_f2_step_hz).round() as i32;
    // The classifier's recompute is the single source of truth (the C asserts the bin is in range; an
    // out-of-range bin only arises from a degenerate lag and is handled there by a clamped recompute).
    if harm_ix < 0 || harm_ix as usize >= cache.len() {
        return harm_strength_at(avg_lag, f2w);
    }
    if cache[harm_ix as usize] > HARM_UNDEF {
        return cache[harm_ix as usize];
    }
    let hs = harm_strength_at(avg_lag, f2w);
    cache[harm_ix as usize] = hs;
    hs
}

/// `smpl_pitch`: the full estimator. `ltp_buf` is the perceptually-weighted speech of length
/// `MAX_LTP_BUF_LEN` (the last `PITCH_LOOKAHEAD_LEN` samples are lookahead). `f2` is the LPC power
/// spectrum. `coded_as_active_voice` gates the search (false → unvoiced defaults). Mutates the
/// cross-frame predictor in `st`.
#[allow(clippy::too_many_arguments)]
pub(crate) fn smpl_pitch(
    st: &mut PitchEstState,
    ltp_buf: &[f32],
    f2: &[f32; F_LEN],
    coded_as_active_voice: bool,
) -> PitchResult {
    let tab = load_pitch_tables();
    let numsubfrs = NUM_SUBFRAMES;
    let l = MAX_LTP_BUF_LEN;
    let look = PITCH_LOOKAHEAD_LEN;

    if !coded_as_active_voice {
        let min_lag = (MINPITCH_MS * FS_KHZ) as f32;
        st.prev_lag = 0.0;
        st.prev_pitch_corr = 0.0;
        st.prev_lagblk = -1;
        st.prev_lagidx = -1;
        return PitchResult {
            pitchcorr: 0.0,
            lags: [min_lag; NUM_SUBFRAMES],
            laginds: [0; NUM_SUBFRAMES],
            avg_lag: min_lag,
            harm_strength: 0.0,
            blockseg_idx: 0,
        };
    }

    // HP filter into ltp_buf_stage1[offset..], where offset = PITCH_DOWNSAMP_DELAY leading zeros.
    let offset = PITCH_DOWNSAMP_DELAY;
    let mut stage1 = vec![0.0f32; l + offset + 64]; // small slack
    pitch_hp_filter(ltp_buf, &mut stage1[offset..offset + l]);
    // ltp_buf_hp = stage1[offset .. offset + (L - look)]
    let hp_len = l - look;
    let ltp_buf_hp: Vec<f32> = stage1[offset..offset + hp_len].to_vec();

    // Downsample stage1[0 .. L+offset] -> stage1_ds (reuse a fresh buffer; the C writes in place but
    // we keep the HP signal we already copied out, so a separate output is equivalent).
    let mut stage1_ds = vec![0.0f32; (l + offset) / 2 + 8];
    let stage1_len = pitch_downsample(&stage1, l + offset, &mut stage1_ds);

    let numlags0 = NUM_LAGS_STAGE1;
    let mut e1 = vec![0.0f32; numlags0 * numsubfrs + 16];
    calc_e1(
        &mut e1,
        &stage1_ds,
        stage1_len,
        numsubfrs,
        MINPITCH_STAGE1,
        MAXPITCH_STAGE1,
        LAG_SUBFRLEN_STAGE1 as usize,
    );
    let mut e2 = vec![0.0f32; numsubfrs];
    // C / E arrays are over-allocated: the upsample stages expand them in place to full-res widths.
    let cap = (2 * FS_KHZ / STAGE1_FS_KHZ) as usize * NUM_LAGS_STAGE1 * numsubfrs + 64;
    let mut c = vec![0.0f32; cap];
    let mut e = vec![0.0f32; cap];
    let mut c_stage1 = vec![0.0f32; numlags0 * numsubfrs];
    calc_c_e2(&mut c_stage1, &mut e2, &stage1_ds, stage1_len, numsubfrs);
    c[..numlags0 * numsubfrs].copy_from_slice(&c_stage1);

    // E from sqrt-energy blend (stage 1).
    let numlags = numlags0;
    for sf in 0..numsubfrs {
        let mut sqrt_e1 = vec![0.0f32; numlags];
        for i in 0..numlags {
            sqrt_e1[i] = (e1[sf * numlags + i] + 1e-30).sqrt();
        }
        let sqrt_e2 = (e2[sf] + 1e-30).sqrt();
        for i in 0..numlags {
            let tmp = 0.5 * (sqrt_e1[i] + sqrt_e2);
            e[sf * numlags + i] = tmp * tmp;
        }
    }

    // Upsample to coarse (16 kHz) resolution.
    let mut minpitch_c = MINPITCH_STAGE1;
    let mut numlags_c = numlags;
    let mut minpitch_e = MINPITCH_STAGE1;
    let mut numlags_e = numlags;
    if LOW_COMPLEXITY {
        upsamp_e_fast(&mut c, numsubfrs, &mut minpitch_c, &mut numlags_c);
    } else {
        upsamp_c_fast(&mut c, numsubfrs, &mut minpitch_c, &mut numlags_c);
    }
    upsamp_e_fast(&mut e, numsubfrs, &mut minpitch_e, &mut numlags_e);

    let minpitch_coarse = COARSE_FS_KHZ * MINPITCH_MS;
    let numlags_coarse = NUMLAGS_COARSE;
    let offset_c0 = (minpitch_coarse - minpitch_c) as usize;
    let offset_e0 = (minpitch_coarse - minpitch_e) as usize;

    // H (coarse) and coarse copies.
    let mut h = vec![0.0f32; numlags_coarse * numsubfrs * 2 + 64];
    let mut h_coarse = vec![0.0f32; numlags_coarse * numsubfrs];
    let mut c_coarse = vec![0.0f32; numlags_coarse * numsubfrs];
    let mut e_coarse = vec![0.0f32; numlags_coarse * numsubfrs];
    for sf in 0..numsubfrs {
        for i in 0..numlags_coarse {
            let cv = c[sf * numlags_c + offset_c0 + i];
            let ev = e[sf * numlags_e + offset_e0 + i];
            h[sf * numlags_coarse + i] = cv / ev;
        }
        h_coarse[sf * numlags_coarse..(sf + 1) * numlags_coarse]
            .copy_from_slice(&h[sf * numlags_coarse..sf * numlags_coarse + numlags_coarse]);
        for i in 0..numlags_coarse {
            c_coarse[sf * numlags_coarse + i] = c[sf * numlags_c + offset_c0 + i];
            e_coarse[sf * numlags_coarse + i] = e[sf * numlags_e + offset_e0 + i];
        }
    }

    // Per-block coarse maxima -> Hblk.
    let pitchblock_coarse = (PITCHBLOCK_MS * COARSE_FS_KHZ) as usize; // 32
    let mut hblk = [[0.0f32; PITCH_NUM_BLOCKS]; NUM_SUBFRAMES];
    for sf in 0..numsubfrs {
        for block in 0..PITCH_NUM_BLOCKS {
            let base = sf * numlags_coarse + block * pitchblock_coarse;
            hblk[sf][block] = smpl_maximum(&h[base..base + pitchblock_coarse]);
        }
    }

    // Block-track survivor selection.
    let blocksize_fs = PITCHBLOCK * 2; // BLOCKSIZE = 64
    let reduction_factor = 0.7f32;
    let pitch_deltawght = PITCH_DELTAWGHT / blocksize_fs as f32;
    let mut sf_wght = [0.0f32; NUM_SUBFRAMES];
    {
        let sum_e2: f32 = e2.iter().take(numsubfrs).sum();
        for sf in 0..numsubfrs {
            sf_wght[sf] = e2[sf] / sum_e2;
        }
    }
    let num_blocktracks = tab.blocktracks.len();
    let mut utils = vec![0.0f32; num_blocktracks];
    for i in 0..num_blocktracks {
        let bt = &tab.blocktracks[i];
        let mut corr = 0.0f32;
        for sf in 0..numsubfrs {
            corr += hblk[sf][bt.track[sf]] * sf_wght[sf];
        }
        let shortlagbias1 = (MAXPITCH_LEN as f32 / ((bt.meanblock + 1.5) * PITCHBLOCK as f32)
            - 1.0)
            * PITCH_SHORTWGHT1;
        utils[i] = 1.0 / (1.1 - corr)
            - reduction_factor * PITCHBLOCK as f32 * pitch_deltawght * bt.trackdeltas
            + shortlagbias1;
    }
    let track_idx = get_maxi_k(&utils, NUMSTATES1);

    // Recompute full-res E1 over the HP signal.
    let mut e1_fs = vec![0.0f32; numlags_e * numsubfrs + 16];
    calc_e1(
        &mut e1_fs,
        &ltp_buf_hp,
        l - look,
        numsubfrs,
        minpitch_e,
        minpitch_e + numlags_e as i32 - 1,
        LAG_SUBFRLEN as usize,
    );

    // uniqueblocks bitmask per subframe from the survivor tracks.
    let mut uniqueblocks = [0u16; NUM_SUBFRAMES];
    for &ti in &track_idx {
        let track = &tab.blocktracks[ti].track;
        for sf in 0..numsubfrs {
            uniqueblocks[sf] |= 1 << track[sf];
        }
    }

    let h_thres = if LOW_COMPLEXITY { 0.0 } else { 0.25 };
    let offset_c = (MINPITCH_MS * FS_KHZ - minpitch_c) as usize;
    let offset_e = (MINPITCH_MS * FS_KHZ - minpitch_e) as usize;
    // Update C and E around survivor block peaks at full resolution.
    for sf in 0..numsubfrs {
        let mut mask = 1u16;
        let c_ptr = offset_c + sf * numlags_c;
        let e_ptr = offset_e + sf * numlags_e;
        let e1_ptr = offset_e + sf * numlags_e;
        let h_ptr = sf * NUMLAGS_FS;
        // ltp_buf_ptr = &ltp_buf_hp[L - look + (sf - numsubfrs)*LAG_SUBFRLEN]
        let ltp_off = ((l - look) as i32 + (sf as i32 - numsubfrs as i32) * LAG_SUBFRLEN) as usize;
        let e2_sf = dot_prod_40(&ltp_buf_hp[ltp_off..], &ltp_buf_hp[ltp_off..]).max(1e-9);
        e2[sf] = e2_sf;
        let sqrt_e2 = (e2_sf + 1e-30).sqrt();
        for block in 0..PITCH_NUM_BLOCKS {
            if uniqueblocks[sf] & mask != 0 {
                let mut sqrt_e1 = [0.0f32; PITCHBLOCK + 1];
                for i in 0..PITCHBLOCK + 1 {
                    sqrt_e1[i] = (e1_fs[e1_ptr + block * PITCHBLOCK + i] + 1e-30).sqrt();
                }
                for i in 0..PITCHBLOCK + 1 {
                    let tmp = 0.5 * (sqrt_e1[i] + sqrt_e2);
                    e[e_ptr + block * PITCHBLOCK + i] = 0.5 * tmp * tmp;
                }
                for i in 0..PITCHBLOCK {
                    if h[h_ptr + block * PITCHBLOCK + i] > h_thres {
                        let lag = (MINPITCH_LEN as usize) + block * PITCHBLOCK + i;
                        let a = &ltp_buf_hp[ltp_off..];
                        let b = &ltp_buf_hp[ltp_off - lag..];
                        c[c_ptr + block * PITCHBLOCK + i] = 0.5 * dot_prod_40(a, b);
                    }
                }
            }
            mask <<= 1;
        }
    }

    // Upsample C and E around survivor peaks to half-sample resolution and compute H (high to low).
    let stride_c = PITCH_NUM_BLOCKS * 2 * PITCHBLOCK + offset_c; // per-subframe frac stride
    let stride_e = PITCH_NUM_BLOCKS * 2 * PITCHBLOCK + offset_e;
    for sf in (0..numsubfrs).rev() {
        let c_ptr = offset_c + sf * numlags_c;
        let c_ptr_frac = offset_c + sf * stride_c;
        let e_ptr = offset_e + sf * numlags_e;
        let e_ptr_frac = offset_e + sf * stride_e;
        let h_ptr = sf * 2 * PITCHBLOCK * PITCH_NUM_BLOCKS;
        let mut mask = 1u16 << (PITCH_NUM_BLOCKS - 1);
        for block in (0..PITCH_NUM_BLOCKS).rev() {
            if uniqueblocks[sf] & mask != 0 {
                let ein = e_ptr + block * PITCHBLOCK;
                let eout = e_ptr_frac + block * 2 * PITCHBLOCK;
                upsamp_e_core(
                    &mut e,
                    ein + PITCHBLOCK - 1,
                    eout + 2 * PITCHBLOCK - 1,
                    PITCHBLOCK,
                );
                let cin = c_ptr + block * PITCHBLOCK;
                let cout = c_ptr_frac + block * 2 * PITCHBLOCK;
                if LOW_COMPLEXITY {
                    upsamp_e_core(
                        &mut c,
                        cin + PITCHBLOCK - 1,
                        cout + 2 * PITCHBLOCK - 1,
                        PITCHBLOCK,
                    );
                } else {
                    upsamp_c_core(
                        &mut c,
                        cin + PITCHBLOCK - 1,
                        cout + 2 * PITCHBLOCK - 1,
                        PITCHBLOCK,
                    );
                }
                for i in 0..2 * PITCHBLOCK {
                    h[h_ptr + block * 2 * PITCHBLOCK + i] = c[cout + i] / e[eout + i];
                }
            }
            mask >>= 1;
        }
    }

    // Fine search: per survivor, per blockseg, per block: combine H over the seg's subframes, argmax.
    let mut laginds_surv: Vec<[i32; NUM_SUBFRAMES]> = Vec::new();
    let mut blocksegs_ix_list: Vec<usize> = Vec::new();
    let mut h_comb = vec![0.0f32; 2 * PITCHBLOCK];
    let mut lagind_cache: std::collections::HashMap<i32, i32> = std::collections::HashMap::new();
    for &idx in &track_idx {
        let range = tab.blocksegs_ix[idx];
        for j in 0..range[1] {
            let bsx = range[0] + j;
            let pblockseg = &tab.blocksegs[bsx];
            let mut laginds_row = [0i32; NUM_SUBFRAMES];
            let mut start_sf = 0usize;
            for n in 0..pblockseg.nblocks {
                let lookup_key = (((start_sf as i32) << 3) + pblockseg.seglens[n] as i32) << 4
                    | pblockseg.blocks[n] as i32;
                let best_i = if let Some(&v) = lagind_cache.get(&lookup_key) {
                    v
                } else {
                    for v in h_comb.iter_mut() {
                        *v = 0.0;
                    }
                    for sf in start_sf..start_sf + pblockseg.seglens[n] {
                        let h_ptr = sf * 2 * PITCHBLOCK * PITCH_NUM_BLOCKS
                            + pblockseg.blocks[n] * 2 * PITCHBLOCK;
                        for i in 0..2 * PITCHBLOCK {
                            h_comb[i] += h[h_ptr + i] * e2[sf];
                        }
                    }
                    let bi = get_maxi(&h_comb) as i32;
                    lagind_cache.insert(lookup_key, bi);
                    bi
                };
                for sf in start_sf..start_sf + pblockseg.seglens[n] {
                    laginds_row[sf] = best_i + (pblockseg.blocks[n] * 2 * PITCHBLOCK) as i32;
                }
                start_sf += pblockseg.seglens[n];
            }
            laginds_surv.push(laginds_row);
            blocksegs_ix_list.push(bsx);
        }
    }
    let nlaginds = laginds_surv.len();

    // Final search.
    let pitch_ratewght = if LOW_RATE { 0.028 } else { RATEWGHT_HR };
    let f2w = build_f2w(f2);
    let max_ix = get_maxi(&sf_wght[..numsubfrs]);
    let mut spectral_harm_cache = vec![0.0f32; 50];

    let mut best_util = 0.0f32;
    let mut best_pitchcorr = 0.0f32;
    let mut best_surv = 0usize;
    let pitch_deltawght_fs = PITCH_DELTAWGHT / blocksize_fs as f32;

    for surv in 0..nlaginds {
        let mut sum_c = 0.0f32;
        let mut sum_e = 0.0f32;
        for sf in 0..numsubfrs {
            let c_base = offset_c + sf * stride_c;
            let e_base = offset_e + sf * stride_e;
            let li = laginds_surv[surv][sf] as usize;
            sum_c += c[c_base + li];
            sum_e += e[e_base + li];
        }
        let rate_bias = encode_lags_bits(
            tab,
            blocksegs_ix_list[surv],
            &laginds_surv[surv],
            st.prev_lagblk,
            st.prev_lagidx,
            1,
        ) * pitch_ratewght;
        let mean_lag = laginds_surv[surv][max_ix] as f32 * 0.5 + MINPITCH_LEN as f32;
        let pitchcorr = sum_c / sum_e;
        let first_lag = 0.5 * laginds_surv[surv][0] as f32 + MINPITCH_LEN as f32;
        let prev_lag_bias = get_prev_lag_bias(st, first_lag);
        let spectral_harm_bias = SPEC_HARM_BIAS
            * spectral_harmonicity_cached(mean_lag, &f2w, &mut spectral_harm_cache, surv == 0);
        let util = 1.0 / (1.1 - pitchcorr)
            - pitch_deltawght_fs * sumdeltas(&laginds_surv[surv], numsubfrs) as f32
            + spectral_harm_bias
            + prev_lag_bias
            - rate_bias;
        if surv == 0 || util > best_util {
            best_util = util;
            best_surv = surv;
        }
        if surv == 0 || pitchcorr > best_pitchcorr {
            best_pitchcorr = pitchcorr;
        }
    }

    let mut lags = [0.0f32; NUM_SUBFRAMES];
    let mut laginds_out = [0i32; NUM_SUBFRAMES];
    for sf in 0..numsubfrs {
        lags[sf] = laginds_surv[best_surv][sf] as f32 * 0.5 + MINPITCH_LEN as f32;
        laginds_out[sf] = laginds_surv[best_surv][sf];
    }
    let avg_lag = laginds_surv[best_surv][max_ix] as f32 * 0.5 + MINPITCH_LEN as f32;
    // SMPL_PITCH_SPEC_HARM_BIAS is defined, so the final harmonicity reuses the survivor-loop cache.
    let harm_strength = spectral_harmonicity_cached(avg_lag, &f2w, &mut spectral_harm_cache, false);

    st.prev_lag = lags[numsubfrs - 1];
    st.prev_pitch_corr = best_pitchcorr;
    st.prev_lagidx = laginds_surv[best_surv][numsubfrs - 1];
    st.prev_lagblk = st.prev_lagidx / (2 * PITCHBLOCK as i32);

    PitchResult {
        pitchcorr: best_pitchcorr,
        lags,
        laginds: laginds_out,
        avg_lag,
        harm_strength,
        blockseg_idx: blocksegs_ix_list[best_surv],
    }
}

fn smpl_maximum(x: &[f32]) -> f32 {
    let mut m = x[0];
    for &v in &x[1..] {
        if v > m {
            m = v;
        }
    }
    m
}

fn get_prev_lag_bias(st: &PitchEstState, lag: f32) -> f32 {
    let lag_diff = (lag - st.prev_lag).abs();
    let diff_thres = PREVWGHT_SPAN * st.prev_lag;
    if lag_diff < diff_thres {
        st.prev_pitch_corr * (1.0 - lag_diff / diff_thres) * PREVWGHT
    } else {
        0.0
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::Value;

    // Feed the C encoder's exact per-frame ltp_buf + F2 into the ported estimator (threading the
    // cross-frame predictor + frame-boundary reset as the C does) and require the outputs to converge
    // to the C `smpl_pitch`: pitchcorr (tight float tol), avg_lag, per-subframe laginds, blockseg_idx,
    // harm_strength (cache-aliasing tol). This is the rigorous proof the estimator port is faithful.
    #[test]
    fn pitch_estimator_matches_c_ground_truth() {
        let recs: Value =
            serde_json::from_str(include_str!("testdata/pitchio_ground_truth.json")).unwrap();
        let arr = recs.as_array().unwrap();
        assert!(arr.len() >= 30, "expected >=30 records, got {}", arr.len());

        // Thread prev_lag/prev_pitch_corr across frames (the estimator carries these), but seed
        // prev_lagblk/prev_lagidx from the C dump per frame so the rate-bias survivor selection uses
        // the exact predictor the C had (its reset timing depends on the voiced decision, out of scope
        // for the isolated estimator test).
        let mut st = PitchEstState::default();
        let mut max_pc_err = 0.0f32;
        let mut max_avglag_err = 0.0f32;
        let mut max_harm_err = 0.0f32;
        let mut lagind_mismatch = 0usize;
        let mut bsx_mismatch = 0usize;
        let mut checked = 0usize;
        for rec in arr {
            let _frame = rec["frame"].as_i64().unwrap();
            let cav = rec["cav"].as_i64().unwrap() != 0;
            st.prev_lagblk = rec["prev_lagblk"].as_i64().unwrap() as i32;
            st.prev_lagidx = rec["prev_lagidx"].as_i64().unwrap() as i32;
            let ltp_buf: Vec<f32> = rec["ltp_buf"]
                .as_array()
                .unwrap()
                .iter()
                .map(|v| v.as_f64().unwrap() as f32)
                .collect();
            assert_eq!(ltp_buf.len(), MAX_LTP_BUF_LEN);
            let f2v: Vec<f32> = rec["F2"]
                .as_array()
                .unwrap()
                .iter()
                .map(|v| v.as_f64().unwrap() as f32)
                .collect();
            let mut f2 = [0.0f32; F_LEN];
            f2.copy_from_slice(&f2v);

            let res = smpl_pitch(&mut st, &ltp_buf, &f2, cav);

            if cav {
                let pc_c = rec["pitchcorr"].as_f64().unwrap() as f32;
                let avg_c = rec["avg_lag"].as_f64().unwrap() as f32;
                let harm_c = rec["harm"].as_f64().unwrap() as f32;
                let bsx_c = rec["blockseg_idx"].as_i64().unwrap() as usize;
                let laginds_c: Vec<i32> = rec["laginds"]
                    .as_array()
                    .unwrap()
                    .iter()
                    .map(|v| v.as_i64().unwrap() as i32)
                    .collect();
                max_pc_err = max_pc_err.max((res.pitchcorr - pc_c).abs());
                max_avglag_err = max_avglag_err.max((res.avg_lag - avg_c).abs());
                max_harm_err = max_harm_err.max((res.harm_strength - harm_c).abs());
                let lag_mm = (0..NUM_SUBFRAMES).any(|sf| res.laginds[sf] != laginds_c[sf]);
                if res.blockseg_idx != bsx_c {
                    bsx_mismatch += 1;
                }
                if lag_mm {
                    lagind_mismatch += 1;
                }
                checked += 1;
            }
        }
        assert!(checked >= 20, "too few active frames checked: {checked}");
        assert!(
            max_pc_err < 1e-3,
            "pitchcorr diverges from C: max_err={max_pc_err}"
        );
        assert!(
            max_avglag_err < 1e-3,
            "avg_lag diverges from C: max_err={max_avglag_err}"
        );
        assert_eq!(lagind_mismatch, 0, "per-subframe laginds diverge from C");
        assert_eq!(bsx_mismatch, 0, "blockseg_idx diverges from C");
        assert!(
            max_harm_err < 0.05,
            "harm_strength diverges beyond cache-aliasing tol: {max_harm_err}"
        );
    }
}
```

### `smpl_signal_mode.rs`

```rust
//! Voiced/unvoiced classifier (`smpl_get_signal_mode.c`) and the spectral-harmonicity measure
//! (`spectral_harmonicity` in `smpl_pitch_util.c`) it shares with the pitch estimator. The classifier
//! folds five strengths (pitch correlation, VAD, spectral tilt, harmonicity, lag) plus a per-stream
//! hysteresis into a single `voicing_strength`; the encoder codes a frame voiced when that is positive
//! and the packet is coded-as-active-voice.
#![allow(clippy::needless_range_loop)]

use super::smpl_lpc::SMPL_F_LEN;

/// `smpl_vuv_weights` (smpl_tables.c): weights on corrs, vad, tilt, harmonicity, short lags. The C
/// declares 6 but only sums the first 5 (`smpl_sum_vec(smpl_vuv_weights, 5)`).
const SMPL_VUV_WEIGHTS: [f32; 5] = [1.0, 0.5, 0.5, 0.7, 0.3];
const SMPL_VUV_BIAS: f32 = -0.1038;
const SMPL_VUV_HYST: f32 = 0.05;
/// `SMPL_F_LEN / 3` (the transition index between the low/high spectral-tilt bands).
const TRANSITION_IX: usize = SMPL_F_LEN / 3;
const HARMONICITY_UNDEF: f32 = -10000.0;

#[inline]
fn smpl_sigmoid(x: f32) -> f32 {
    if x > 80.0 {
        return 1.0;
    }
    if x < -80.0 {
        return 0.0;
    }
    1.0 / (1.0 + (-x).exp())
}

#[inline]
fn smpl_inv_sigmoid(x: f32) -> f32 {
    -((1.0 / x) - 1.0).ln()
}

#[inline]
fn smpl_dot_prod(a: &[f32], b: &[f32], l: usize) -> f32 {
    let mut s = 0.0f32;
    for i in 0..l {
        s += a[i] * b[i];
    }
    s
}

#[inline]
fn smpl_sum_vec(x: &[f32], l: usize) -> f32 {
    let mut s = 0.0f32;
    for &v in x.iter().take(l) {
        s += v;
    }
    s
}

/// Per-stream voicing hysteresis + spectral-tilt background tracker (`VUV_Mode` in the C). The
/// encoder threads one instance across the whole stream.
#[derive(Clone)]
pub(crate) struct VuvMode {
    nrg_lo_bgn: f32,
    nrg_hi_bgn: f32,
    voicing_prev: f32,
    last_lag_prev: f32,
}

impl Default for VuvMode {
    fn default() -> Self {
        // The C zero-inits the struct (calloc), so all fields start at 0.
        VuvMode {
            nrg_lo_bgn: 0.0,
            nrg_hi_bgn: 0.0,
            voicing_prev: 0.0,
            last_lag_prev: 0.0,
        }
    }
}

/// `spectral_harmonicity` (smpl_pitch_util.c): harmonic peak/valley energy ratio at low frequencies,
/// from the per-bin weighted power spectrum `f2w` (the C `F2w`, = `F2[i] * (i+3)` with `F2w[0,1]=0`).
/// `cache` is the C's per-call harmonicity memo keyed by harmonic bin; `reset` clears it.
fn spectral_harmonicity(avg_lag: f32, f2w: &[f32], cache: &mut [f32], reset: bool) -> f32 {
    if reset {
        for c in cache.iter_mut() {
            *c = HARMONICITY_UNDEF;
        }
    }
    let inv_f2_step_hz = 2.0 * (SMPL_F_LEN - 1) as f32 / 16000.0;
    let harm_hz = 16000.0 / avg_lag;
    let harm_ix = (harm_hz * 2.0 * inv_f2_step_hz).round() as i32;
    debug_assert!(harm_ix >= 0);
    let cache_len = cache.len() as i32;
    if harm_ix >= cache_len {
        // The C asserts this never happens; guard defensively and fall through to recompute.
        return recompute_harmonicity(harm_hz, inv_f2_step_hz, f2w);
    }
    if cache[harm_ix as usize] > HARMONICITY_UNDEF {
        return cache[harm_ix as usize];
    }
    let hs = recompute_harmonicity(harm_hz, inv_f2_step_hz, f2w);
    cache[harm_ix as usize] = hs;
    hs
}

const NUM_HARMS: usize = 4;

fn recompute_harmonicity(harm_hz: f32, inv_f2_step_hz: f32, f2w: &[f32]) -> f32 {
    let harm_width = harm_hz * inv_f2_step_hz;
    let mut harm_strength = 0.1f32;
    if harm_width > 1.97 {
        let mut peak_valley_mags = [0.0f32; 2 * NUM_HARMS + 1];
        for (num_harm, pvm) in peak_valley_mags.iter_mut().enumerate() {
            let ix_start = 0.5 * num_harm as f32 * harm_width;
            let ix_end = ix_start + harm_width;
            let idx_start = ix_start.ceil() as i32;
            let idx_end = ix_end.floor() as i32;
            let weights_len = (idx_end - idx_start + 1).max(0) as usize;
            let mut weights = [0.0f32; 20];
            let inv_harm_width = 1.0 / harm_width;
            for (i, w) in weights.iter_mut().take(weights_len).enumerate() {
                let mut tmp = (idx_start as f32 - ix_start + i as f32) * inv_harm_width;
                tmp -= tmp * tmp;
                *w = tmp * tmp;
            }
            let base = (idx_start.max(0) as usize).min(f2w.len());
            // The C assumes the harmonic window stays within F2w; clamp defensively so a degenerate
            // (too-short) lag can't read past the spectrum.
            let avail = (f2w.len() - base).min(weights_len);
            let peak_valley_nrg =
                smpl_dot_prod(&f2w[base..], &weights, avail) / smpl_sum_vec(&weights, weights_len);
            *pvm = (peak_valley_nrg + 1e-30).sqrt();
        }
        let mut mag_ratios_log = [0.0f32; NUM_HARMS];
        let mut mag_weights = [0.0f32; NUM_HARMS];
        const MAG_PEAK_WEIGHTS: [f32; 3] = [1.0, 10.0, 1.0];
        const MAG_VALLEY_WEIGHTS: [f32; 3] = [5.0, 2.0, 5.0];
        for num_harm in 0..NUM_HARMS {
            let mag_peak = MAG_PEAK_WEIGHTS[0] * peak_valley_mags[2 * num_harm]
                + MAG_PEAK_WEIGHTS[1] * peak_valley_mags[2 * num_harm + 1]
                + MAG_PEAK_WEIGHTS[2] * peak_valley_mags[2 * num_harm + 2];
            let mag_valley = MAG_VALLEY_WEIGHTS[0] * peak_valley_mags[2 * num_harm]
                + MAG_VALLEY_WEIGHTS[1] * peak_valley_mags[2 * num_harm + 1]
                + MAG_VALLEY_WEIGHTS[2] * peak_valley_mags[2 * num_harm + 2];
            mag_ratios_log[num_harm] = (mag_peak / mag_valley).ln();
            mag_weights[num_harm] = (mag_peak + mag_valley + 1e-30).sqrt();
        }
        harm_strength = smpl_dot_prod(&mag_weights, &mag_ratios_log, NUM_HARMS)
            / smpl_sum_vec(&mag_weights, NUM_HARMS);
    }
    harm_strength
}

/// Build the C `F2w` (`F2[i] * (i+3)`, with `F2w[0]=F2w[1]=0`) consumed by `spectral_harmonicity`.
pub(crate) fn build_f2w(f2: &[f32; SMPL_F_LEN]) -> [f32; SMPL_F_LEN] {
    let mut f2w = [0.0f32; SMPL_F_LEN];
    for i in 2..SMPL_F_LEN {
        f2w[i] = f2[i] * (i + 3) as f32;
    }
    f2w
}

/// Harmonicity at `avg_lag` (the C call right after the pitch search, fresh cache). Reused by the
/// pitch estimator so its `harm_strength` matches the value fed to `smpl_get_signal_mode`.
pub(crate) fn harm_strength_at(avg_lag: f32, f2w: &[f32; SMPL_F_LEN]) -> f32 {
    let mut cache = [0.0f32; 50];
    spectral_harmonicity(avg_lag, f2w, &mut cache, true)
}

/// `smpl_get_signal_mode`: combine the five voicing strengths + hysteresis into `voicing_strength`.
/// `lags` is the per-lag-subframe pitch lag in samples (`framelen / SMPL_LAG_SUBFRLEN` entries);
/// `f2` is the power spectrum `F2[0..256]`. Mutates `vuv` (background tilt + hysteresis state).
pub(crate) fn smpl_get_signal_mode(
    pitchcorr: f32,
    lags: &[f32],
    avg_lag: f32,
    harm_strength: f32,
    f2: &[f32; SMPL_F_LEN],
    sp_act_prob: f32,
    vuv: &mut VuvMode,
) -> f32 {
    let corr_strength = smpl_inv_sigmoid(0.1 + 0.75 * pitchcorr.clamp(0.0, 1.0)); // -1.4 .. 1.4
    let vad_strength = 0.04 * (1.0 - 1.04 / (sp_act_prob + 0.04)); // -1 .. 0

    // spectral tilt
    let mut nrg_lo = 0.0f32;
    for i in 2..TRANSITION_IX {
        let tmp = f2[i] * (i + 3) as f32;
        nrg_lo += tmp * (TRANSITION_IX - i) as f32;
    }
    let mut nrg_hi = 0.0f32;
    for i in TRANSITION_IX..SMPL_F_LEN {
        let tmp = f2[i] * (i + 3) as f32;
        nrg_hi += tmp * (i - TRANSITION_IX) as f32;
    }
    if vad_strength < -0.1 {
        let smth_coef = -0.5 * vad_strength;
        vuv.nrg_lo_bgn += smth_coef * (nrg_lo - vuv.nrg_lo_bgn);
        vuv.nrg_hi_bgn += smth_coef * (nrg_hi - vuv.nrg_hi_bgn);
    }
    let tilt_lin = ((nrg_lo - vuv.nrg_lo_bgn).max(0.0) - (nrg_hi - vuv.nrg_hi_bgn).max(0.0))
        / (nrg_lo + nrg_hi + 1e-9);
    let tilt_strength = tilt_lin * tilt_lin * tilt_lin; // make less binary (C: tilt *= tilt*tilt)
    let lag_strength = -smpl_sigmoid(0.25 * (38.0 - avg_lag));

    let mut voicing_strength = (SMPL_VUV_WEIGHTS[0] * corr_strength
        + SMPL_VUV_WEIGHTS[1] * vad_strength
        + SMPL_VUV_WEIGHTS[2] * tilt_strength
        + SMPL_VUV_WEIGHTS[3] * harm_strength
        + SMPL_VUV_WEIGHTS[4] * lag_strength)
        / smpl_sum_vec(&SMPL_VUV_WEIGHTS, 5)
        + SMPL_VUV_BIAS;

    // hysteresis
    if vuv.last_lag_prev > 0.0 {
        let mut tmp = (lags[0] / vuv.last_lag_prev).log2();
        if tmp > 0.0 {
            tmp *= 0.5;
        }
        vuv.voicing_prev /= 0.4 + tmp * tmp;
    }
    voicing_strength += vuv.voicing_prev * SMPL_VUV_HYST;
    vuv.voicing_prev = (3.0 * voicing_strength).tanh();
    vuv.last_lag_prev = lags[lags.len() - 1];

    voicing_strength
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::Value;

    // Feed the C encoder's exact per-frame pitchcorr/avg_lag/harm/lags/F2/sp_act_prob (in stream
    // order, threading one VuvMode) and require our voicing_strength + voiced decision to match the
    // C `smpl_get_signal_mode` output. Isolates the classifier port from the pitch port.
    #[test]
    fn signal_mode_matches_c_ground_truth() {
        let recs: Value =
            serde_json::from_str(include_str!("testdata/sigmode_ground_truth.json")).unwrap();
        let arr = recs.as_array().unwrap();
        assert!(arr.len() >= 12);
        let mut vuv = VuvMode::default();
        let mut max_err = 0.0f32;
        let mut max_harm_err = 0.0f32;
        for rec in arr {
            let pitchcorr = rec["pitchcorr"].as_f64().unwrap() as f32;
            let avg_lag = rec["avg_lag"].as_f64().unwrap() as f32;
            let harm = rec["harm"].as_f64().unwrap() as f32;
            let sp = rec["sp_act_prob"].as_f64().unwrap() as f32;
            let vstr_c = rec["vstr"].as_f64().unwrap() as f32;
            let voiced_c = rec["voiced"].as_i64().unwrap() != 0;
            let lags: Vec<f32> = rec["lags"]
                .as_array()
                .unwrap()
                .iter()
                .map(|v| v.as_f64().unwrap() as f32)
                .collect();
            let f2v: Vec<f32> = rec["F2"]
                .as_array()
                .unwrap()
                .iter()
                .map(|v| v.as_f64().unwrap() as f32)
                .collect();
            let mut f2 = [0.0f32; SMPL_F_LEN];
            f2.copy_from_slice(&f2v);

            // Validate harm_strength_at against C's harm on frames where the pitch search ran. On
            // inactive frames the C `smpl_pitch` early-returns (lag clamped to the 32-sample floor)
            // and never computes harmonicity, leaving harm at its 0.0 init — not a recompute target.
            if avg_lag > 33.0 {
                let f2w = build_f2w(&f2);
                let harm_rs = harm_strength_at(avg_lag, &f2w);
                max_harm_err = max_harm_err.max((harm_rs - harm).abs());
            }

            let vstr_rs = smpl_get_signal_mode(pitchcorr, &lags, avg_lag, harm, &f2, sp, &mut vuv);
            max_err = max_err.max((vstr_rs - vstr_c).abs());
            // Voiced decision (all dump frames are coded_as_active_voice).
            assert_eq!(
                vstr_rs > 0.0,
                voiced_c,
                "voiced flip frame vstr_rs={vstr_rs} vstr_c={vstr_c}"
            );
        }
        assert!(
            max_err < 1e-4,
            "voicing_strength diverges from C: max_err={max_err}"
        );
        // harm_strength_at is exact PER call, but the C reuses a survivor-loop cache keyed by a
        // quantized harm bin, so its FINAL value can be one computed at a different (bin-sharing)
        // survivor lag. A fresh-cache recompute is therefore close but not bit-exact without the
        // full pitch survivor sequence; bound the residual.
        assert!(
            max_harm_err < 0.05,
            "harm_strength diverges from C beyond cache-aliasing tolerance: {max_harm_err}"
        );
    }
}
```

## Go envelope (signatures only)

The corresponding Go declarations — exported types and function **signatures with
no bodies**. This is the surface to implement; it is not the implementation.

```go
package mlow

const SmplEncodeBufBytes = 512

// MlowEncoder is the stateful top-level MLow encoder. The cross-frame analysis
// history persists across Encode calls.
type MlowEncoder struct {
	state SmplEncoderState
}

func NewMlowEncoder() *MlowEncoder

// Reset clears the cross-frame analysis history (call at a stream discontinuity).
func (e *MlowEncoder) Reset()

// Encode turns one 60 ms frame (exactly 960 samples) into a wire MLow frame.
func (e *MlowEncoder) Encode(pcm []float32) ([]byte, error)

// EncodeSmplFrame builds [TOC || range-coded body] from analyzed frame parameters.
func EncodeSmplFrame(fp *SmplFrameParams) ([]byte, error)

// VuvMode is the per-stream voicing hysteresis + spectral-tilt background tracker.
type VuvMode struct {
	nrgLoBgn    float32
	nrgHiBgn    float32
	voicingPrev float32
	lastLagPrev float32
}

// BuildF2w builds F2w (F2[i] * (i+3), with F2w[0]=F2w[1]=0).
func BuildF2w(f2 *[SmplFLen]float32) [SmplFLen]float32

// HarmStrengthAt is the harmonicity at avgLag with a fresh cache.
func HarmStrengthAt(avgLag float32, f2w *[SmplFLen]float32) float32

// SmplGetSignalMode combines the five voicing strengths + hysteresis into the
// voicing strength; it mutates vuv. lags is the per-lag-subframe pitch lag in
// samples; f2 is the power spectrum F2[0..256].
func SmplGetSignalMode(
	pitchcorr float32,
	lags []float32,
	avgLag float32,
	harmStrength float32,
	f2 *[SmplFLen]float32,
	spActProb float32,
	vuv *VuvMode,
) float32
```

## Implementation suggestions (guidance, not authoritative)

- `i32`/`u32`/`usize` map to Go `int32`/`uint32`/`int`; `f32`/`f64` map to
  `float32`/`float64` — keep the exact width per site, since the float arithmetic must
  match the reference bit-for-bit at the classifier (`max_err < 1e-4`).
- `Result<Vec<u8>, &'static str>` becomes `([]byte, error)`; return a sentinel `error`
  for the two failure cases (`pcm.len() != 960`, range-encoder overflow). `encode`
  sanitizes input: NaN → 0, clamp to [-1, 1] before analysis.
- The Rust marks most analysis types/fns `pub(crate)`/private; in Go expose only what a
  caller needs (`MlowEncoder`, `Encode`, `EncodeSmplFrame`, and — for the KAT — `VuvMode`,
  `SmplGetSignalMode`, `BuildF2w`, `HarmStrengthAt`). Keep the rest unexported.
- Fixed-size arrays (`[f32; SMPL_F_LEN]`, `[i32; 4]`, `[[f32; 2]; SMPL_SUBFR_COUNT]`)
  should stay value arrays in Go (`[SmplFLen]float32`, `[4]int32`), not slices, to
  preserve the exact element counts the indexing assumes. Pass large arrays by pointer.
- The many WASM heap reads (`mem.u8/i16/i32/u32/cdf_at` at literal hex addresses, the
  `wrapping_add`/`wrapping_mul`/`wrapping_sub` arithmetic) are unsigned modular
  pointer math — in Go do the offset math in `uint32` with wraparound semantics, never
  signed, or the table lookups drift.
- `VuvMode` and the encoder's `SmplEncoderState` carry per-stream state across calls;
  zero-init equals the C `calloc`. Do not re-create them per frame except on `Reset`.
- `TODO(human)`: this datasheet's KAT (`sigmode_ground_truth.json`) pins only the
  voicing classifier (`SmplGetSignalMode` / `HarmStrengthAt`). The full encoder
  (`analysis.rs` LPC/CELP/perc/bitrate front-end + `encode.rs` entropy coder) depends on
  many sibling modules (`smpl_celp`, `smpl_perc`, `smpl_lpc`, `smpl_lsf_quant`,
  `smpl_vad`, `smpl_mem`, …); decide the implementation order and which of those land
  before the encoder's own closed-loop round-trip can be exercised.
- `TODO(human)`: choose value vs pointer receiver for `MlowEncoder` and whether
  `SmplEncoderState` sub-models (CELP/perc/bitrate, `Option<...>` in Rust) are lazily
  constructed (`get_or_insert_with`) or eagerly built in `NewMlowEncoder`.
```

