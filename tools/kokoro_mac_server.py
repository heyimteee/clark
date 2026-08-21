#!/usr/bin/env python3
"""Kokoro TTS HTTP server for clark, meant to run on the Master's Mac.

Exposes a single POST /tts endpoint that clark's server calls over Tailscale,
so synthesis happens on Apple Silicon via Apple's MLX framework (native Metal
GPU/ANE) instead of the i5 box. Same HTTP contract as the previous
onnxruntime/CoreML server, so clark's Go client is unchanged.

  POST /tts
    Header: X-Clark-Kokoro-Token: <shared token>
    Body:   {"text":"...", "voice":"am_michael"}
    200:    {"audio":"<base64 WAV>", "format":"audio/wav"}
    401:    bad/missing token
    400:    missing text / empty audio

Usage: kokoro_mac_server.py [--port 8790] [--model DIR] [--voice am_michael]
                            [--token SECRET]

The model is a local directory from `huggingface_hub.snapshot_download` of
mlx-community/Kokoro-82M-8bit (config.json + safetensors + voices/).
"""
import argparse
import base64
import hmac
import io
import signal
import sys
import threading
import wave
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse

import numpy as np

# Requests larger than this are rejected outright: a legitimate TTS payload is
# a few KB of text, so anything bigger is abuse or a bug (memory DoS guard).
MAX_BODY_BYTES = 1 << 20

_token = ""
_kokoro = None

# MLX inference (and misaki's spacy phonemizer) is not safe to run concurrently
# from multiple handler threads: it intermittently raises "There is no
# Stream(cpu, 1) in current thread" and returns 500s, which made clark fall
# back to piper mid-reply (mixing voices). Synthesis is ~10x real-time on the
# Mac, so serializing it costs ~nothing: the HTTP layer stays concurrent and
# the SPA already queues playback FIFO.
_synth_lock = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    server_version = "Kokoro/1.0"

    def log_message(self, fmt, *args):
        sys.stderr.write("[kokoro] " + (fmt % args) + "\n")

    def _check_token(self):
        # Constant-time comparison so token bytes are never leaked via timing.
        got = self.headers.get("X-Clark-Kokoro-Token", "")
        return hmac.compare_digest(got, _token)

    def do_POST(self):
        if urlparse(self.path).path != "/tts":
            self._json(404, {"error": "not found"})
            return
        if not self._check_token():
            self._json(401, {"error": "unauthorized"})
            return
        try:
            length = int(self.headers.get("Content-Length", 0))
        except ValueError:
            self._json(400, {"error": "invalid Content-Length"})
            return
        if length > MAX_BODY_BYTES:
            self._json(413, {"error": "body too large"})
            return
        try:
            body = json_loads(self.rfile.read(length))
        except Exception:
            self._json(400, {"error": "invalid JSON body"})
            return
        text = (body.get("text") or "").strip()
        if not text:
            self._json(400, {"error": "text is required"})
            return
        voice = body.get("voice") or _voice
        try:
            wav = _synthesize(text, voice)
        except Exception as exc:
            self._json(500, {"error": "synthesis failed", "detail": str(exc)})
            return
        self._json(200, {"audio": base64.b64encode(wav).decode(), "format": "audio/wav"})

    def _json(self, code, payload):
        data = json_dumps(payload).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def json_loads(b):
    import json
    return json.loads(b)


def json_dumps(o):
    import json
    return json.dumps(o)


_voice = "am_michael"


def _synthesize(text, voice):
    import mlx.core as mx

    with _synth_lock:
        # Lang code "a" = American English. generate() yields per-segment
        # results; concatenate them into one clip so the reply plays as a
        # single sound.
        segments = []
        for result in _kokoro.generate(text=text, voice=voice, speed=1.0, lang_code="a"):
            segments.append(result.audio)
        if not segments:
            raise RuntimeError("model produced no audio")
        samples = np.asarray(mx.concatenate(segments))
    sample_rate = 24000

    pcm = (np.clip(samples, -1.0, 1.0) * 32767.0).astype(np.int16).tobytes()
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(sample_rate)
        w.writeframes(pcm)
    return buf.getvalue()


def main():
    global _token, _voice, _kokoro

    parser = argparse.ArgumentParser(description="Kokoro TTS server for clark")
    parser.add_argument("--port", type=int, default=8790)
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--model", required=True, help="local dir with config.json + safetensors + voices/")
    parser.add_argument("--voice", default="am_michael")
    parser.add_argument("--allow-no-token", action="store_true",
                        help="dev only: run without a shared token (auth disabled)")
    parser.add_argument("--token", default=os.environ.get("KOKORO_TOKEN", ""))
    args = parser.parse_args()

    # Fail closed (#57): an empty token silently disabled auth, leaving the
    # synthesis endpoint open to anyone who can reach the Mac. Refuse to start
    # instead; pass --allow-no-token explicitly to opt out (dev only).
    if not args.token and not args.allow_no_token:
        print(
            "error: a non-empty --token (or KOKORO_TOKEN env) is required. "
            "The TTS endpoint can synthesize speech in Clark's voice, so it must never run unauthenticated. "
            "Pass --allow-no-token to override for local development only.",
            file=sys.stderr,
        )
        return 2

    _token = args.token
    _voice = args.voice

    from mlx_audio.tts.utils import load_model

    # Eager load (default) materializes parameters on the main thread's default
    # stream, so request-handler threads can safely run generate() afterwards.
    _kokoro = load_model(args.model, lazy=False, model_type="kokoro")
    print(
        "Kokoro MLX ready (voice=%s, model=%s)" % (args.voice, args.model),
        file=sys.stderr,
        flush=True,
    )

    # Release the GIL during synthesis so concurrent /tts requests can overlap
    # (MLX eval is thread-safe; ThreadingHTTPServer + threads).
    httpd = ThreadingHTTPServer((args.host, args.port), Handler)
    print("Kokoro TTS listening on %s:%d (voice=%s)" % (args.host, args.port, _voice), file=sys.stderr, flush=True)

    def _shutdown(signum, _frame):
        print("shutting down", file=sys.stderr, flush=True)
        threading.Thread(target=httpd.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, _shutdown)
    signal.signal(signal.SIGINT, _shutdown)
    httpd.serve_forever()


if __name__ == "__main__":
    import os
    sys.exit(main())
