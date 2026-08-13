#!/usr/bin/env python3
"""Generate the wake-word affirmation clips and the 'Processing, Sir.' clip
with the Kokoro voice, baked at image build so the browser plays them
instantly with zero server latency.

Usage: gen_affirmations.py <model.onnx> <voices.bin> <voice> <outdir>
"""
import io
import sys
import wave

import numpy as np


def synth(kokoro, text, voice, out):
    samples, sample_rate = kokoro.create(text, voice=voice, speed=1.0, lang="en-us")
    pcm = (np.clip(samples, -1.0, 1.0) * 32767.0).astype(np.int16).tobytes()
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(sample_rate)
        w.writeframes(pcm)
    with open(out, "wb") as f:
        f.write(buf.getvalue())


def main():
    if len(sys.argv) < 5:
        print("usage: gen_affirmations.py <model.onnx> <voices.bin> <voice> <outdir>", file=sys.stderr)
        return 2
    model_path, voices_path, voice, outdir = sys.argv[1:5]

    from kokoro_onnx import Kokoro

    kokoro = Kokoro(model_path, voices_path)

    phrases = [
        "Sir.",
        "Listening, Sir.",
        "Right here, Sir.",
        "Yes, Sir?",
        "At your service, Sir.",
        "I'm here, Sir.",
        "How can I help, Sir?",
        "Ready when you are, Sir.",
        "Standing by, Sir.",
        "Go ahead, Sir.",
    ]
    for i, phrase in enumerate(phrases):
        synth(kokoro, phrase, voice, "%s/%02d.wav" % (outdir, i))
    synth(kokoro, "Processing, Sir.", voice, outdir + "/processing.wav")

    return 0


if __name__ == "__main__":
    sys.exit(main())
