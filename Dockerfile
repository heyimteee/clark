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
# TTS voice model (en_US-amy-medium, ~60 MB) + its config, baked in so the
# container never phones home at runtime.
FROM debian:bookworm-slim AS piper-download

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /opt/piper/voices \
    && curl -fsSL -o /opt/piper/voices/en_US-amy-medium.onnx \
        https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/amy/medium/en_US-amy-medium.onnx \
    && curl -fsSL -o /opt/piper/voices/en_US-amy-medium.onnx.json \
        https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/amy/medium/en_US-amy-medium.onnx.json

# ---- runtime stage ----
# bookworm-slim (glibc) because piper's binaries are glibc-linked and fail on
# alpine/musl. `pip install piper-tts` bundles the ONNX runtime and espeak-ng
# phonemization, so no extra system packages are needed for TTS.
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata python3 python3-pip \
    && pip3 install --no-cache-dir --break-system-packages piper-tts \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/clark /usr/local/bin/clark
COPY --from=piper-download /opt/piper /opt/piper
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

ENV CLARK_DB=/data/clark.db
ENV PIPER_BIN=/usr/local/bin/piper
ENV PIPER_VOICE=/opt/piper/voices/en_US-amy-medium.onnx
WORKDIR /data
VOLUME /data

ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["run"]
