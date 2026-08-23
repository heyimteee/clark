#!/usr/bin/env python3
"""Shared synthesis helpers for clark's piper-tts 1.2.0 callers.

piper-tts 1.2.0 replaced PiperVoice.synthesize_wav with a chunked
synthesize() yielding AudioChunk objects. These helpers render text to a
complete WAV byte string and tolerate minor attribute naming differences
between 1.2.x point releases.
"""
import io
import wave

# Default sample rate used only when a chunk carries no discoverable rate.
DEFAULT_SAMPLE_RATE = 22050


def chunk_pcm(chunk):
    """Return raw signed-16-bit PCM bytes for one AudioChunk."""
    for attr in ("audio_int16_bytes", "audio_int16_array", "audio"):
        data = getattr(chunk, attr, None)
        if data is None:
            continue
        if hasattr(data, "tobytes"):  # numpy array
            return data.astype("int16").tobytes()
        return bytes(data)
    raise AttributeError("AudioChunk carries no known PCM attribute")


def chunk_rate(chunks, default=DEFAULT_SAMPLE_RATE):
    """Best-effort sample rate from the first chunk or its synthesis config."""
    for c in chunks:
        rate = getattr(c, "sample_rate", None)
        if not rate:
            cfg = getattr(c, "synthesis_config", None)
            rate = getattr(cfg, "sample_rate", None) if cfg else None
        if rate:
            return int(rate)
    return default


def synth_wav_bytes(voice, text):
    """Synthesize text via the piper-tts 1.2.0 chunk API into WAV bytes."""
    chunks = list(voice.synthesize(text))
    pcm = b"".join(chunk_pcm(c) for c in chunks)
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(chunk_rate(chunks))
        w.writeframes(pcm)
    return buf.getvalue()
