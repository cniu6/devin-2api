# Contributing

Thanks for considering contributing to `devin-2api`. This guide covers the architecture, the dev environment, and how to submit changes.

> **English** | [中文](CONTRIBUTING.zh-CN.md)

## Project positioning (read this first)

`devin-2api` is a **protocol adapter**: HTTP speaks OpenAI Responses, the upstream is Devin Connect, and a vendor-neutral model layer (`internal/llm`) isolates the two — so new upstreams can be added behind the same HTTP surface by implementing the adapter interface. Agent-loop semantics stay equivalent — not provider request-structure equivalent.

Every change must respect this boundary: an upstream must not bypass the `llm` intermediate layer — the HTTP protocol and any upstream protocol must never be mapped directly:

```text
OpenAI Responses HTTP ──► llm intermediate layer ──► Devin Connect RPC
   (codec)                  (semantic model)           (adapter)
```

- The HTTP codec only understands the OpenAI protocol, never Devin;
- `internal/llm` is the single semantic model; each side only performs semantic conversion, never pass-through of structures;
- Adapters only translate `internal/llm` ⇄ the upstream protocol and keep no HTTP-layer knowledge.

**What makes a good upstream? Statelessness.** The ideal upstream keeps no session state — every request is self-contained and carries the full conversation. Devin currently meets this: each `GetChatMessage` call carries the complete, replayable context, which keeps the gateway horizontally scalable and safe to replay.

## Architecture and data flow

```text
                 ┌────────────────────────────────────────────────────┐
                 │                  devin-2api                        │
  HTTP client    │                                                    │    upstream
 ─────────────►  │  /v1/responses                                    │  ┌──────────────────┐
   Responses     │   │                                                │  │ Devin Connect     │
   JSON / SSE    │   ▼                                                │  │ (server.codeium   │
                 │  responses.DecodeRequest ──► llm.RequestMessages  │  │  .com)            │
                 │        │                                            │  │                   │
                 │        ▼                                            │  │ GetChatMessage    │
                 │  adapter.Stream(ctx, RequestMessages) ────────────►│  │ (Connect, proto)  │
                 │        │                                            │  └──────────────────┘
                 │        ▼                                            │
                 │  llm.ResponseStream (event stream)                  │
                 │        │                                            │
                 │        ├─ streaming:  writeSSE + StreamEncoder ──► │
                 │        └─ non-stream: collectFinalMessage ──► JSON │
                 └────────────────────────────────────────────────────┘
```

Full request lifecycle:

1. `POST /v1/responses` receives OpenAI Responses JSON (body capped at 8 MiB);
2. `responses.DecodeRequest` converts the request into `llm.RequestMessages` (system prompt, message history, tool definitions) plus generation options;
3. `adapter.Stream` hands the vendor-neutral context to the configured adapter and returns an `llm.ResponseStream`;
4. the Devin adapter translates the intermediate model into a `GetChatMessageRequest` (protobuf), reads upstream frames over a Connect stream, and a `responseDecoder` interprets each frame into zero or more `llm.ResponseEvent`s;
5. output is split by the request's `stream` option:
   - **streaming**: `StreamEncoder` expands events into typed SSE (`response.output_text.delta`, …);
   - **non-streaming**: `done`/`error` events are aggregated into a final `AssistantMessage` encoded as Responses JSON.

### Intermediate model (internal/llm)

The semantic model shared by all adapters, defined in `internal/llm`:

| Concept | Description |
| --- | --- |
| `RequestMessages` | Full request context: `SystemPrompt` + chronologically ordered `Messages` + `Tools` |
| `Message` | `UserMessage` / `AssistantMessage` / `ToolResultMessage` (role decided by `Role()`) |
| `Content` | Content blocks: `TextContent` / `ThinkingContent` / `ImageContent` / `ToolCall` |
| `ToolDefinition` | Tool name + description + JSON Schema input |
| `ResponseEvent` | Incremental events (12 kinds: `start`, `text_delta`, `toolcall_*`, `done`, `error`, …) |
| `AssistantMessage` | Final aggregated message incl. `Usage`, `StopReason`, provider metadata |

Message history is a **complete, replayable conversation across providers**: thinking signatures, tool-call IDs, and usage fields are designed to be passed verbatim into the next request round (see the comments on `TextSignature`, `ThinkingSignature`, etc. in `request.go`).

## Dev environment

- Go 1.26.3 (see `go.mod`)
- [Task](https://taskfile.dev/): the proto → Go binding generation entrypoint (`Taskfile.yml`)
- Generating the bindings requires `protoc` + `protoc-gen-go` + `protoc-gen-connect-go` (versions pinned in `Taskfile.yml`):

```bash
brew install protobuf
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
go install connectrpc.com/connect/cmd/protoc-gen-connect-go@v1.19.1
```

## Common commands

```bash
# Generate the proto Go bindings (mandatory right after cloning; generated code is not tracked)
task generate

# Run the full test suite
go test ./...

# Run locally (needs config.yaml first, see README → Quick start)
go run ./cmd/devin-2api -config config.yaml
```

## Supported API surface

`/v1/responses` supports a subset of the OpenAI Responses API:

- `input` may be a plain string or an array of input items (`message`, `function_call`, `function_call_output`);
- `content` supports a string or an array of parts (`input_text`, `output_text`, `text`, `input_image` as base64 data URL);
- generation options: `instructions`, `tools` (function type with JSON Schema `parameters`), `stream`;
- `stream: false` returns a full JSON Response; `stream: true` returns typed SSE (`response.created`, `response.output_item.added`, `response.output_text.delta`, `response.completed`, …).

`/v1/audio/transcriptions` accepts `multipart/form-data` with a `file` field (any audio bytes, ≤25MiB) and returns `{"text": ...}` via upstream `ApiServerService.GetTranscription`.

### Upstream RPCs not yet wired

`ApiServerService` exposes more RPCs that are reachable but need the exact Windsurf client payload before they can be mapped cleanly:

- `GetCompletions` / `GetStreamingCompletions` — tab autocomplete (FIM). The `CompletionsRequest.prompt` pipeline depends on prompt-element ranges/kind info built by the real client; a bare `{prompt, language}` probe returns `invalid_argument`.
- `GetEmbeddings` — `{prompts[], model: MODEL_EMBED_6591}` probe returns `failed_precondition`; likely requires a specific priority/prefix/experiment flag.
- `GetStreamingModelAPITextCompletion` — takes `chat_message_prompts` + numeric `ExaCodeiumCommonPb_Model` enum; catalog uids are strings without a published uid→enum mapping.
- `GetCommandModelConfigs` — returns the "command" models used for commit-message generation etc. (`swe-2-flash-medium` + `MODEL_PRIVATE_2` "Claude 4.5 Sonnet"). **Merged**: appended to `/v1/models` and the panel catalog with `is_command: true` (dedup by uid, best-effort on upstream error) — they're callable via GetChatMessage even though they're absent from the picker.

Probe tool: `DEVIN_TOKEN=... [HTTPS_PROXY=...] go run ./cmd/probe-api`.

### Tool description passing

Intermediate tools are converted into Devin native function tools (name and JSON Schema constraints preserved, while natural-language annotations such as `description`/`title` — which upstream may misinterpret as classification hints — are stripped); non-empty tool descriptions are additionally injected into the system prompt as `<tool name="...">` blocks so the model understands their purpose. See `internal/adapter/devin/tool_definition.go`.

## Debug logging

When `debug.enabled: true`, each request gets a staged log directory under `logs/<request-time>/` next to the config file, useful for pinpointing failures at any hop of "HTTP ⇄ intermediate ⇄ upstream":

```text
meta.json                  # request outcome summary (status, model, duration)
01-http-request.json       # raw HTTP request (redacted)
02-request-messages.json   # converted intermediate request context
03-devin-request.json      # proto request sent upstream (as JSON)
04-devin-response.jsonl    # raw upstream response frames
05-response-events.jsonl   # intermediate response events
06-http-response.jsonl     # final response/SSE events written to the client
error.json                 # first failing stage and error
attachments/               # externalized image attachments (deduped by SHA-256)
```

Concurrent requests in the same second are distinguished by an incrementing suffix in the directory name.

## Before submitting

1. **Tests pass**: `go test ./...`
2. **Formatted**: `gofmt -l .` produces no output
3. **Comment conventions**: follow the repo's Go comment conventions (`.agent/skills/go-comment-conventions`) — exported symbols get doc comments, field comments explain "why", not restate the code
4. **No real tokens**: `config.yaml` is tracked by git; make sure no real `devin.token` is committed (add it to `.gitignore` if needed)

## Submitting changes

1. Fork the repo and create a feature branch off `main` (e.g. `fix/sse-close`, `feat/stream-options`);
2. One logical change per commit; write commit messages in the imperative, saying what and why;
3. If the change alters protocol semantics or adapter behavior, update the README and tests accordingly;
4. Open a Pull Request and describe:
   - the purpose and how you verified it;
   - which layer it touches ("HTTP ⇄ intermediate ⇄ upstream");
   - whether an upstream protocol upgrade is involved (descriptor changes, see below).

## Releasing

Releases follow [SemVer](https://semver.org/). While the project is in the 0.x phase, breaking changes bump the minor version (`v0.1.0` → `v0.2.0`), not the major one.

A release is a `v`-prefixed tag. `gh release create` tags the current `HEAD`, pushes the tag, and creates the GitHub Release page with auto-generated notes — which triggers the `release.yml` workflow (full test suite, then a Docker image build + push to Docker Hub `leokun123/devin-2api` for amd64 and arm64):

```bash
gh release create v0.1.0 --generate-notes
```

Notes:

- the workflow tags the image as `0.1.0`, `0.1`, `0`, and `latest` (pre-releases like `v0.2.0-rc.1` skip `latest`);
- the workflow reads the `DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN` repository secrets — the maintainer must set them once (token created at [hub.docker.com/settings/security](https://hub.docker.com/settings/security), not the login password);
- tags are immutable once pushed; fix a bad release by releasing a new version, never by rewriting the tag.

## Updating the upstream protocol (proto extraction)

The full pipeline is fixed in `Taskfile.yml` as two steps:

```text
task extract BINARY=<upstream-binary>   ① binary → outputs/devin-proto/    (not reproducible, depends on packet capture)
task generate                           ② proto  → outputs/devin-proto-go/ (reproducible, standard toolchain)
```

### ① protoextract: binary → proto

Scans compiled binaries for embedded `FileDescriptorProto`s and reconstructs .proto sources — used to recover the upstream protocol when no original .proto files exist (this project used it to extract Devin's 63 descriptors, see `outputs/devin-proto/`).

Two ways to run it:

```bash
# Option 1 (recommended): Taskfile wrapper, output fixed to outputs/devin-proto/
task extract BINARY=/Applications/Devin.app/Contents/Resources/app/extensions/windsurf/bin/language_server_macos_arm

# Option 2: call the underlying tool directly with any output directory
# (useful for extracting to a temp dir and comparing first)
go run ./cmd/protoextract <source-binary> <output-directory>
```

Arguments:

- `<source-binary>` — the compiled artifact to analyze (a regular file, e.g. the Devin/Windsurf language server binary);
- `<output-directory>` — the output directory, **emptied and rebuilt**; fixed to `outputs/devin-proto/` via Option 1, or a temp dir (e.g. `/tmp/extract-test`) via Option 2 to compare before overwriting.

Outputs:

- `descriptors.pb` — the complete descriptor set preserving original package names, syntax, options, and file boundaries;
- `all-protos.proto` — a flattened single-file bundle that compiles as-is;
- `manifest.json` — per-descriptor metadata and symbol mappings.

**Standard procedure after an upstream upgrade** (the new binary may embed new descriptors):

1. Extract to a temp dir and diff against the committed version:
   ```bash
   go run ./cmd/protoextract <new-binary> /tmp/extract-test
   diff <(grep '"name"' outputs/devin-proto/manifest.json | sort) \
        <(grep '"name"' /tmp/extract-test/manifest.json | sort)
   ```
2. Confirm the added/changed descriptors are expected, then overwrite `outputs/devin-proto/` (Option 1) or copy the temp outputs;
3. Run `task generate` to regenerate the Go bindings;
4. Verify extraction quality: check `descriptor_count` and `missing_dependencies` in `manifest.json`, and compile-check `all-protos.proto` with `protoc --descriptor_set_out=/dev/null all-protos.proto`.

The command **empties the output directory** and guards against destructive paths (filesystem root, home directory, the source binary, etc.). Step ① depends on the upstream binary and packet capture, so it is **not reproducible** and its outputs must be committed to git.

### ② task generate: proto → Go bindings

The Connect client bindings (`outputs/devin-proto-go/`, referenced via `replace local/devinproto => ./outputs/devin-proto-go` in go.mod) are generated from `outputs/devin-proto/all-protos.proto`:

```bash
task generate
```

The generated code is **not tracked in git** (see `.gitignore`); run this command after cloning or whenever the upstream descriptors change. The generation parameters are fixed in `Taskfile.yml` (protoc with the `Mall-protos.proto=local/devinproto` mapping); Docker builds run generation automatically in the builder stage (`golang:1.26.3-alpine` → `alpine:3.22`, binary at `/app/devin-2api`).

## Code layout at a glance

```text
cmd/
  devin-2api/       # entrypoint: load config, assemble deps, serve HTTP
  protoextract/     # tool: extract embedded protobuf descriptors from binaries
internal/
  adapter/          # adapter boundary (interface Adapter)
    devin/          # Devin Connect adapter: request/response conversion + tool-definition sanitizing
  api/openai/
    responses/      # OpenAI Responses HTTP codec (JSON request, JSON/SSE response)
  app/              # chi routing, request lifecycle, error handling
  config/           # YAML config loading and validation
  debuglog/         # per-request staged debug logs (redaction + externalized images)
  llm/              # vendor-neutral intermediate model (request, response, event stream)
outputs/
  devin-proto/      # raw descriptors extracted from the Devin binary, committed
  devin-proto-go/   # generated Go bindings, untracked; produced by task generate
e2e/
  devin-client/     # end-to-end Devin connect client call example (hand-written)
```

New adapters (for other upstreams) should implement the `Adapter` interface in `internal/adapter`, built entirely on the `internal/llm` semantic model, without introducing HTTP-layer knowledge.