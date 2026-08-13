#!/usr/bin/env python3
"""Generate the wake-word affirmation clips and the 'Processing, Sir.' clip
with the piper voice (en_US-ryan-high), baked at image build so the browser
plays them instantly with zero server latency.

Usage: gen_affirmations.py <voice.onnx> <outdir>
"""
import io
import sys
import wave

from piper import PiperVoice


def synth(voice, text, out):
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        voice.synthesize_wav(text, w)
    with open(out, "wb") as f:
        f.write(buf.getvalue())


def main():
    if len(sys.argv) < 3:
        print("usage: gen_affirmations.py <voice.onnx> <outdir>", file=sys.stderr)
        return 2
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
