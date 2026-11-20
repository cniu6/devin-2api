# devin-2api

> [English](README.md) | **中文**

`devin-2api` 是一个把 [Devin](https://app.devin.ai/) / Windsurf 后端封装成 OpenAI 兼容协议的多协议网关。它对外同时提供 OpenAI Responses API、OpenAI Chat Completions API 和 Anthropic Messages API 三类接口，并内置一个 Web 管理面板。

## 特性

- **多协议兼容**：`POST /v1/responses`、`POST /v1/chat/completions`、`POST /v1/messages`、`POST /v1/audio/transcriptions`（语音转写），以及对应的 `/v1/models` 模型发现接口。
- **流式与一次性响应**：均支持 JSON 和 SSE 流式输出。
- **OpenAI Responses WebSocket**：支持 `GET /v1/responses` 升级为 WebSocket，供 Codex 等客户端使用。
- **工具调用（function calling）**：Responses / Chat / Anthropic 均支持函数工具。
- **推理/thinking 内容**：自动把上游思考过程映射到各协议对应字段。
- **图片输入**：支持 base64 data URL 形式的图片；Responses 兼容 `input_image` 字符串与 `image_url` 对象、`image_url` part 等写法。
- **管理面板**：`/panel`（默认关闭，`dashboard.enabled` 开启）查看模型分类、渠道/供应商、账户用量、促销倒计时与价格筛选；密码支持随机/固定/免密三种模式。
- **流控与连接模型**：`server.max_concurrency` 限制并发；`devin.force_http1` 默认强制 HTTP/1.1，避免 HTTP/2 单连接多 stream 被上游串行处理。
- **代理与分流**：默认直连；`devin.proxy` 作为兜底出口；`devin.proxy_pools` 支持任意命名的代理池（池内多地址轮询分摊并发）；`devin.routes` 按模型 uid/标签/系列/分组通配符把不同模型分流到不同池；支持 `http://`、`https://`、`socks5://`、`socks5h://`，可带认证；全部修改后重启生效。
- **请求级调试日志**：开启 `debug.enabled` 后在 `logs/` 目录下输出每请求的完整链路。

## 快速开始

### 1. 获取 Devin token

`devin-2api` 需要 Devin / Windsurf 的 session token，格式为 `devin-session-token$...`。

Windows（PowerShell 一键提取，无需安装 sqlite3，兼容 user/system 安装）：

```powershell
Get-ChildItem "$env:APPDATA","$env:LOCALAPPDATA" -Recurse -Filter state.vscdb -ErrorAction SilentlyContinue | ForEach-Object {
  try {
    $fs = [System.IO.File]::Open($_.FullName, 'Open', 'Read', 'ReadWrite')
    $text = (New-Object System.IO.StreamReader($fs)).ReadToEnd(); $fs.Close()
    if ($text -match 'devin-session-token\$[^\s"''\\,;(){}\[\]]+') { "{0}`t{1}" -f $Matches[0], $_.FullName }
  } catch {}
}
```

该命令递归扫描 `%APPDATA%` / `%LOCALAPPDATA%` 下所有 `state.vscdb`（Devin / Devin - Next / Windsurf / VS Code 等客户端的本地状态库，user 与 system 安装都会写这里），直接从库文件字节中正则提取 token 并打印来源路径，复制即用，无任何外部依赖。

若已安装 sqlite3，也可以精确查询：

```powershell
$state = "$env:APPDATA\devin\User\globalStorage\state.vscdb"
# 如果使用 Devin - Next 客户端，请尝试：
# $state = "$env:APPDATA\Devin - Next\User\globalStorage\state.vscdb"
sqlite3 $state "SELECT json_extract(value, '\$.apiKey') FROM ItemTable WHERE key='windsurfAuthStatus';"
```

macOS 示例：

```bash
sqlite3 ~/Library/"Application Support"/Devin/User/globalStorage/state.vscdb \
  "SELECT json_extract(value, '$.apiKey') FROM ItemTable WHERE key='windsurfAuthStatus';"
```

Linux 用户可以在对应应用目录的 `state.vscdb` 中查找同样的键，或用同样的字符串匹配思路提取。

### 2. 配置

```bash
cp config.example.yaml config.yaml
```

编辑 `config.yaml`，填入真实 token，其余字段示例值通常无需改动：

```yaml
server:
  listen: ":8080"           # HTTP 监听地址
  max_concurrency: 1024     # /v1/* 并发上限，默认 1024

devin:
  base_url: "https://server.codeium.com"
  token: "devin-session-token$..."   # 必填
  model: "glm-5-2"                   # 默认模型 UID，可按需修改
  # proxy: "socks5://127.0.0.1:1080" # 可选代理，重启生效
  force_http1: true                  # 默认强制 HTTP/1.1

dashboard:
  enabled: true             # 默认关闭；true 启用 /panel
  password: ""              # 留空 = 随机密码（见启动日志）；"none" = 免密

auth:
  api_key: ""               # /v1/* 访问密钥；留空不校验
```

### 3. 启动

本地：

```bash
go run ./cmd/devin-2api -config config.yaml
```

Docker：

```bash
docker run --rm -p 8080:8080 \
  -v "$PWD/config.yaml:/app/config.yaml" \
  leokun123/devin-2api --config /app/config.yaml
```

### 4. 验证

```bash
curl http://localhost:8080/healthz
# {"status":"ok"}
```

## 支持的 API 端点

| 端点 | 方法 | 说明 |
| --- | --- | --- |
| `/healthz` | GET | 健康检查 |
| `/v1/models` | GET | OpenAI 兼容模型列表 |
| `/v1/models/{model}` | GET | 单个模型信息 |
| `/v1/responses` | POST | OpenAI Responses API（JSON / typed SSE） |
| `/v1/responses` | GET | 升级 WebSocket（子协议 `responses_websockets=2026-02-06`） |
| `/v1/chat/completions` | POST | OpenAI Chat Completions API（JSON / SSE） |
| `/v1/messages` | POST | Anthropic Messages API（JSON / SSE） |
| `/v1/audio/transcriptions` | POST | OpenAI 兼容语音转写：multipart `file` 字段（wav/mp3/m4a，≤25MiB）→ `{"text": ...}` |
| `/panel` | GET | 管理面板（需 `dashboard.enabled: true`） |
| `/panel/login` | POST | 面板登录（需 `dashboard.enabled: true`） |
| `/panel/api/*` | GET | 面板数据 API（需 `dashboard.enabled: true`） |

`/v1/*` 端点默认无需鉴权。设置 `auth.api_key` 后，客户端需通过 `Authorization: Bearer <key>` 或 `X-Api-Key: <key>` 传递。

## 用法示例

### OpenAI Responses

非流式：

```bash
curl http://localhost:8080/v1/responses \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-api-key" \
  -d '{
    "model": "glm-5-2",
    "input": "你好"
  }'
```

流式：

```bash
curl -N http://localhost:8080/v1/responses \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5-2",
    "input": "你好",
    "stream": true
  }'
```

多模态（base64 data URL）：

```bash
curl http://localhost:8080/v1/responses \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5-2",
    "input": [
      {"type": "message", "role": "user", "content": [
        {"type": "input_text", "text": "描述这张图"},
        {"type": "input_image", "image_url": {"url": "data:image/png;base64,..."}}
      ]}
    ]
  }'
```

### OpenAI Chat Completions

非流式：

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5-2",
    "messages": [{"role": "user", "content": "你好"}]
  }'
```

流式并在最后返回 usage：

```bash
curl -N http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5-2",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true,
    "stream_options": {"include_usage": true}
  }'
```

多模态：

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5-2",
    "messages": [{"role": "user", "content": [
      {"type": "text", "text": "描述这张图"},
      {"type": "image_url", "image_url": {"url": "data:image/png;base64,..."}}
    ]}]
  }'
```

### Anthropic Messages

```bash
curl -N http://localhost:8080/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: your-api-key" \
  -d '{
    "model": "glm-5-2",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": [
      {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "..."}},
      {"type": "text", "text": "描述这张图"}
    ]}]
  }'
```

### 模型列表

```bash
curl http://localhost:8080/v1/models
```

## 配置说明

所有配置集中在 `config.yaml`，启动时加载一次，未知字段会被拒绝。

| 字段 | 说明 | 必填 |
| --- | --- | --- |
| `server.listen` | HTTP 监听地址 | 是 |
| `server.max_concurrency` | 同时处理的 `/v1/*` 请求数上限；0 使用默认值 1024 | 否 |
| `devin.base_url` | Devin Connect 上游地址 | 配置了 `devin.token` 后必填 |
| `devin.token` | Devin session token（`devin-session-token$...`） | 否；未设置时 `/v1/*` 返回 503 |
| `devin.model` | 默认 chat model UID（如 `glm-5-2`） | 配置了 `devin.token` 后必填 |
| `devin.proxy` | 上游代理地址；支持 `http/https/socks5/socks5h`，可带认证；对话流量兜底出口 + 面板数据出口；留空直连或走系统 `HTTP_PROXY`/`HTTPS_PROXY`；修改后重启生效 | 否 |
| `devin.proxy_pools` | 任意命名的代理池集合：`名字: [代理地址...]`；池内多地址按请求轮询分摊并发；保留名 `direct`/`default` | 否 |
| `devin.routes` | 分流规则列表：`- models: [通配符...] pool: 池名`；自上而下首个命中的规则生效，按模型 uid/标签/系列/分组匹配（`*`/`?`，忽略大小写）；未命中走 `devin.proxy` | 否 |
| `devin.force_http1` | 强制 HTTP/1.1，避免 HTTP/2 单连接多 stream 并发瓶颈 | 否；默认 `true` |
| `debug.enabled` | 在配置文件同目录 `logs/` 下写请求级调试日志 | 否 |
| `dashboard.enabled` | 是否启用 `/panel` 管理面板 | 否；默认 `false`（关闭） |
| `dashboard.password` | 面板密码：留空=随机密码（打印启动日志）、`none`=免密、其他=固定密码 | 否 |
| `auth.api_key` | `/v1/*` 接口访问密钥；留空不鉴权 | 否 |

完整示例：

```yaml
server:
  listen: ":8080"
  max_concurrency: 1024

devin:
  base_url: "https://server.codeium.com"
  token: "devin-session-token$..."
  model: "glm-5-2"
  force_http1: true

debug:
  enabled: false

dashboard:
  enabled: true
  password: ""

auth:
  api_key: ""
```

## 管理面板

面板默认关闭（`dashboard.enabled` 未设置或 `false` 时 `/panel` 返回 404）。开启后访问 `http://localhost:8080/panel`：

- `dashboard.enabled: true` 启用面板；需 `devin.token` 已配置，否则面板不启用（启动日志有提示）；
- `dashboard.password` 三种模式：
  - **留空（默认）**：每次启动生成随机密码并打印到启动日志，重启后变化；
  - **`"none"`**：免密开放，仅限可信本机/内网环境；
  - **其他字符串**：固定密码；
- 登录会话 cookie 24 小时有效；
- 面板分两个页签：**统计**（请求数/成功率/耗时/token 进出缓存用量、24h 柱状+折线图、最近 20 条请求含客户端 IP 与可展开详情、在线时长、模型 TOP）与**模型**（分类/渠道/促销图标悬浮详情/价格筛选与同系列合并）；促销模型名称旁的 🎁 图标悬浮显示起止、剩余与原价/现价；
- 最近请求点击行可展开 请求头（Authorization/X-Api-Key 等敏感头不记录）/请求体/响应，详情只存内存不落盘（≤48KB 截断）；
- 统计为进程内存聚合，每 5 分钟落盘到配置文件同目录 `stats.json`（存在即合并续计），退出时再保存一次；面板页每 15 秒自动刷新统计。

## 代理池与模型分流

上游按出口地区下发不同的模型目录：直连（国内）约 50 个模型；走美区代理则下发全量约 300 个（`claude-*`、`gpt-*`、`gemini-*`、`grok-*`、`muse-*` 等）。因此 `devin.proxy` 同时也是**目录出口**——想看到全量模型就把它设为美区代理，再用 `routes` 把低延迟需求的模型拉回直连：

```yaml
devin:
  proxy: "socks5://user:pass@us1.example.com:1080"  # 美区出口：全量目录 + 面板 + 未分流模型
  proxy_pools:                    # 动态命名，池内多地址按请求轮询（可选）
    us:
      - "socks5://user:pass@us1.example.com:1080"
      - "socks5://user:pass@us2.example.com:1080"
  routes:                         # 自上而下，首个命中生效
    - models: ["swe-*", "glm-*", "kimi-*", "deepseek-*", "nemotron-*", "qwen-*", "minimax-*"]
      pool: direct                # 国内可用模型 → 直连低延迟
      # 未命中的 claude-*/gpt-*/gemini-* 等 → 走 devin.proxy 美区兜底
```

反过来也行：`proxy` 留空（目录只显示国内可见模型），显式把美区模型路由到池中：

```yaml
  routes:
    - models: ["claude-*", "gpt-*", "gemini-*", "grok-*", "muse-*"]
      pool: us
```

- `models` 每条模式按模型 **uid / 显示名 / 系列(family) / 上游分组名** 匹配；支持 `*`（任意串）、`?`（单字符），忽略大小写；分组匹配需要目录已加载（首个请求自动拉取，失败时退化为 uid/标签/系列匹配）。
- `pool` 填 `proxy_pools` 中的名字；内置保留名：`direct`（直连）、`default`（= `devin.proxy`）。
- 规则仅作用于对话流量（`/v1/*`）；面板数据与模型目录始终走 `devin.proxy`。
- 池内多地址轮询分摊并发，适合"同一个出口多个入口"的场景；高延迟代理建议只承担少量模型。
- 修改后重启生效；代理 URL 可内嵌 `user:pass` 认证，注意 `config.yaml` 权限、勿提交凭据。

## 注意事项

- `devin.token` 为空时，`/v1/*` 返回 `503 provider_configuration`；
- 鉴权头同时支持 `Authorization: Bearer <key>` 和 `X-Api-Key: <key>`；
- token 等敏感字段在调试日志中会被脱敏为 `<redacted>`；
- 图片只支持 base64 data URL，不支持外部 URL、`file_id` 或本地文件路径；
- 每轮请求仍需携带完整消息历史；`previous_response_id` 字段会被接收，但当前不会启用服务端状态保存；
- `config.yaml` 已加入 `.gitignore`；即使示例文件被 git 追踪，真实 token 也不要提交；
- 请求体上限为 32 MiB，满足多图 base64 场景。

## 文档

- **架构、API 字段子集、proto 提取等技术细节**：[Contributing guide](CONTRIBUTING.md)
- **开源协议**：[MIT](LICENSE)