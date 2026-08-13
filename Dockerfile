# ---- build stage ----
# clark links against CGO for SQLite (mattn/go-sqlite3), so the build stage
# needs a C compiler. The result is a fully static binary.
FROM golang:1.26-alpine AS build

RUN apk add --no-cache gcc musl-dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -linkmode external -extldflags -static" -o /out/clark .

# ---- piper stage ----
# TTS voice model (en_US-ryan-high, ~120 MB, unmistakably male) + its config,
# baked in so the container never phones home at runtime.
FROM debian:bookworm-slim AS piper-download

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /opt/piper/voices \
    && curl -fsSL -o /opt/piper/voices/en_US-ryan-high.onnx \
        https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/ryan/high/en_US-ryan-high.onnx \
    && curl -fsSL -o /opt/piper/voices/en_US-ryan-high.onnx.json \
        https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/ryan/high/en_US-ryan-high.onnx.json

# ---- runtime stage ----
# bookworm-slim (glibc) because piper's binaries are glibc-linked and fail on
# alpine/musl. `pip install piper-tts` bundles the ONNX runtime and espeak-ng
# phonemization; faster-whisper provides STT on the CPU (int8). Whisper tiny
# (~75 MB) is baked in at build so the container never phones home at runtime.
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata python3 python3-pip espeak-ng curl \
    && pip3 install --no-cache-dir --break-system-packages piper-tts faster-whisper kokoro-onnx misaki[en] \
    && rm -rf /var/lib/apt/lists/*

# Bake the faster-whisper small model (downloaded from HF at build time):
# multilingual, ~461 MB, noticeably better digit/word accuracy than tiny and
# still fast on the CPU. Handles long-form speech well.
RUN python3 -c "from huggingface_hub import snapshot_download; snapshot_download('Systran/faster-whisper-small', local_dir='/opt/whisper/model')"

# Bake the Kokoro TTS model + all voice vectors (int8, ~88 MB) at build time.
RUN mkdir -p /opt/kokoro/model \
    && curl -fsSL -o /opt/kokoro/model/kokoro-v1.0.int8.onnx \
        https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/kokoro-v1.0.int8.onnx \
    && curl -fsSL -o /opt/kokoro/model/voices-v1.0.bin \
        https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/voices-v1.0.bin

COPY --from=build /out/clark /usr/local/bin/clark
COPY --from=piper-download /opt/piper /opt/piper
COPY docker/whisper_run.py /opt/whisper/run.py
COPY docker/piper_daemon.py /opt/piper/daemon.py
COPY docker/kokoro_daemon.py /opt/kokoro/daemon.py
COPY docker/gen_affirmations.py /opt/kokoro/gen_affirmations.py
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# Pre-render the wake-word affirmations and the "Processing, Sir." clip with the
# default Kokoro voice (am_michael), so the browser plays them instantly with
# zero server latency.
RUN mkdir -p /opt/affirmations \
    && python3 /opt/kokoro/gen_affirmations.py \
        /opt/kokoro/model/kokoro-v1.0.int8.onnx \
        /opt/kokoro/model/voices-v1.0.bin \
        am_michael /opt/affirmations

# Generate the ambient "AI thinking" idle tone (seamlessly loopable sine).
COPY docker/gen_idle.py /opt/affirmations/gen_idle.py
RUN python3 /opt/affirmations/gen_idle.py /opt/affirmations/idle.wav \
    && rm /opt/affirmations/gen_idle.py

ENV CLARK_DB=/data/clark.db
ENV PIPER_DAEMON=/opt/piper/daemon.py
ENV PIPER_VOICE=/opt/piper/voices/en_US-ryan-high.onnx
ENV KOKORO_DAEMON=/opt/kokoro/daemon.py
ENV KOKORO_MODEL=/opt/kokoro/model/kokoro-v1.0.int8.onnx
ENV KOKORO_VOICES=/opt/kokoro/model/voices-v1.0.bin
ENV KOKORO_VOICE=am_michael
ENV WHISPER_SCRIPT=/opt/whisper/run.py
ENV WHISPER_MODEL_DIR=/opt/whisper/model
ENV AFFIRMATIONS_DIR=/opt/affirmations
WORKDIR /data
VOLUME /data

ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["run"]
