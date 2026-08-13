#!/usr/bin/env python3
"""Long-lived Kokoro TTS daemon for clark's TTS seam.

The ONNX model and voice vectors load once and stay resident, so every
request after startup is a fast stdin/stdout round-trip. Framing matches the
shared TTS daemon protocol:

  request:  [u32 little-endian length][UTF-8 text bytes]
  response: [u32 little-endian length][WAV bytes]  (16-bit mono, 24 kHz)

Usage: kokoro_daemon.py <model.onnx> <voices.bin> <voice>
"""
import io
import struct
import sys
import wave

import numpy as np


def read_exact(stream, n):
    buf = b""
    while len(buf) < n:
        chunk = stream.read(n - len(buf))
        if not chunk:
            return None
        buf += chunk
    return buf


def main():
    if len(sys.argv) < 4:
        print("usage: kokoro_daemon.py <model.onnx> <voices.bin> <voice>", file=sys.stderr)
        return 2
    model_path, voices_path, voice = sys.argv[1], sys.argv[2], sys.argv[3]

    from kokoro_onnx import Kokoro

    kokoro = Kokoro(model_path, voices_path)

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

        samples, sample_rate = kokoro.create(text, voice=voice, speed=1.0, lang="en-us")
        pcm = (np.clip(samples, -1.0, 1.0) * 32767.0).astype(np.int16).tobytes()

        buf = io.BytesIO()
        with wave.open(buf, "wb") as wav_file:
            wav_file.setnchannels(1)
            wav_file.setsampwidth(2)
            wav_file.setframerate(sample_rate)
            wav_file.writeframes(pcm)
        wav = buf.getvalue()

        stdout.write(struct.pack("<I", len(wav)) + wav)
        stdout.flush()

    return 0


if __name__ == "__main__":
    sys.exit(main())
