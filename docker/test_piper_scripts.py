#!/usr/bin/env python3
"""Unit tests for the piper-tts 1.2.0 compat layer and its callers.

Runs with the stdlib only: PiperVoice is stubbed, so no model download is
required. Validates that gen_affirmations.synth and piper_daemon produce
parseable WAV files from chunked synthesis.
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


class StubChunk:
    def __init__(self, pcm=b"\x01\x00", rate=22050):
        self.audio_int16_bytes = pcm
        self.sample_rate = rate


class StubVoiceModern:
    """Mimics piper-tts 1.2.0: synthesize() yields AudioChunks."""

    def __init__(self, rate=22050):
        self.rate = rate

    def synthesize(self, text):
        yield StubChunk(b"\x01\x00\x02\x00", self.rate)
        yield StubChunk(b"\x03\x00\x04\x00", self.rate)


class StubVoiceConfigRate:
    """Variant where the rate hides on synthesis_config (point-release drift)."""

    class _Cfg:
        sample_rate = 16000

    def __init__(self):
        self.rate = 16000

    def synthesize(self, text):
        c = StubChunk(b"\x05\x00")
        c.sample_rate = None
        c.synthesis_config = self._Cfg()
        yield c


def parse_wav(data):
    w = wave.open(io.BytesIO(data), "rb")
    out = (w.getnchannels(), w.getsampwidth(), w.getframerate(), w.getnframes())
    w.close()
    return out


class TestCompat(unittest.TestCase):
    def test_wav_shape(self):
        data = compat.synth_wav_bytes(StubVoiceModern(), "Sir.")
        ch, width, rate, frames = parse_wav(data)
        self.assertEqual((ch, width), (1, 2))
        self.assertEqual(rate, 22050)
        self.assertEqual(frames, 4)

    def test_rate_from_synthesis_config(self):
        data = compat.synth_wav_bytes(StubVoiceConfigRate(), "hello")
        self.assertEqual(parse_wav(data)[2], 16000)

    def test_unknown_chunk_raises(self):
        class Bad:
            pass
        v = type("V", (), {"synthesize": lambda s, t: iter([Bad()])})()
        with self.assertRaises(AttributeError):
            compat.synth_wav_bytes(v, "x")


class TestCallersUseCompatLayer(unittest.TestCase):
    def test_gen_affirmations_synth_writes_wav(self):
        gen = load("gen_affirmations")
        out = DOCKER / "_tmp_test.wav"
        try:
            gen.synth(StubVoiceModern(), "Yes, Sir?", str(out))
            self.assertEqual(parse_wav(out.read_bytes())[3], 4)
        finally:
            out.unlink(missing_ok=True)

    def test_piper_daemon_importable_and_compat_backed(self):
        daemon = load("piper_daemon")
        self.assertTrue(hasattr(daemon, "synth_wav_bytes"))


if __name__ == "__main__":
    sys.exit(unittest.main())
