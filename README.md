# devin-2api

> **English** | [中文](README.zh-CN.md)

`devin-2api` is a multi-protocol gateway that wraps the [Devin](https://app.devin.ai/) / Windsurf backend behind an OpenAI-compatible API. It exposes OpenAI Responses API, OpenAI Chat Completions API, and Anthropic Messages API endpoints, and includes a built-in web dashboard.

## Features

- **Multi-protocol support**: `POST /v1/responses`, `POST /v1/chat/completions`, `POST /v1/messages`, `POST /v1/audio/transcriptions` (speech-to-text), plus `/v1/models` model discovery.
- **Streaming and non-streaming** responses in JSON and SSE.
- **OpenAI Responses WebSocket**: `GET /v1/responses` upgrades to WebSocket for clients such as Codex.
- **Function calling**: supported across Responses, Chat, and Anthropic protocols.
- **Reasoning / thinking content**: upstream thinking is mapped to the corresponding fields of each protocol.
- **Image input**: supports base64 data URL images; Responses also accepts `input_image` as a string or `image_url` object, as well as `image_url` parts.
- **Dashboard**: `/panel` (off by default, enable via `dashboard.enabled`) shows model categories, providers, account usage, promo countdowns, and price filters; password supports random / fixed / open modes.
- **Concurrency and connection model**: `server.max_concurrency` limits concurrent `/v1/*` requests; `devin.force_http1` defaults to `true` to avoid HTTP/2 multi-stream serialization by the upstream.
- **Proxy & routing**: direct by default; `devin.proxy` is the fallback egress; `devin.proxy_pools` defines arbitrarily named pools (multiple addresses per pool, per-request round-robin to spread concurrency); `devin.routes` maps models to pools via uid/label/family/group wildcards; `http://`, `https://`, `socks5://`, `socks5h://` with optional auth; all changes take effect on restart.
- **Request-level debug logs**: enable with `debug.enabled`; logs each request lifecycle under `logs/`.

## How it works

```mermaid
flowchart TB
    subgraph Clients
        CC[Claude Code / Z Code]
        CX[Codex]
        OAI[OpenAI-compatible tools]
    end

    CC -->|POST /v1/messages| GATE
    CX -->|POST /v1/responses| GATE
    OAI -->|"POST /v1/chat/completions<br/>/v1/audio/transcriptions"| GATE

    subgraph GW["devin-2api"]
        GATE["API key auth +<br/>concurrency gate"]
        DEC["protocol decode<br/>→ llm.RequestMessages"]
        ROUTE{"model route<br/>devin.routes wildcards"}
        ADP["Devin adapter<br/>gRPC-Web"]
        ENC["protocol encode<br/>JSON / SSE / WS"]
        RSP[("response store<br/>previous_response_id")]
        GATE --> DEC --> ROUTE --> ADP --> ENC
        DEC -.->|history inject| RSP
        ENC -.usage.- STATS[("stats collector<br/>→ stats.json")]
    end

    ROUTE -->|direct| P1["domestic egress<br/>low latency"]
    ROUTE -->|named pool| P2["proxy pool<br/>round-robin"]
    ROUTE -->|default| P3["devin.proxy<br/>fallback"]
    P1 & P2 & P3 --> UP[("server.codeium.com")]

    PANEL["/panel dashboard<br/>models · quotas · promos · stats"] -.read.- STATS
```

The upstream catalog differs by egress region (~50 models direct vs ~300 via a US egress), so models are routed per-request: latency-sensitive models go direct, region-locked models go through named proxy pools.

## Quick start

### 1. Get a Devin token

`devin-2api` needs a Devin / Windsurf session token in the `devin-session-token$...` format.

On Windows (one-shot PowerShell, no sqlite3 required, works for user/system installs):

```powershell
Get-ChildItem "$env:APPDATA","$env:LOCALAPPDATA" -Recurse -Filter state.vscdb -ErrorAction SilentlyContinue | ForEach-Object {
  try {
    $fs = [System.IO.File]::Open($_.FullName, 'Open', 'Read', 'ReadWrite')
    $text = (New-Object System.IO.StreamReader($fs)).ReadToEnd(); $fs.Close()
    if ($text -match 'devin-session-token\$[^\s"''\\,;(){}\[\]]+') { "{0}`t{1}" -f $Matches[0], $_.FullName }
  } catch {}
}
```

This recursively scans every `state.vscdb` under `%APPDATA%` / `%LOCALAPPDATA%` (the local state DB used by Devin / Devin - Next / Windsurf / VS Code clients — both user and system installs write there), regex-extracts the token straight from the file bytes, and prints it together with the source path. Copy-paste and go, zero dependencies.

If you do have sqlite3 installed, a precise query works too:

```powershell
$state = "$env:APPDATA\devin\User\globalStorage\state.vscdb"
# If using the Devin - Next client, try:
# $state = "$env:APPDATA\Devin - Next\User\globalStorage\state.vscdb"
sqlite3 $state "SELECT json_extract(value, '\$.apiKey') FROM ItemTable WHERE key='windsurfAuthStatus';"
```

On macOS:

```bash
sqlite3 ~/Library/"Application Support"/Devin/User/globalStorage/state.vscdb \
  "SELECT json_extract(value, '$.apiKey') FROM ItemTable WHERE key='windsurfAuthStatus';"
```

Linux users can look for the same key in the equivalent `state.vscdb` path, or extract the token with the same string-matching approach.

### 2. Configure

```bash
cp config.example.yaml config.yaml
```

Edit `config.yaml` and set the real token; the other fields can usually stay at their example values:

```yaml
server:
  listen: ":8080"          # HTTP listen address
  max_concurrency: 1024    # /v1/* concurrency limit, default 1024

devin:
  base_url: "https://server.codeium.com"
  token: "devin-session-token$..."  # required
  model: "glm-5-2"                  # default model UID
  # proxy: "socks5://127.0.0.1:1080" # optional proxy, effective on restart
  force_http1: true                 # force HTTP/1.1 by default

dashboard:
  enabled: true            # off by default; true enables /panel
  password: ""             # empty = random password (see startup log); "none" = open

auth:
  api_key: ""              # /v1/* API key; empty = no auth
```

### 3. Run

Local:

```bash
go run ./cmd/devin-2api -config config.yaml
```

Docker:

```bash
docker run --rm -p 8080:8080 \
  -v "$PWD/config.yaml:/app/config.yaml" \
  leokun123/devin-2api --config /app/config.yaml
```

### 4. Verify

```bash
curl http://localhost:8080/healthz
# {"status":"ok"}
```

## Supported API endpoints

| Endpoint | Method | Description |
| --- | --- | --- |
| `/healthz` | GET | Health check |
| `/v1/models` | GET | OpenAI-compatible model list |
| `/v1/models/{model}` | GET | Single model info |
| `/v1/responses` | POST | OpenAI Responses API (JSON / typed SSE) |
| `/v1/responses` | GET | WebSocket upgrade (subprotocol `responses_websockets=2026-02-06`) |
| `/v1/chat/completions` | POST | OpenAI Chat Completions API (JSON / SSE) |
| `/v1/messages` | POST | Anthropic Messages API (JSON / SSE) |
| `/v1/audio/transcriptions` | POST | OpenAI-compatible speech-to-text: multipart `file` field (wav/mp3/m4a, ≤25MiB) → `{"text": ...}` |
| `/panel` | GET | Dashboard (requires `dashboard.enabled: true`) |
| `/panel/login` | POST | Dashboard login (requires `dashboard.enabled: true`) |
| `/panel/api/*` | GET | Dashboard data APIs (requires `dashboard.enabled: true`) |

`/v1/*` endpoints are open by default. Set `auth.api_key` and send it via `Authorization: Bearer <key>` or `X-Api-Key: <key>`.

## Usage examples

### OpenAI Responses

Non-streaming:

```bash
curl http://localhost:8080/v1/responses \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-api-key" \
  -d '{
    "model": "glm-5-2",
    "input": "Hello"
  }'
```

Streaming:

```bash
curl -N http://localhost:8080/v1/responses \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5-2",
    "input": "Hello",
    "stream": true
  }'
```

Multimodal (base64 data URL):

```bash
curl http://localhost:8080/v1/responses \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5-2",
    "input": [
      {"type": "message", "role": "user", "content": [
        {"type": "input_text", "text": "Describe this image"},
        {"type": "input_image", "image_url": {"url": "data:image/png;base64,..."}}
      ]}
    ]
  }'
```

### OpenAI Chat Completions

Non-streaming:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5-2",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

Streaming with usage at the end:

```bash
curl -N http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5-2",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true,
    "stream_options": {"include_usage": true}
  }'
```

Multimodal:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5-2",
    "messages": [{"role": "user", "content": [
      {"type": "text", "text": "Describe this image"},
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
      {"type": "text", "text": "Describe this image"}
    ]}]
  }'
```

### Model list

```bash
curl http://localhost:8080/v1/models
```

## Configuration

All settings live in `config.yaml`, loaded once at startup; unknown fields are rejected.

| Field | Description | Required |
| --- | --- | --- |
| `server.listen` | HTTP listen address | Yes |
| `server.max_concurrency` | Max concurrent `/v1/*` requests; `0` uses default 1024 | No |
| `devin.base_url` | Devin Connect upstream URL | Required once `devin.token` is set |
| `devin.token` | Devin session token (`devin-session-token$...`) | No; `/v1/*` returns 503 if unset |
| `devin.model` | Default chat model UID (e.g. `glm-5-2`) | Required once `devin.token` is set |
| `devin.proxy` | Upstream proxy URL; `http/https/socks5/socks5h` with optional auth; fallback egress for chat traffic + panel data egress; empty = direct or system `HTTP_PROXY`/`HTTPS_PROXY`; effective on restart | No |
| `devin.proxy_pools` | Arbitrarily named proxy pools: `name: [urls...]`; addresses inside a pool rotate per request to spread concurrency; reserved names: `direct`/`default` | No |
| `devin.routes` | Routing rules: `- models: [patterns...] pool: name`; first match wins; matched against model uid/label/family/upstream group (`*`/`?`, case-insensitive); unmatched models use `devin.proxy` | No |
| `devin.force_http1` | Force HTTP/1.1 to avoid HTTP/2 multi-stream serialization | No; defaults to `true` |
| `debug.enabled` | Write per-request debug logs under `logs/` | No |
| `dashboard.enabled` | Enable the `/panel` dashboard | No; defaults to `false` (off) |
| `dashboard.password` | Panel password: empty = random (printed in startup log), `none` = open access, other = fixed | No |
| `auth.api_key` | API key for `/v1/*`; empty disables auth | No |

Full example:

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

## Dashboard

The dashboard is off by default (`/panel` returns 404 unless `dashboard.enabled: true`). Once enabled, visit `http://localhost:8080/panel`:

![Devin·2API dashboard — models page](docs/dashboard.png)

- `dashboard.enabled: true` turns it on; it requires `devin.token` to fetch model catalog, groups, and usage — otherwise it stays disabled (a hint is logged at startup);
- `dashboard.password` has three modes:
  - **empty (default)**: a random password is generated on every start and printed in the startup log (rotates on restart);
  - **`"none"`**: open access — only for trusted localhost/intranet use;
  - **any other string**: fixed password;
- On login a session cookie is set, valid for 24 hours;
- The panel has two tabs: **Stats** (requests/success rate/latency, input-output-cached token usage, 24h bar+line chart, last 20 requests with client IP and click-to-expand detail, uptime, per-model top) and **Models** (categories/providers, promo gift-icon popover with start/end/remaining/original-vs-current price, price filters, merge-by-family view);
- Clicking a recent request expands request headers (sensitive headers like Authorization are never stored), request body and response — details are in-memory only, never persisted (capped at 48KB);
- Stats are aggregated in memory and flushed to `stats.json` next to the config file every 5 minutes (merged on restart) plus once more on shutdown; the tab auto-refreshes every 15s.

## Proxy pools & model routing

The upstream serves a different model catalog per egress region: direct (CN) yields ~50 models; a US egress yields the full ~300 (`claude-*`, `gpt-*`, `gemini-*`, `grok-*`, `muse-*`, ...). So `devin.proxy` is also the **catalog egress** — set it to a US proxy to see everything, then route latency-sensitive models back to direct:

```yaml
devin:
  proxy: "socks5://user:pass@us1.example.com:1080"  # US egress: full catalog + panel + unrouted models
  proxy_pools:                    # arbitrary names; per-request round-robin (optional)
    us:
      - "socks5://user:pass@us1.example.com:1080"
      - "socks5://user:pass@us2.example.com:1080"
  routes:                         # top-down, first match wins
    - models: ["swe-*", "glm-*", "kimi-*", "deepseek-*", "nemotron-*", "qwen-*", "minimax-*"]
      pool: direct                # domestic models -> direct, low latency
      # unmatched claude-*/gpt-*/gemini-* etc. -> devin.proxy US fallback
```

Or the inverse: leave `proxy` empty (catalog shows only region-visible models) and explicitly route US-only models to a pool:

```yaml
  routes:
    - models: ["claude-*", "gpt-*", "gemini-*", "grok-*", "muse-*"]
      pool: us
```

- Each `models` pattern matches the model **uid / display label / family / upstream group name**; `*` (any run) and `?` (single char) wildcards, case-insensitive; group matching needs the catalog (fetched on first request; on failure matching degrades to uid/label/family).
- `pool` is a name from `proxy_pools`, or a reserved name: `direct` (direct connection), `default` (= `devin.proxy`).
- Routes only apply to chat traffic (`/v1/*`); panel data and the model catalog always use `devin.proxy`.
- Multiple addresses per pool rotate per request — good for "one egress, several entries"; reserve high-latency proxies for the few models that need them.
- Changes take effect on restart. Keep `config.yaml` protected — proxy URLs may embed credentials.

## Notes

- If `devin.token` is empty, `/v1/*` returns `503 provider_configuration`;
- Auth headers are accepted as both `Authorization: Bearer <key>` and `X-Api-Key: <key>`;
- Sensitive fields such as tokens are redacted as `<redacted>` in debug logs;
- Images must be base64 data URLs; external URLs, `file_id`, and local file paths are not supported;
- Each request may carry the full message history (fully supported and recommended); when a request carries only the new turn plus a `previous_response_id` issued by this server, the conversation history is now restored server-side and prepended automatically. An unknown or expired id never fails the request -- it degrades to the single-turn behaviour and is recorded in the debug log;
- `config.yaml` is in `.gitignore`; even though the example is tracked, never commit a real token;
- Request body size is capped at 32 MiB to accommodate multi-image base64 payloads.

## Documentation

- **Architecture, supported API fields, proto extraction, and other technical details**: [Contributing guide](CONTRIBUTING.md)
- **License**: [MIT](LICENSE)