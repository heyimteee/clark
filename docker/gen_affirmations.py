#!/usr/bin/env python3
"""Generate the wake-word affirmation clips and the 'Processing, Sir.' clip
with the piper voice (en_US-ryan-high), baked at image build so the browser
plays them instantly with zero server latency.

Usage: gen_affirmations.py <voice.onnx> <outdir>
"""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from piper_compat import synth_wav_bytes


def synth(voice, text, out):
    with open(out, "wb") as f:
        f.write(synth_wav_bytes(voice, text))


def main():
    if len(sys.argv) < 3:
        print("usage: gen_affirmations.py <voice.onnx> <outdir>", file=sys.stderr)
        return 2
    from piper import PiperVoice

    voice_path, outdir = sys.argv[1:3]

    voice = PiperVoice.load(voice_path)

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
        synth(voice, phrase, "%s/%02d.wav" % (outdir, i))
    synth(voice, "Processing, Sir.", outdir + "/processing.wav")

    return 0


if __name__ == "__main__":
    sys.exit(main())
