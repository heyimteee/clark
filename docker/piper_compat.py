#!/usr/bin/env python3
"""Shared synthesis helper for clark's piper-tts 1.2.0 callers.

piper-tts 1.2.0 replaced PiperVoice.synthesize_wav(text, wav_file) with
synthesize(text, wav_file, ...) — same semantics, new name. This renders
text to a complete WAV byte string via an in-memory wave file.
"""
import io
import wave


def synth_wav_bytes(voice, text):
    """Synthesize text via piper-tts 1.2.0 into a complete WAV byte string."""
    buf = io.BytesIO()
    w = wave.open(buf, "wb")
    try:
        voice.synthesize(text, w)
    finally:
        try:
            w.close()  # finalizes the header on success
        except wave.Error:
            # Synthesis failed mid-write: swallow the cosmetic header error
            # so the real exception from voice.synthesize surfaces.
            pass
    return buf.getvalue()
