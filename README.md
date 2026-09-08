# CommandCode Proxy Server

OpenAI-compatible proxy server for the CommandCode API. It exposes `/v1/chat/completions` and `/v1/models` endpoints so OpenAI-compatible clients can call CommandCode models through a local HTTP server.

Repository: https://github.com/dev2k6/command-code-proxy-server

Version: `v1.0.8`

## Features

- OpenAI-compatible chat completions endpoint
- Streaming and non-streaming responses
- OpenAI-compatible model list endpoint
- Short model name mapping
- Optional default API key from CLI
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
| `-api-key` | empty | Optional default CommandCode API key |
| `-project-slug` | current dir name | Value for the `x-project-slug` header (the CLI reports its project directory name) |
| `-version` | `false` | Print version and exit |

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
go build -o bin/command-code-proxy
```

Cross-compile for Windows and Linux:

```bash
GOOS=windows GOARCH=amd64 go build -o bin/command-code-proxy.exe
GOOS=linux GOARCH=amd64 go build -o bin/command-code-proxy
```

## API key behavior

The proxy uses the API key in this order:

1. `Authorization` header from the incoming client request
2. `-api-key` CLI value, or the `COMMANDCODE_API_KEY` environment variable
3. If neither exists, the request returns `401 Unauthorized`

> 命令行参数会出现在进程列表与 shell 历史里；常驻服务建议改用环境变量：
> `setx COMMANDCODE_API_KEY "..."`（新终端生效）。

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
├── README.md
├── go.mod
├── go.sum
├── main.go
├── bin
│   ├── command-code-proxy
│   └── command-code-proxy.exe
└── internal
    ├── api
    │   ├── commandcode.go
    │   └── openai.go
    ├── proxy
    │   ├── catalog.go
    │   ├── convert.go
    │   ├── model.go
    │   └── proxy.go
    ├── server
    │   └── server.go
    ├── update
    │   └── update.go
    └── version
        └── version.go
```

## How it works

1. Client sends an OpenAI-compatible request to the local proxy.
2. The proxy extracts system messages, maps the model name, and converts messages to CommandCode format.
3. The proxy sends the request to `https://api.commandcode.ai/alpha/generate`.
4. CommandCode streaming NDJSON events are converted back to OpenAI-compatible SSE chunks or collected into a single JSON response.

## Version check

On startup and when running `-version`, the proxy calls:

```text
https://api.github.com/repos/dev2k6/command-code-proxy-server/tags
```

If the latest GitHub tag is newer than the current app version, the version line is displayed as:

```text
v1.0.8 (latest: v1.x.x)
```

## CommandCode version header

The upstream request includes:

```http
x-command-code-version: <latest npm command-code version>
```

The value is fetched from:

```text
https://registry.npmjs.org/command-code/latest
```

The fetched version is cached for 30 minutes. If the registry request fails, the proxy uses the last cached version, or `unknown` if no version has been fetched yet.

## 重捕获指纹基准（模板老化维护）

本代理的 TLS 握手指纹（`internal/upstream/node24_clienthello.bin`）、CLI 内置工具定义（`internal/proxy/clitools.json`）与设备指纹上报体（`fingerprint.json`，不入库）都来自真实 CLI 的一次捕获。CLI 会自动更新，捆绑的 Node/OpenSSL 版本变化后基准会老化，此时按下列步骤重捕：

1. 解析真实上游 IP（供探针转发用，避免 hosts 回环）：
   `nslookup api.commandcode.ai` → 记下任一 IPv4（如 `172.67.167.23`）。
2. 以管理员在 `C:\Windows\System32\drivers\etc\hosts` 末尾添加：
   `127.0.0.1 api.commandcode.ai`
3. 启动转发探针（会透明转发到真实上游，会话正常进行）：
   `go run ./tools/tap -listen 127.0.0.1:443 -upstream <IP>:443 -out capture.jsonl`
4. 跑一次真 CLI 直到发出 generate 请求（会产生一次真实对话，消耗少量额度）：
   `set NODE_TLS_REJECT_UNAUTHORIZED=0 && command-code -p "Reply with exactly: OK" --no-session --skip-onboarding -m deepseek/deepseek-v4-flash`
5. **立刻删除 hosts 里的重定向行并停掉探针。**
6. 一键回灌三份基准数据：
   `node tools/recapture.mjs capture.jsonl`
7. 重跑 `go test ./...`（测试会将传输层握手与该基准逐字段比对），然后重新构建二进制。
