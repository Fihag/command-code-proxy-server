# CommandCode Proxy Server

**English** | [简体中文](README.zh-CN.md)

OpenAI-compatible proxy server for the CommandCode API. It exposes `/v1/chat/completions` and `/v1/models` endpoints so OpenAI-compatible clients can call CommandCode models through a local HTTP server. Upstream traffic is shaped to match the official CLI's wire behaviour (TLS handshake, header order, session fields).

Repository: https://github.com/dev2k6/command-code-proxy-server

Version: `v1.1.0`

## Features

- OpenAI-compatible chat completions endpoint
- Streaming and non-streaming responses
- OpenAI-compatible model list endpoint
- Short model name mapping
- Optional default API key from CLI or environment (`COMMANDCODE_API_KEY`)
- Per-request API key via `Authorization` header
- Configurable host and port
- Checks GitHub tags for a newer proxy version and displays it next to the current version

## Requirements

- Go 1.26.2 or newer

## Run

```bash
go run main.go
```

Default server address:

```text
http://127.0.0.1:55990
```

## CLI options

```bash
go run main.go [options]
```

| Option | Default | Description |
| --- | --- | --- |
| `-host` | `127.0.0.1` | Host to bind the server to |
| `-port` | `55990` | Port to run the server on |
| `-api-key` | empty | Optional default CommandCode API key (also via `COMMANDCODE_API_KEY`) |
| `-project-slug` | derived from work dir | Value for the `x-project-slug` header |
| `-workdir` | home dir / `COMMANDCODE_WORKING_DIR` | Directory reported in the request `config` snapshot (and auto `x-project-slug`); relative paths are resolved to absolute |
| `-version` | `false` | Print version and exit |

> The `config` snapshot sent upstream contains the working directory name, its top-level file listing and git status. **Never launch the proxy from this repo's own directory** — every request would then report a project literally named `command-code-proxy-server` containing `tools/tap` and `recapture.mjs`, i.e. self-describing the proxy. It defaults to the user's home directory (a plain non-git folder, indistinguishable from running the CLI there); use `-workdir` / `COMMANDCODE_WORKING_DIR` to point it at any neutral project directory. `x-project-slug` is always derived from the same directory, so header and body agree.

Examples:

```bash
# Run on default host and port
go run main.go

# Run on a custom port
go run main.go -port 8080

# Expose on all interfaces
go run main.go -host 0.0.0.0

# Use a default API key for all requests that do not include Authorization
go run main.go -api-key your-commandcode-api-key

# Print version
go run main.go -version
```

## Build

Build for the current platform:

```bash
go build -trimpath -ldflags "-s -w" -o bin/command-code-proxy
```

Cross-compile the three shipped binaries:

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o bin/command-code-proxy.exe
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o bin/command-code-proxy
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o bin/command-code-proxy-arm64
```

## API key behavior

The proxy uses the API key in this order:

1. `Authorization` header from the incoming client request (a present-but-empty token such as `Bearer ` also falls back)
2. `-api-key` CLI value, or the `COMMANDCODE_API_KEY` environment variable
3. If neither exists, the request returns `401 Unauthorized`

> Command-line arguments appear in the process list and shell history. For a long-running service prefer the environment variable:
> `setx COMMANDCODE_API_KEY "..."` (takes effect in new terminals).

Header format:

```http
Authorization: Bearer your-commandcode-api-key
```

## Endpoints

### Health check

```http
GET /health
```

Response:

```json
{"status":"ok"}
```

### List models

```http
GET /v1/models
```

Returns an OpenAI-compatible model list.

### Chat completions

```http
POST /v1/chat/completions
```

Example non-streaming request:

```bash
curl http://127.0.0.1:55990/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-commandcode-api-key" \
  -d '{
    "model": "deepseek-v4-pro",
    "messages": [
      {"role": "system", "content": "You are helpful."},
      {"role": "user", "content": "Hello"}
    ],
    "stream": false
  }'
```

Example streaming request:

```bash
curl -N http://127.0.0.1:55990/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-commandcode-api-key" \
  -d '{
    "model": "deepseek-v4-pro",
    "messages": [
      {"role": "user", "content": "Write a short poem."}
    ],
    "stream": true
  }'
```

## Supported model aliases

The proxy accepts full model IDs, short names (`kimi-k2.5` → `moonshotai/Kimi-K2.5`), and punctuation-insensitive variants (`gemini38flash` → `google/gemini-3.8-flash`).

On startup and then every 6 hours, the proxy fetches the official model list from:

```text
https://commandcode.ai/docs/reference/cli/models
```

This is the same model registry that backs the Command Code CLI (`--list-models` / `/model` picker), so the proxy's `/v1/models` always reflects the latest available models without code changes. The fetched catalog is cached in memory; `MapModel` resolves against it first (exact id, short name, and punctuation-insensitive match), falling back to the static list below until the first fetch completes.

Built-in alias fallbacks (used before the first catalog fetch succeeds):

| Alias | Maps to |
| --- | --- |
| `deepseek-v4-pro`, `deepseek-v4`, `deepseek-pro` | `deepseek/deepseek-v4-pro` |
| `deepseek-v4-flash`, `deepseek-flash` | `deepseek/deepseek-v4-flash` |
| `minimax-m2.7`, `minimax2.7` | `MiniMaxAI/MiniMax-M2.7` |
| `minimax-m2.5`, `minimax2.5`, `minimax` | `MiniMaxAI/MiniMax-M2.5` |
| `glm-5.1` | `zai-org/GLM-5.1` |
| `glm-5` | `zai-org/GLM-5` |
| `kimi-k2.6`, `kimi2.6` | `moonshotai/Kimi-K2.6` |
| `kimi-k2.5`, `kimi2.5` | `moonshotai/Kimi-K2.5` |
| `qwen-3.6-max-preview`, `qwen3.6-max` | `Qwen/Qwen3.6-Max-Preview` |
| `qwen-3.6-plus`, `qwen3.6-plus`, `qwen3.6` | `Qwen/Qwen3.6-Plus` |
| `step-3.5-flash`, `step3.5` | `stepfun/Step-3.5-Flash` |
| `gemini-3.1-flash-lite`, `gemini-flash-lite` | `google/gemini-3.1-flash-lite` |
| `minimax-m3`, `minimax3` | `MiniMaxAI/MiniMax-M3` |
| `qwen-3.7-max-free`, `qwen3.7-max-free` | `Qwen/Qwen3.7-Max-Free` |
| `qwen-3.7-max`, `qwen3.7-max` | `Qwen/Qwen3.7-Max` |
| `step-3.7-flash`, `step3.7` | `stepfun/Step-3.7-Flash` |
| `mimo-v2.5-pro`, `mimo-pro` | `xiaomi/mimo-v2.5-pro` |
| `mimo-v2.5`, `mimo` | `xiaomi/mimo-v2.5` |

Unknown model names are passed through unchanged.

## Project structure

```text
.
├── README.md / README.zh-CN.md
├── go.mod / go.sum / main.go
├── bin
│   ├── command-code-proxy           (linux amd64)
│   ├── command-code-proxy-arm64     (linux arm64)
│   └── command-code-proxy.exe       (windows amd64)
├── internal
│   ├── api          (openai.go, commandcode.go — wire types)
│   ├── proxy        (proxy.go, convert.go, model.go, catalog.go,
│   │                 identity.go, envconfig.go, beacon.go, clitools.json)
│   ├── upstream     (transport.go, spec.go, node24_clienthello.bin)
│   ├── server       (server.go)
│   ├── update       (update.go — GitHub tag self-update notice)
│   └── version      (version.go — npm-resolved CLI version, frozen per process)
└── tools
    ├── tap          (transparent TLS capture probe)
    └── recapture.mjs (regenerate baselines from a capture)
```

## How it works

1. On first use with a key, the proxy replays the real CLI's startup sequence upstream (whoami → lifecycle-events → fingerprint/record) before forwarding any chat traffic, so every session has its birth events.
2. Client sends an OpenAI-compatible request to the local proxy.
3. The proxy extracts system messages, maps the model name, converts messages to CommandCode format, and fills the identity fields (`threadId`/`x-session-id`, `x-project-slug`, `config` snapshot) exactly like the CLI does; when the client sends no tools, the CLI's built-in tool set is injected.
4. The request goes to `https://api.commandcode.ai/alpha/generate` over a hand-rolled transport that replays the captured Node/OpenSSL TLS ClientHello (JA3/JA4) and emits HTTP/1.1 headers in the CLI's wire order.
5. CommandCode streaming NDJSON events are converted back to OpenAI-compatible SSE chunks (always terminated with `finish_reason` + `[DONE]`) or collected into a single JSON response; upstream errors surface as HTTP errors, never as fake successes.

## Version check

On startup and when running `-version`, the proxy calls:

```text
https://api.github.com/repos/dev2k6/command-code-proxy-server/tags
```

If the latest GitHub tag is newer than the current app version, the version line is displayed as:

```text
v1.1.0 (latest: v1.x.x)
```

## Impersonated CLI version

Upstream requests carry an `x-command-code-version` header (e.g. `1.53.0`), tracking the latest published CLI.

The value is **resolved once at process start** from npm (`command-code`'s
`latest`) and then **frozen for the whole process**. This mirrors the real
CLI, which force-updates itself at launch and then reports one exact version
consistently in its lifecycle beacon and every header — a version that changed
mid-run would contradict the `cliVersion` already sent in this process's
lifecycle-events beacon, the one cross-check a server can reliably make. The
lookup is bounded by a 5s timeout and does NOT sit on any request path (it
runs before serving), so a stalled registry can never hang a chat request —
unlike the old design that did a synchronous no-timeout `http.Get` on the
first request.

If the lookup fails (offline, blocked, malformed answer), the version falls
back to `internal/version.Baseline` — the command-code version whose TLS
handshake, header templates and tool set this proxy actually replays. Restart
the proxy to re-resolve; bump `Baseline` after a re-capture so the offline
fallback stays current.

## Re-capturing the fingerprint baseline (template-aging maintenance)

The TLS handshake fingerprint (`internal/upstream/node24_clienthello.bin`), the CLI built-in tool definitions (`internal/proxy/clitools.json`) and the device fingerprint report body (`fingerprint.json`, not committed) all come from one capture of the real CLI. The CLI auto-updates; once its bundled Node/OpenSSL changes, the baselines age and must be re-captured:

1. Resolve the real upstream IP (for the probe to forward to, avoiding a hosts loop):
   `nslookup api.commandcode.ai` → note any IPv4 (e.g. `172.67.167.23`).
2. As administrator, append to `C:\Windows\System32\drivers\etc\hosts`:
   `127.0.0.1 api.commandcode.ai`
3. Start the forwarding probe (transparently relays to the real upstream; the session proceeds normally):
   `go run ./tools/tap -listen 127.0.0.1:443 -upstream <IP>:443 -out capture.jsonl`
4. Run the real CLI once until it issues a generate request (this creates one real conversation, consuming a little quota):
   `set NODE_TLS_REJECT_UNAUTHORIZED=0 && command-code -p "Reply with exactly: OK" --no-session --skip-onboarding -m deepseek/deepseek-v4-flash`
5. **Immediately remove the hosts redirect line and stop the probe.**
6. Regenerate all three baselines in one step:
   `node tools/recapture.mjs capture.jsonl`
7. The script prints the captured `cliVersion` at the end. The live `x-command-code-version` header is resolved from npm at startup, but update `Baseline` in `internal/version/version.go` to this version as well: it is the offline fallback and the reference for the replayed behaviour, so it should not lag the new capture.
8. Re-run `go test ./...` (the transport test field-compares the handshake against the new baseline), then rebuild all three binaries.
