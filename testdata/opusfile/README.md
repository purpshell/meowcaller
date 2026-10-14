# OpusFile fixture

`tone.ogg` is a generated one-second 440 Hz mono tone, encoded with FFmpeg/libopus 1.6.1. It contains both mandatory Ogg Opus headers (OpusHead and OpusTags), a pre-skip of 312 samples at 48 kHz, and 51 audio packets of 20 ms each.

Regenerate with:

```sh
ffmpeg -v error -f lavfi -i 'sine=frequency=440:sample_rate=48000:duration=1' -ac 1 -c:a libopus -b:a 64k -vbr off -frame_duration 20 -application audio -y testdata/opusfile/tone.ogg
```

At 16 kHz, the current source discards 104 pre-skip samples and emits 17 frames of 960 samples, padding the final frame. The regression checks decoding past OpusTags, frame shape, audible finite output, EOF and repeated Close. This generated fixture contains no human recordings.
