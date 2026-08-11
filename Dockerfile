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

# ---- runtime stage ----
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=build /out/clark /usr/local/bin/clark
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

ENV CLARK_DB=/data/clark.db
WORKDIR /data
VOLUME /data

ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["run"]
