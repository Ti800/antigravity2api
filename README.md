# antigravity2api

A local proxy that exposes your own Antigravity subscription as an OpenAI- and Claude-compatible API. It is built to run inside iSH on iOS and to be consumed by Minis.

## What changed

This repository started as a proxy for the Gemini web app. That version authenticated with browser cookies, tracked a build label, captured a per-model ticket, and impersonated a browser's TLS fingerprint. None of that applies to Antigravity. Antigravity authenticates with Google OAuth and speaks plain JSON to the Cloud Code endpoint, so the web-app machinery is gone.

What carried over is the part that was measured on a device rather than assumed:

- one static binary, no runtime dependencies
- a cold start that makes no network request
- a service script that detects and skips zombie ports left behind when iOS reclaims the app
- no in-process watchdog, because nothing inside the process can outlive the app being killed

## Layout

The split follows OpenCodex, pointed the other way. OpenCodex routes a coding client out to arbitrary providers. This routes arbitrary clients in to one subscription.

| Package | Owns |
|---|---|
| `internal/translate` | Conversion between OpenAI or Claude requests and the Cloud Code envelope |
| `internal/upstream` | HTTP transport, SSE reading, project discovery |
| `internal/auth` | Account files and access-token refresh |
| `internal/server` | Routing, local authentication, account selection |

The inbound protocol and the upstream protocol do not import each other. Supporting another client format means adding a translator and a handler, and nothing else.

## Build

```sh
go build -trimpath -ldflags="-s -w" -o antigravity2api .
```

The only dependency is `github.com/google/uuid`. On iSH the bootstrap Go segfaults when it tries to hand off to a downloaded toolchain. Call the toolchain binary directly:

```sh
TC=$(ls -d /root/go/pkg/mod/golang.org/toolchain@*/ | head -1)
"$TC/bin/go" build -trimpath -ldflags="-s -w" -o antigravity2api .
```

The binary is about 6.3 MB. Idle heap is about 0.5 MB; total process size is about 12 MB.

## Configure

Token refresh needs the OAuth client id and secret. They are read from the environment, not compiled in:

```sh
export ANTIGRAVITY_CLIENT_ID=...
export ANTIGRAVITY_CLIENT_SECRET=...
```

`scripts/service.sh` sources a `.env` file in the same directory if one exists (exporting the
variables before launching the binary). That file is gitignored.

The IDE user agent is pinned in the binary (`antigravity/ide/2.5.5 (...)`); Google occasionally
gates newer models behind a later IDE version, so `ANTIGRAVITY_USER_AGENT` overrides it without
a rebuild.

Each account is a JSON file in `auth/`:

```json
{
  "email": "you@example.com",
  "refresh_token": "...",
  "project_id": ""
}
```

`project_id` can be left empty. It is discovered on the first request, provided the upstream call carries the IDE user agent `antigravity/ide/<version>`. A generic user agent is accepted for model listing and rejected for project discovery, which looks like an account that has no project. It does not.

Then:

```sh
cp config.example.json config.json
sh scripts/service.sh start
```

In Minis, add an OpenAI-compatible provider with base URL `http://127.0.0.1:8081` and appendV1Suffix enabled. Bind chats to a model group that has a fallback, not to a single local model. A single-model binding fails hard while the service is down.

`/var/minis` drops the executable bit. If the binary fails with `Permission denied`, copy it to `/tmp`, run `chmod +x`, and copy it back.

## Models

`GET /v1/models` returns the ids the upstream currently advertises (from
`fetchAvailableModels`; cached for ten minutes, with a small static fallback list if the fetch
fails). Request them verbatim — the current flash generation is tiered by wire id
(`gemini-3.8-flash-low` / `-medium` / `-high`), the pro tier is `gemini-pro-agent`, and
`claude-*` / `gpt-oss-*` ids pass through to those models.

A few legacy spellings resolve automatically so older configurations keep working:

| requested | resolves to |
|---|---|
| `gemini-3.8-flash` | `gemini-3.8-flash-medium` |
| `gemini-3.8-flash-thinking` | `gemini-3.8-flash-high` |
| `gemini-3.7-flash` / `gemini-3.6-flash` | the `-medium` tier of that generation |
| `gemini-3.1-pro` | `gemini-pro-agent` |

Replayed tool calls must carry a thought signature; when no captured signature exists, the
request fills the official `skip_thought_signature_validator` sentinel on the first
functionCall of the replayed model turn (Gemini-family models only).

## Endpoints

- `GET /healthz` — includes the pid, so the service script can tell its own process from anything else on the port
- `GET /v1/models`
- `POST /v1/chat/completions` — streaming, tool calls, and reasoning content
- `POST /v1/messages` — Claude-shaped input, served through the same path

## Limits on iOS

Killing the Minis app kills the whole environment. cron, nohup and any goroutine die with it, so there is no recovery short of running `sh scripts/service.sh start` again. Low Power Mode kills background processes within about two minutes. Turn it off if the service needs to outlive an app switch.

## License

MIT
