#!/usr/bin/env python3
"""faster-whisper transcription runner for clark's STT seam.

Reads a WAV clip on stdin and prints the transcript on stdout. The model is
pre-baked into the image at build time, so this never downloads at runtime.
English-only output, matching clark's voice output decision.
"""
import io
import sys

from faster_whisper import WhisperModel


def main() -> int:
    if len(sys.argv) < 2:
        print("usage: whisper_run.py <model-dir>", file=sys.stderr)
        return 2
    wav = sys.stdin.buffer.read()
    if not wav:
        print("empty audio on stdin", file=sys.stderr)
        return 2

    model = WhisperModel(sys.argv[1], device="cpu", compute_type="int8")
    segments, _info = model.transcribe(io.BytesIO(wav), language="en")
    print(" ".join(s.text.strip() for s in segments), end="")
    return 0


if __name__ == "__main__":
    sys.exit(main())
