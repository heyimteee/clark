#!/usr/bin/env python3
"""Kokoro TTS HTTP server for clark, meant to run on the Master's Mac.

Exposes a single POST /tts endpoint that clark's server calls over Tailscale,
so synthesis happens on Apple Silicon (CoreML preferred, CPU fallback) instead
of the i5 box. Mirrors the daemon's framing but over HTTP/JSON.

  POST /tts
    Header: X-Clark-Kokoro-Token: <shared token>
    Body:   {"text":"...", "voice":"am_michael"}
    200:    {"audio":"<base64 WAV>", "format":"audio/wav"}
    401:    bad/missing token
    400:    missing text / empty audio

Usage: kokoro_mac_server.py [--port 8790] [--model PATH] [--voices PATH]
                            [--voice am_michael] [--token SECRET]

The model and voices files must exist; the installer pre-downloads them to
~/.clark/kokoro/. Set ONNX_PROVIDER=CoreMLExecutionProvider (auto-detected)
to use the Apple Neural Engine via onnxruntime.
"""
import argparse
import base64
import io
import os
import signal
import sys
import threading
import wave
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse

import numpy as np

_token = ""
_kokoro = None


class Handler(BaseHTTPRequestHandler):
    server_version = "Kokoro/1.0"

    def log_message(self, fmt, *args):
        sys.stderr.write("[kokoro] " + (fmt % args) + "\n")

    def _check_token(self):
        if not _token:
            return True
        return self.headers.get("X-Clark-Kokoro-Token", "") == _token

    def do_POST(self):
        if urlparse(self.path).path != "/tts":
            self._json(404, {"error": "not found"})
            return
        if not self._check_token():
            self._json(401, {"error": "unauthorized"})
            return
        try:
            length = int(self.headers.get("Content-Length", 0))
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
    samples, sample_rate = _kokoro.create(text, voice=voice, speed=1.0, lang="en-us")
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
    parser.add_argument("--model", required=True)
    parser.add_argument("--voices", required=True)
    parser.add_argument("--voice", default="am_michael")
    parser.add_argument("--token", default=os.environ.get("KOKORO_TOKEN", ""))
    args = parser.parse_args()

    _token = args.token
    _voice = args.voice

    # Prefer CoreML (Apple Neural Engine) when available; kokoro-onnx reads
    # ONNX_PROVIDER when it builds its InferenceSession.
    try:
        import onnxruntime as ort
        providers = ort.get_available_providers()
        if "CoreMLExecutionProvider" in providers:
            os.environ["ONNX_PROVIDER"] = "CoreMLExecutionProvider"
            print("Using provider: CoreMLExecutionProvider", file=sys.stderr, flush=True)
        else:
            print("Providers: %s (CoreML unavailable, using CPU)" % providers, file=sys.stderr, flush=True)
    except Exception as exc:
        print("onnxruntime probe failed: %s" % exc, file=sys.stderr, flush=True)

    from kokoro_onnx import Kokoro
    _kokoro = Kokoro(args.model, args.voices)

    # Release the GIL during synthesis so concurrent /tts requests can overlap
    # (onnxruntime Run() is thread-safe; ThreadingHTTPServer + threads).
    httpd = ThreadingHTTPServer((args.host, args.port), Handler)
    print("Kokoro TTS listening on %s:%d (voice=%s)" % (args.host, args.port, _voice), file=sys.stderr, flush=True)

    def _shutdown(signum, _frame):
        print("shutting down", file=sys.stderr, flush=True)
        threading.Thread(target=httpd.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, _shutdown)
    signal.signal(signal.SIGINT, _shutdown)
    httpd.serve_forever()


if __name__ == "__main__":
    sys.exit(main())
