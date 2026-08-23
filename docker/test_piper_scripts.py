#!/usr/bin/env python3
"""Unit tests for the piper-tts 1.2.0 compat layer and its callers.

Runs with the stdlib only: PiperVoice is stubbed with the real 1.2.0
synthesis contract — synthesize(text, wav_file) writes into an open wave
writer — so no model download is required.
"""
import importlib.util
import io
import sys
import unittest
import wave
from pathlib import Path

DOCKER = Path(__file__).resolve().parent


def load(name):
    spec = importlib.util.spec_from_file_location(name, DOCKER / f"{name}.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


compat = load("piper_compat")


class StubVoice:
    """Mimics piper-tts 1.2.0: synthesize(text, wav_file) writes WAV."""

    def __init__(self, rate=22050):
        self.rate = rate
        self.received_text = None

    def synthesize(self, text, wav_file):
        self.received_text = text
        wav_file.setnchannels(1)
        wav_file.setsampwidth(2)
        wav_file.setframerate(self.rate)
        wav_file.writeframes(b"\x01\x00\x02\x00\x03\x00\x04\x00")


def parse_wav(data):
    w = wave.open(io.BytesIO(data), "rb")
    out = (w.getnchannels(), w.getsampwidth(), w.getframerate(), w.getnframes())
    w.close()
    return out


class TestCompat(unittest.TestCase):
    def test_wav_shape(self):
        v = StubVoice()
        data = compat.synth_wav_bytes(v, "Sir.")
        self.assertEqual(v.received_text, "Sir.")
        ch, width, rate, frames = parse_wav(data)
        self.assertEqual((ch, width), (1, 2))
        self.assertEqual(rate, 22050)
        self.assertEqual(frames, 4)

    def test_missing_wav_arg_signature_caught(self):
        # Guards the exact regression that broke the build: a stub whose
        # synthesize lacks the wav_file parameter must fail loudly here,
        # not silently at image-build time.
        class Old:
            def synthesize(self, text):
                pass
        with self.assertRaises(TypeError):
            compat.synth_wav_bytes(Old(), "x")


class TestCallersUseCompatLayer(unittest.TestCase):
    def test_gen_affirmations_synth_writes_wav(self):
        gen = load("gen_affirmations")
        out = DOCKER / "_tmp_test.wav"
        try:
            gen.synth(StubVoice(), "Yes, Sir?", str(out))
            self.assertEqual(parse_wav(out.read_bytes())[3], 4)
        finally:
            out.unlink(missing_ok=True)

    def test_piper_daemon_importable_and_compat_backed(self):
        daemon = load("piper_daemon")
        self.assertTrue(hasattr(daemon, "synth_wav_bytes"))


if __name__ == "__main__":
    sys.exit(unittest.main())
