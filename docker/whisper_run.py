#!/usr/bin/env python3
"""faster-whisper transcription daemon for clark's STT seam.

Long-lived process that reads framed audio on stdin and writes framed
transcripts on stdout. The model is loaded once at startup and stays
resident, eliminating the cold-start penalty of spawning a new Python
process per request.

Protocol (same as Piper daemon):
  request:  [u32 little-endian length][WAV bytes]
  response: [u32 little-endian length][UTF-8 transcript bytes]

Prints "ready" to stderr when the model is loaded and the daemon is
ready to accept requests.
"""
import io
import struct
import sys

from faster_whisper import WhisperModel


def read_frame(stream):
    """Read a length-prefixed frame from the stream."""
    head = stream.read(4)
    if len(head) < 4:
        return None
    n = struct.unpack("<I", head)[0]
    if n == 0:
        return b""
    return stream.read(n)


def write_frame(stream, data):
    """Write a length-prefixed frame to the stream."""
    stream.write(struct.pack("<I", len(data)))
    stream.write(data)
    stream.flush()


def main():
    if len(sys.argv) < 2:
        print("usage: whisper_run.py <model-dir>", file=sys.stderr)
        return 2

    model_dir = sys.argv[1]
    model = WhisperModel(model_dir, device="cpu", compute_type="int8")

    # Signal ready — Go daemon reads this to know the model is loaded.
    print("ready", file=sys.stderr, flush=True)

    while True:
        wav = read_frame(sys.stdin.buffer)
        if wav is None:
            # stdin closed — parent process is gone.
            break
        if not wav:
            write_frame(sys.stdout.buffer, b"")
            continue

        segments, _info = model.transcribe(io.BytesIO(wav), language="en")
        text = " ".join(s.text.strip() for s in segments)
        write_frame(sys.stdout.buffer, text.encode("utf-8"))

    return 0


if __name__ == "__main__":
    sys.exit(main())
