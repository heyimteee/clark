#!/usr/bin/env python3
"""Long-lived piper daemon for clark's TTS seam.

The model is loaded once and stays resident, so every request after startup is
a fast stdin/stdout round-trip. Framing is simple and unambiguous:

  request:  [u32 little-endian length][UTF-8 text bytes]
  response: [u32 little-endian length][WAV bytes]

Usage: piper_daemon.py <voice.onnx>
"""
import io
import struct
import sys
import wave

from piper import PiperVoice


def read_exact(stream, n):
    buf = b""
    while len(buf) < n:
        chunk = stream.read(n - len(buf))
        if not chunk:
            return None
        buf += chunk
    return buf


def main():
    if len(sys.argv) < 2:
        print("usage: piper_daemon.py <voice.onnx>", file=sys.stderr)
        return 2
    voice = PiperVoice.load(sys.argv[1])

    stdin = sys.stdin.buffer
    stdout = sys.stdout.buffer

    while True:
        head = read_exact(stdin, 4)
        if head is None:
            break
        (n,) = struct.unpack("<I", head)
        data = read_exact(stdin, n)
        if data is None:
            break
        text = data.decode("utf-8", "replace")

        buf = io.BytesIO()
        with wave.open(buf, "wb") as wav_file:
            voice.synthesize_wav(text, wav_file)
        wav = buf.getvalue()

        stdout.write(struct.pack("<I", len(wav)) + wav)
        stdout.flush()

    return 0


if __name__ == "__main__":
    sys.exit(main())
