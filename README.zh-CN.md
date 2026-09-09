# CommandCode 代理服务

[English](README.md) | **简体中文**

面向 CommandCode API 的 OpenAI 兼容代理服务。暴露 `/v1/chat/completions` 与 `/v1/models` 端点，让 OpenAI 兼容客户端可以通过本地 HTTP 服务调用 CommandCode 模型。发往上游的流量会按官方 CLI 的线上行为整形（TLS 握手、请求头顺序、会话字段）。

仓库：https://github.com/dev2k6/command-code-proxy-server

版本：`v1.1.0`

## 功能特性

- OpenAI 兼容的对话补全端点
- 流式与非流式响应
- OpenAI 兼容的模型列表端点
- 模型短名映射
- 可通过命令行参数或环境变量（`COMMANDCODE_API_KEY`）设置默认 API key
- 每请求独立的 API key（走 `Authorization` 头）
- 可配置监听地址与端口
- 启动时检查 GitHub tag 是否有新版代理，并在版本号旁提示

## 环境要求

- Go 1.26.2 或更新

## 运行

```bash
go run main.go
```

默认服务地址：

```text
http://127.0.0.1:55990
```

## 命令行参数

```bash
go run main.go [options]
```

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `-host` | `127.0.0.1` | 服务绑定地址 |
| `-port` | `55990` | 监听端口 |
| `-api-key` | 空 | 可选的默认 CommandCode API key（也可用 `COMMANDCODE_API_KEY`） |
| `-project-slug` | 由工作目录推导 | `x-project-slug` 头的取值 |
| `-workdir` | 主目录 / `COMMANDCODE_WORKING_DIR` | 上报给上游的 `config` 快照工作目录（并推导自动 slug）；相对路径会解析为绝对路径 |
| `-version` | `false` | 打印版本号后退出 |

> 上报的 `config` 快照包含工作目录名、顶层文件列表与 git 状态。**切勿用本代理仓库目录启动**——否则每个请求都会上报一个叫 `command-code-proxy-server`、内含 `tools/tap`、`recapture.mjs` 的工程，等于自曝用途。默认取用户主目录（普通非 git 目录，与在 home 下运行 CLI 无异），或用 `-workdir` / `COMMANDCODE_WORKING_DIR` 指向任意中性项目目录；`x-project-slug` 始终由该目录推导，header 与 body 一致。

示例：

```bash
# 默认地址端口运行
go run main.go

# 自定义端口
go run main.go -port 8080

# 暴露到所有网卡
go run main.go -host 0.0.0.0

# 为不带 Authorization 的请求设置默认 key
go run main.go -api-key your-commandcode-api-key

# 打印版本
go run main.go -version
```

## 构建

当前平台构建：

```bash
go build -trimpath -ldflags "-s -w" -o bin/command-code-proxy
```

交叉编译三个入库产物：

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o bin/command-code-proxy.exe
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o bin/command-code-proxy
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o bin/command-code-proxy-arm64
```

## API key 行为

代理按以下顺序取用 API key：

1. 客户端请求的 `Authorization` 头（带了但是空的 token，如 `Bearer `，也会回退）
2. `-api-key` 命令行值，或 `COMMANDCODE_API_KEY` 环境变量
3. 两者都没有时，请求返回 `401 Unauthorized`

> 命令行参数会出现在进程列表与 shell 历史里；常驻服务建议改用环境变量：
> `setx COMMANDCODE_API_KEY "..."`（新终端生效）。

请求头格式：

```http
Authorization: Bearer your-commandcode-api-key
```

## 端点

### 健康检查

```http
GET /health
```

响应：

```json
{"status":"ok"}
```

### 模型列表

```http
GET /v1/models
```

返回 OpenAI 兼容的模型列表。

### 对话补全

```http
POST /v1/chat/completions
```

非流式请求示例：

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

流式请求示例：

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

## 支持的模型别名

代理接受完整模型 ID、短名（`kimi-k2.5` → `moonshotai/Kimi-K2.5`）以及忽略标点的变体（`gemini38flash` → `google/gemini-3.8-flash`）。

代理在启动时以及此后每 6 小时，从下列地址拉取官方模型列表：

```text
https://commandcode.ai/docs/reference/cli/models
```

这与 Command Code CLI 的模型注册表（`--list-models` / `/model` 选择器）同源，因此 `/v1/models` 无需改代码即可始终反映最新模型。拉取结果缓存在内存中；`MapModel` 优先按目录解析（完整 id、短名、忽略标点匹配），首次拉取完成前退回到下表的静态别名。

内置别名回退（首次目录拉取成功前使用）：

| 别名 | 映射到 |
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

未知模型名原样透传。

## 项目结构

```text
.
├── README.md / README.zh-CN.md
├── go.mod / go.sum / main.go
├── bin
│   ├── command-code-proxy           (linux amd64)
│   ├── command-code-proxy-arm64     (linux arm64)
│   └── command-code-proxy.exe       (windows amd64)
├── internal
│   ├── api          (openai.go, commandcode.go — 线格式类型)
│   ├── proxy        (proxy.go, convert.go, model.go, catalog.go,
│   │                 identity.go, envconfig.go, beacon.go, clitools.json)
│   ├── upstream     (transport.go, spec.go, node24_clienthello.bin)
│   ├── server       (server.go)
│   ├── update       (update.go — GitHub tag 新版本提示)
│   └── version      (version.go — 钉死的 CLI 版本基线)
└── tools
    ├── tap          (透明 TLS 捕获探针)
    └── recapture.mjs (从捕获文件再生成基准)
```

## 工作原理

1. 拿到 key 后的第一次使用前，代理会先向上游重放真实 CLI 的启动序列（whoami → lifecycle-events → fingerprint/record），然后才转发对话流量，保证每个会话都有出生事件。
2. 客户端向本地代理发送 OpenAI 兼容请求。
3. 代理提取 system 消息、映射模型名、把消息转换为 CommandCode 格式，并按 CLI 的方式填充身份字段（`threadId`/`x-session-id`、`x-project-slug`、`config` 快照）；客户端没传 tools 时注入 CLI 内置工具集。
4. 请求经手写传输层发往 `https://api.commandcode.ai/alpha/generate`：回放捕获的 Node/OpenSSL TLS ClientHello（JA3/JA4），HTTP/1.1 头按 CLI 的线上顺序输出。
5. CommandCode 的流式 NDJSON 事件转回 OpenAI 兼容 SSE 块（永远以 `finish_reason` + `[DONE]` 收尾），或聚合为单个 JSON 响应；上游错误如实映射为 HTTP 错误，绝不伪装成功。

## 新版本检查

启动与 `-version` 时，代理会请求：

```text
https://api.github.com/repos/dev2k6/command-code-proxy-server/tags
```

若最新 GitHub tag 比当前版本新，版本行显示为：

```text
v1.1.0 (latest: v1.x.x)
```

## 模拟的 CLI 版本

上游请求携带：

```http
x-command-code-version: 1.50.1
```

该值**钉死**为 `internal/version.Baseline`——即本代理所回放行为（TLS 握手、头模板、工具集）对应的 command-code 版本。刻意不再运行时查 npm：冻结的 1.50.1 行为配一个实时的 "latest" 版本头，是服务端可交叉验证的矛盾；旧的运行时查找还无超时地挂在首个请求路径上。重新捕获基准时（下节）一并更新此常量。

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
7. 脚本末尾会打印捕获到的 `cliVersion`。若与 `internal/version/version.go` 的 `Baseline` 常量不同，**必须同步修改**：`x-command-code-version` 报的版本要与 TLS/头/工具所回放的行为版本一致，否则"新版 CLI 却发旧版行为"会被服务端交叉验证出来。
8. 重跑 `go test ./...`（测试会将传输层握手与该基准逐字段比对），然后重新构建三个二进制。
