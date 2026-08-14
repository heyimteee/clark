#!/usr/bin/env python3
"""Render the wake-word affirmation clips and 'Processing, Sir.' with Kokoro
(Michael) using mlx-audio on the Mac.

The server image bakes piper (Ryan) fallback clips, but the Master wants Michael
for everything and piper only as a last resort. Run this on the Mac (which has
the MLX model) and copy the output into the server's affirmation volume so the
wake/processing clips match the reply voice.

Usage: render_affirmations.py [--model DIR] [--voice am_michael] [--out DIR]
"""
import argparse
import sys

import numpy as np

PHRASES = [
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


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--model", required=True, help="local dir with config.json + safetensors + voices/")
    parser.add_argument("--voice", default="am_michael")
    parser.add_argument("--out", required=True, help="output dir (00.wav..09.wav, processing.wav)")
    args = parser.parse_args()

    import os
    import mlx.core as mx

    from mlx_audio.tts.utils import load_model

    os.makedirs(args.out, exist_ok=True)
    model = load_model(args.model, lazy=False, model_type="kokoro")

    def synth(text, path):
        segments = []
        for result in model.generate(text=text, voice=args.voice, speed=1.0, lang_code="a"):
            segments.append(result.audio)
        if not segments:
            raise RuntimeError("no audio for %r" % text)
        samples = np.asarray(mx.concatenate(segments))
        pcm = (np.clip(samples, -1.0, 1.0) * 32767.0).astype(np.int16)
        import wave
        with wave.open(path, "wb") as w:
            w.setnchannels(1)
            w.setsampwidth(2)
            w.setframerate(24000)
            w.writeframes(pcm.tobytes())

    for i, phrase in enumerate(PHRASES):
        synth(phrase, "%s/%02d.wav" % (args.out, i))
        print("rendered %02d.wav: %s" % (i, phrase))
    synth("Processing, Sir.", args.out + "/processing.wav")
    print("rendered processing.wav: Processing, Sir.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
