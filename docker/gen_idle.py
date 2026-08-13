#!/usr/bin/env python3
"""
Generate a 2-second, seamlessly-loopable "AI thinking" idle WAV.

Sound design — three layers at 22050 Hz, 16-bit mono:
  1. Warm drone: 130 Hz sine (amplitude 250) — barely audible, fills silence
  2. Digital pulse: 400 Hz sine burst every 0.4s, 60 ms bell-curve envelope,
     amplitude 1500 — conveys "I'm processing"
  3. Detuned shimmer: 402 Hz sine beating against the 400 Hz pulse at 1 Hz,
     amplitude 100 — gives a subtle "computing" texture

Boundary fade (120 ms) makes it seamlessly loopable.

Total amplitude peaks at ~2000 (≈6 % of full range) — ambient, never annoying.
"""

import math
import struct
import wave
import io
import sys

SR = 22050
DUR = 2.0
FADE = int(SR * 0.12)
N = int(SR * DUR)

samples = bytearray()
for i in range(N):
    t = i / SR

    # 1. Warm base tone (130 Hz, very quiet)
    base = 250.0 * math.sin(260.0 * math.pi * t)

    # 2. Digital pulse every 0.4 s (400 Hz burst, 60 ms envelope)
    p = t % 0.4
    env = max(0.0, math.sin(math.pi * p / 0.06)) if p < 0.06 else 0.0
    pulse = env * 1500.0 * math.sin(800.0 * math.pi * t)

    # 3. Detuned shimmer (402 Hz, 1 Hz beating → "computing" texture)
    shimmer = 100.0 * math.sin(804.0 * math.pi * t) * (0.5 + 0.5 * math.sin(2.0 * math.pi * t))

    sample = base + pulse + shimmer

    # Boundary fade for seamless looping
    if i < FADE:
        sample *= i / FADE
    elif i > N - FADE:
        sample *= (N - i) / FADE

    s = max(-32767, min(32767, int(sample)))
    samples += struct.pack("<h", s)

wav = io.BytesIO()
with wave.open(wav, "wb") as w:
    w.setnchannels(1)
    w.setsampwidth(2)
    w.setframerate(SR)
    w.writeframes(samples)

outpath = sys.argv[1] if len(sys.argv) > 1 else "idle.wav"
with open(outpath, "wb") as f:
    f.write(wav.getvalue())
print(f"written {outpath}: {len(samples)} bytes, {N} samples, {DUR}s, {SR} Hz")
