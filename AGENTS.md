# AGENTS.md — Clark Development Workflow

## Overview

This document defines the mandatory workflow for AI agents working on Clark.
Every task must follow the Issue → Plan → Implement → Test → Commit → Push cycle.

---

## 1. GitHub Issues as Task Management

All work tracked via GitHub Issues at `heyimteee/clark`.

### Issue lifecycle

```
Open → Plan (comment) → Implement → Commit → Close
```

### Issue body (problem statement)

Every issue must contain:

- **Title**: imperative mood, conventional commit prefix (`feat:`, `fix:`, `chore:`, `tune:`)
- **Problem**: what's broken or missing, with exact file:line references
- **Goal**: what "done" looks like
- **Scope**: what's in/out
- **Success Criteria**: checklist of verifiable conditions

### Issue comment (implementation plan)

The first comment on every issue must contain:

- Step-by-step implementation plan with code snippets
- File-by-file changes
- Test plan
- Verification steps

### Issue → Commit linking

Every commit that resolves an issue must reference it:

```
fix: description of change

Resolves #37
```

Or for multiple commits per issue, each commit references the issue number.

### Closing issues

Close the issue only after:
1. All commits are pushed
2. All tests pass (`gofmt -l .`, `go vet ./...`, `go test -race ./...`)
3. The success criteria in the issue are met

---

## 2. Commit Conventions

All commits follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>: <description>

[optional body]

[optional footer]
```

### Types

| Type | When to use |
|---|---|
| `feat` | New feature or capability |
| `fix` | Bug fix |
| `chore` | Maintenance, deps, config, no logic change |
| `tune` | Performance, optimization, tuning |
| `refactor` | Code restructuring without behavior change |
| `docs` | Documentation only |
| `test` | Adding or fixing tests |

### Rules

- Description: imperative mood, lowercase, no period, max 72 chars
- Body: wrap at 72 chars, explain **what** and **why** (not how)
- Reference issue: `Resolves #NNN` in footer
- No commits without passing tests
- No force-pushes to `main`

### Examples

```
feat: enable Ollama streaming for real-time token delivery

Stream tokens from Ollama as they are generated instead of waiting
for the full response. Enables early TTS on first sentence.

Resolves #38
```

```
fix: alert-mode toggle reads ON=voice / OFF=silent

The toggle logic was inverted — ON was mapping to silent mode
and OFF to voice mode.

Resolves #35
```

---

## 3. Testing Gates

Every commit must pass all three gates before push:

```bash
gofmt -l .            # must be empty (no unformatted files)
go vet ./...          # must pass (no static analysis errors)
go test -race ./...   # must pass (no data races, all tests green)
```

If any gate fails, fix before committing. Do not commit partially working code.

### JavaScript (SPA)

```bash
node --check internal/web/static/app.js    # syntax check
```

### Docker

```bash
docker compose build    # must build without errors
```

---

## 4. Deployment Pipeline

### Auto-deploy on push to main

Pushing to `main` triggers the GitHub Actions self-hosted runner on the server:

1. `git pull --ff-only`
2. `docker compose up -d --build`
3. Container restarts with new code

### Manual verification after deploy

After pushing, verify on the server:
1. `docker compose logs clark` — no startup errors
2. `curl -k https://clark.studio.lab/web/api/state` — API responds
3. Test the specific feature that changed

---

## 5. Release Process

### When to release

- After a batch of related features (e.g., voice engine work)
- After a significant bug fix
- When the user requests a release

### Release naming

Semantic versioning: `vMAJOR.MINOR.PATCH`

- **MAJOR**: breaking changes
- **MINOR**: new features (backwards compatible)
- **PATCH**: bug fixes

### Release steps

1. Verify all tests pass on `main`
2. Create annotated tag:
   ```bash
   git tag -a v5.3.0 -m "v5.3.0: description of release"
   ```
3. Push tag:
   ```bash
   git push origin v5.3.0
   ```
4. Create GitHub Release with changelog:
   ```bash
   gh release create v5.3.0 --title "v5.3.0 — Title" --notes "changelog"
   ```
5. Changelog groups commits by area (Voice, Alerts, UI, Infra)

---

## 6. Code Quality Standards

### Go

- Follow existing code style (no new frameworks)
- Use `gofmt` for formatting
- Use `go vet` for static analysis
- Use table-driven tests
- Use `context.Context` for cancellation
- Use `sync.Mutex` for shared state
- Prefer composition over inheritance
- Keep packages small and focused

### JavaScript (SPA)

- Vanilla JS, no frameworks, no build step
- Embedded via `go:embed`
- Use `fetch` for HTTP, `WebSocket` for real-time
- Keep the UI responsive (no blocking main thread)

### Docker

- Multi-stage builds (builder + runtime)
- Minimal runtime image (debian:bookworm-slim)
- Models baked in at build time (no runtime downloads)
- Graceful degradation (missing engines = unavailable, not crash)

---

## 7. Architecture Principles

- **Transport-neutral pipeline**: WhatsApp, iMessage, and web all feed through the same `gateway` → `assistant` pipeline
- **Interface-driven**: depend on interfaces (`STT`, `TTS`, `LLM`, `Messenger`), not concrete types
- **Nil-safe**: missing engines degrade gracefully, never crash
- **Daemon pattern**: long-lived processes for STT/TTS (FasterWhisper, Piper, Kokoro)
- **FailoverTTS**: primary → fallback with health gate (2 consecutive failures)

---

## 8. File Reference

| File | Purpose |
|---|---|
| `internal/voice/fasterwhisper.go` | STT engine (FasterWhisper) |
| `internal/voice/piper.go` | TTS daemon (Piper fallback) |
| `internal/voice/kokororemote.go` | TTS remote (Kokoro MLX on Mac) |
| `internal/ollama/ollama.go` | LLM client (Ollama) |
| `internal/assistant/assistant.go` | Butler service (brain) |
| `internal/web/chat.go` | WebSocket chat handler |
| `internal/web/voice.go` | STT/TTS HTTP handlers |
| `internal/web/static/app.js` | SPA frontend |
| `internal/alert/alert.go` | Alert delivery service |
| `internal/gateway/` | Transport-neutral pipeline |
| `cmd/imessage-bridge/` | macOS iMessage bridge |
| `docker/whisper_run.py` | FasterWhisper runner script |
| `Dockerfile` | Multi-stage container build |
| `docker-compose.yml` | Container orchestration |
| `.github/workflows/deploy.yml` | Auto-deploy on push |
