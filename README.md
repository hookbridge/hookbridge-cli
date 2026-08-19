# HookBridge CLI

Receive webhooks on your local machine during development. The HookBridge CLI connects your local server to HookBridge infrastructure so you can test webhook integrations without exposing your machine to the internet.

## How It Works

1. Configure your webhook provider (Stripe, GitHub, etc.) to send events to your HookBridge Webhook URL.
2. HookBridge receives and stores the webhook at its public endpoint.
3. The CLI receives the webhook over a persistent WebSocket connection within milliseconds.
4. The CLI forwards it to your local server as an HTTP POST, preserving the original headers, body, and content type.
5. The local response (status code and latency) is displayed in your terminal.

No inbound ports are required — the CLI connects outbound to HookBridge over HTTPS/WSS.

## Prerequisites

- A [HookBridge](https://app.hookbridge.io) account
- An [API key](https://app.hookbridge.io) (create one in the console under **API Keys**)

## Installation

### Homebrew (macOS / Linux)

```bash
brew install hookbridge/tap/hb
```

### Download a Binary

Download the latest release from [GitHub Releases](https://github.com/hookbridge/hookbridge-cli/releases) for your platform.

**macOS (Apple Silicon):**

```bash
curl -L https://github.com/hookbridge/hookbridge-cli/releases/download/v1.1.0/hb_v1.1.0_darwin_arm64.tar.gz | tar xz
sudo mv hb /usr/local/bin/
```

**macOS (Intel):**

```bash
curl -L https://github.com/hookbridge/hookbridge-cli/releases/download/v1.1.0/hb_v1.1.0_darwin_amd64.tar.gz | tar xz
sudo mv hb /usr/local/bin/
```

**Linux (x86_64):**

```bash
curl -L https://github.com/hookbridge/hookbridge-cli/releases/download/v1.1.0/hb_v1.1.0_linux_amd64.tar.gz | tar xz
sudo mv hb /usr/local/bin/
```

**Linux (ARM64):**

```bash
curl -L https://github.com/hookbridge/hookbridge-cli/releases/download/v1.1.0/hb_v1.1.0_linux_arm64.tar.gz | tar xz
sudo mv hb /usr/local/bin/
```

**Windows (x86_64):**

Download `hb_v1.1.0_windows_amd64.zip` from [GitHub Releases](https://github.com/hookbridge/hookbridge-cli/releases), extract `hb.exe`, and add it to a directory in your `PATH`. Use Windows Terminal or PowerShell for the best experience — color-coded output is not supported in the legacy Command Prompt.

### Go Install

If you have Go installed:

```bash
go install github.com/hookbridge/hookbridge-cli/cmd/hb@latest
```

### Verify Installation

```bash
hb version
```

## Quick Start

### 1. Log In

Authenticate with your HookBridge API key:

```bash
hb login
```

You will be prompted to enter your API key. The CLI verifies the key against the HookBridge API and saves credentials locally.

You can also pass the key non-interactively:

```bash
hb login --api-key YOUR_API_KEY
```

### 2. Start Listening

Start your local server, then run:

```bash
hb listen --port 3000
```

The CLI will:

1. Find or create a CLI-mode inbound endpoint in your project.
2. Print a **Webhook URL** — copy this into your webhook provider's settings.
3. Connect and wait for webhooks.

```
HookBridge CLI v1.1.0
Endpoint: CLI Endpoint (01a002bd-d4ce-710b-b817-a757e78d0a16)

Webhook URL: https://receive.hookbridge.io/v1/webhooks/receive/01a002bd-d4ce-710b-b817-a757e78d0a16/7c73a1e0f4b28d6590ac4471e2b8d3c5

Paste this URL into your webhook provider's settings.
Forwarding to http://localhost:3000
Ready. Waiting for webhooks...
```

### 3. Send a Test Webhook

From another terminal, send a test request to the Webhook URL printed above:

```bash
curl -X POST "YOUR_WEBHOOK_URL" \
  -H "Content-Type: application/json" \
  -d '{"event":"test","data":{"id":"123"}}'
```

You should see the webhook arrive in your CLI output:

```
12:34:01  POST  →  200  89ms  application/json  (42 bytes)
```

### 4. Connect a Webhook Provider

Replace the test curl with a real webhook provider:

1. Go to your provider's webhook settings (Stripe Dashboard, GitHub repo settings, etc.).
2. Set the webhook URL to the **Webhook URL** from `hb listen`.
3. Trigger an event in the provider.
4. Watch the webhook arrive in your terminal and hit your local server.

The Webhook URL is stable across sessions — you do not need to update your provider settings each time you restart the CLI.

## Global Flags

These are persistent flags on the root `hb` command — valid on every subcommand.

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | `false` | Machine-readable JSON on stdout |
| `--no-color` | `false` | Disable ANSI colour output |

- Under `--json`, **stdout carries only JSON**. All human-readable text, warnings, and diagnostics move to stderr, so nothing but a result can land on stdout.
- Not every command has a JSON result. Commands that produce one write it to stdout; the rest write nothing at all to stdout and put their human output on stderr as usual:

  | Command | `--json` stdout |
  |---------|-----------------|
  | `hb version` | `{"version":"..."}` |
  | `hb endpoints` | JSON array, one line |
  | `hb endpoints create` | `{"id":"...","receive_url":"..."}` |
  | `hb endpoints delete` | `{"id":"...","deleted":true}` |
  | `hb listen` | NDJSON event stream |
  | `hb login` | *nothing* |
  | `hb logout` | *nothing* |

  So `hb --json login > out.json` leaves `out.json` empty — an empty file, not `{}`. Check for empty output before parsing when scripting against a command that has no result shape.
- `--help` output stays on **stdout** even under `--json`. It's an explicit request for human text, not a result.
- Colour precedence, highest first: `--json` (never coloured) → `--no-color` → the `NO_COLOR` environment variable → whether stdout is a terminal. Output is never coloured when piped or redirected.
- Consistent with [no-color.org](https://no-color.org), `NO_COLOR` set but empty counts as not set.

## Command Reference

### `hb login`

Authenticate with your HookBridge API key.

```bash
hb login
hb login --api-key YOUR_API_KEY
```

| Flag | Description |
|------|-------------|
| `--api-key` | API key for non-interactive login |

### `hb logout`

Remove stored credentials.

```bash
hb logout
```

Deletes the config file. If you are already logged out, this prints a confirmation message without error.

### `hb listen`

Listen for webhooks and forward them to a local server.

```bash
hb listen --port 3000
hb listen --forward http://localhost:8080/webhooks/stripe
hb listen --no-forward --verbose
hb listen --endpoint 01a002bd-d4ce-710b-b817-a757e78d0a16
```

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--port` | `-p` | `3000` | Localhost port to forward to (`http://localhost:{port}`) |
| `--forward` | | | Full URL to forward to (overrides `--port`) |
| `--no-forward` | | `false` | Display webhooks without forwarding |
| `--verbose` | `-v` | `false` | Show full headers and body for each webhook |
| `--endpoint` | | | Use a specific endpoint by ID instead of auto-selecting |

**Output:**

Each webhook prints a log line:

```
12:34:01  POST  →  200  89ms  application/json  (328 bytes)
```

Status codes are color-coded: green for 2xx, red for 4xx/5xx, yellow for connection errors.

With `--verbose`, headers and body are printed below each log line.

Press `Ctrl+C` to stop listening. The CLI prints a summary of how many webhooks were received during the session.

**`--json` output:**

Under `--json`, JSON replaces the human line — it is not also echoed. stdout is one JSON object per line ([NDJSON](https://github.com/ndjson/ndjson-spec)).

A normal session — ready, one successfully forwarded webhook, shutdown:

```
{"event":"ready","endpoint_id":"01a002bd-d4ce-710b-b817-a757e78d0a16","forward_to":"http://localhost:3000"}
{"event":"webhook","id":"01a002bd-e297-7dae-bc8d-0f63ed59ce00","content_type":"application/json","size_bytes":56,"received_at":"2026-08-15T00:06:34.22206422Z","forwarded":true,"status_code":200,"latency_ms":3}
{"event":"shutdown","received":1}
```

A failed forward (e.g. the local server isn't running) looks like this instead — `forwarded` is `false`, and `status_code`/`latency_ms` are replaced by `error`:

```
{"event":"webhook","id":"01a002d3-a839-7c31-a61d-cf00f68890c7","content_type":"application/json","size_bytes":56,"received_at":"2026-08-15T00:30:21.050323773Z","forwarded":false,"error":"connection refused or timeout: Post \"http://localhost:45999\": dial tcp [::1]:45999: connect: connection refused"}
```

Diagnostics (connection status, errors) go to stderr, also as NDJSON:

```
{"event":"status","message":"Connected via WebSocket (real-time)"}
{"event":"error","message":"..."}
```

Full field reference — every key each event can emit:

**`ready`** — always the first stdout line, emitted once, before the listener starts.

| Field | Type | Description |
|---|---|---|
| `event` | string | Always `"ready"` |
| `endpoint_id` | string | ID of the endpoint being listened on |
| `forward_to` | string | The `--forward` target URL, emitted verbatim — or `""` when `--no-forward` is used. See [Security](#security) if that URL might contain credentials |

**`webhook`** — emitted once per received webhook.

| Field | Type | Description |
|---|---|---|
| `event` | string | Always `"webhook"` |
| `id` | string | Webhook message ID |
| `content_type` | string | Content-Type of the received webhook |
| `size_bytes` | number | Size of the webhook body in bytes |
| `received_at` | string | RFC3339 timestamp of when HookBridge received the webhook |
| `forwarded` | boolean | Whether the webhook was forwarded to the local server and got a response |
| `status_code` | number | HTTP status code from the local server. Present only when `forwarded` is `true` |
| `latency_ms` | number | Forward round-trip time in milliseconds. Present only when `forwarded` is `true`. `0` is a real sub-millisecond forward, not a missing value |
| `error` | string | Present only when a forward was attempted and failed (e.g. connection refused, timeout). Contains the underlying error text, which embeds the forward target URL — see [Security](#security) |

Headers and bodies are never included in the `webhook` event — payloads can carry customer secrets, and stdout is the machine stream. `--verbose` has no effect under `--json`.

**`shutdown`** — the last stdout line, emitted exactly once per session.

| Field | Type | Description |
|---|---|---|
| `event` | string | Always `"shutdown"` |
| `received` | number | Total webhooks received during the session, across both the WebSocket and polling transports |

**stderr diagnostics** — connection status changes and errors, written as NDJSON to stderr instead of stdout.

| Field | Type | Description |
|---|---|---|
| `event` | string | `"status"` for a connection state change, `"error"` for a failure |
| `message` | string | Human-readable diagnostic text |

- `ready` is always first and `shutdown` is always last, appearing exactly once — poll for `ready` if you're scripting against the CLI.
- The receive URL never appears anywhere in the listen JSON stream.

### `hb endpoints`

List CLI-mode inbound endpoints in your project.

```bash
hb endpoints
hb endpoints --json
```

```
ID                                       NAME                 ACTIVE
--------------------------------------   ------------------   ------
01a002bd-d4ce-710b-b817-a757e78d0a16     CLI Endpoint         yes
```

**`--json` output:**

```json
[{"id":"01a002bd-d4ce-710b-b817-a757e78d0a16","name":"CLI Endpoint","active":true}]
```

A single JSON array on one line, not NDJSON — a list is a finite result, so it gets one document. Each element has exactly `id`, `name`, and `active` (a JSON boolean, not the `yes`/`no` string the human table shows). An empty result is `[]` — never `null`, never zero bytes — so a consumer can tell "none" from "failed".

Only CLI-mode endpoints are listed, in both human and JSON form.

The receive URL is deliberately not included. It embeds a plaintext 32-character secret in its path, making it a credential — listing would dump every endpoint's secret to stdout in CI. Capture it once, from `hb endpoints create`.

### `hb endpoints create`

Create a new CLI-mode inbound endpoint.

```bash
hb endpoints create
hb endpoints create --name "Stripe Local"
hb endpoints create --ephemeral --ttl-minutes 60
```

| Flag | Default | Description |
|------|---------|-------------|
| `--name` | `CLI Endpoint` | Name for the new endpoint |
| `--ephemeral` | `false` | Mark the endpoint as ephemeral so it is swept up automatically |
| `--ttl-minutes` | | Minutes until the ephemeral endpoint expires (1-1440) |

`--ttl-minutes` is validated locally, before any API call is made:

- `--ttl-minutes` without `--ephemeral` is an error: `Error: --ttl-minutes requires --ephemeral`
- `--ttl-minutes` must be between 1 and 1440: `Error: --ttl-minutes must be between 1 and 1440`. This also applies to an explicit `--ttl-minutes 0` passed alongside `--ephemeral` — it's a range error, not treated as unset.

Both errors exit non-zero and send zero HTTP requests.

**`--json` output:**

```json
{"id":"01a002bd-d4ce-710b-b817-a757e78d0a16","receive_url":"https://receive.hookbridge.io/v1/webhooks/receive/01a002bd-d4ce-710b-b817-a757e78d0a16/7c73a1e0f4b28d6590ac4471e2b8d3c5"}
```

**This is the only command that returns the receive URL.** The API does not return it again afterwards, so capture it as soon as you create the endpoint.

### `hb endpoints delete`

Delete an inbound endpoint.

```bash
hb endpoints delete 01a002bd-d4ce-710b-b817-a757e78d0a16
hb endpoints delete 01a002bd-d4ce-710b-b817-a757e78d0a16 --force
```

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--force` | `-f` | `false` | Skip the confirmation prompt |

Takes the endpoint ID as a required positional argument. When stdin is a terminal, it asks for confirmation before deleting; `--force` (or `-f`) skips the prompt. Aborting the prompt exits non-zero, so a script checking only the exit code can never mistakenly conclude the endpoint was deleted.

An unknown or already-deleted ID also exits non-zero:

```
Error: inbound endpoint "01a002bd-d4ce-710b-b817-a757e78d0a16" not found — it may already be deleted
```

**`--json` output:**

```json
{"id":"01a002bd-d4ce-710b-b817-a757e78d0a16","deleted":true}
```

### `hb version`

Print the installed CLI version.

```bash
hb version
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `HB_API_KEY` | Override the stored API key |
| `HB_API_URL` | Override the API base URL |
| `HB_STREAM_URL` | Override the WebSocket stream URL |
| `NO_COLOR` | Disable ANSI colour output when set to a non-empty value |

Environment variables take precedence over values in the config file.

## Config File

Credentials are stored in `~/.hookbridge/config.json`:

| Platform | Path |
|----------|------|
| macOS / Linux | `~/.hookbridge/config.json` |
| Windows | `%USERPROFILE%\.hookbridge\config.json` |

```json
{
  "api_key": "hb_live_...",
  "project_id": "proj_..."
}
```

On macOS and Linux, the file is created with `0600` permissions (owner read/write only).

## Connection Resilience

The CLI maintains a WebSocket connection to receive webhooks in real time. If the connection drops (network change, laptop sleep, etc.), it automatically reconnects with exponential backoff. If reconnection fails, it switches to an HTTP polling fallback.

While polling, the CLI periodically retries the WebSocket connection. When restored, it switches back to real-time streaming automatically.

Webhooks that arrive while the CLI is offline are stored by HookBridge and delivered when you reconnect.

## Security

- **No inbound ports** — your machine does not need to be publicly accessible. The CLI connects outbound over HTTPS/WSS.
- **API key authentication** — only authenticated clients with access to the endpoint can receive webhooks.
- **Credentials stored locally** — your API key is saved with restricted file permissions (`0600`).
- **TLS everywhere** — all communication between the CLI and HookBridge uses TLS encryption.
- **Receive URLs are credentials** — each endpoint's receive URL embeds a plaintext 32-character secret in its path. Treat it like a password: mask it in CI logs and never commit it to source control. This is also why `--json` output for `hb endpoints` (list) and `hb listen` deliberately omits it.
- **`--forward` targets are echoed back, not masked** — under `--json`, the `ready` event's `forward_to` field always includes your `--forward` URL verbatim, and a failed forward's `webhook` event `error` field includes it too (there, a userinfo password is masked — `user:pass@host` becomes `user:***@host` — but a query-string secret isn't). A plain `--port`/localhost target, the default, carries nothing sensitive, so this only matters if you put credentials in the URL yourself. Prefer keeping `--forward` to a plain host and let your local receiver hold any secret.

## Troubleshooting

### "not logged in — run 'hb login' first"

Run `hb login` and enter a valid API key. If you have already logged in, the config file may have been deleted — log in again.

### "invalid API key"

Your API key may have been revoked or may be from a different project. Create a new key in the [HookBridge console](https://app.hookbridge.io) and run `hb login` again.

### "WebSocket unavailable, using polling fallback"

The real-time streaming connection could not be established. The CLI will continue to work via HTTP polling. This may happen on networks that block WebSocket connections. Webhooks will still be received, with slightly higher latency.

### Webhooks are not arriving

1. Confirm your local server is running and accepting POST requests on the expected port.
2. Check that the Webhook URL in your provider's settings matches the URL printed by `hb listen`.
3. Try `hb listen --no-forward --verbose` to see if webhooks are reaching HookBridge but failing to forward locally.
4. Check that the endpoint is active with `hb endpoints`.

### Connection refused errors

Your local server is not running or is not listening on the port/URL the CLI is forwarding to. Start your server and confirm the port matches the `--port` or `--forward` flag.

### Where do I get my Webhook URL?

The receive URL is issued once, when the endpoint is created: `hb endpoints create` prints it (and returns it as `receive_url` under `--json`), and the `hb listen` run that creates a new endpoint shows it too. Capture it at that point.

The URL is stable for the life of the endpoint — paste it into your provider's settings once, and it keeps working across CLI restarts and later `hb listen` runs. You won't need to look it up again.

## Documentation

Full documentation is available at [docs.hookbridge.io](https://docs.hookbridge.io).

## License

This project is licensed under the Apache License 2.0. See [LICENSE](LICENSE) for details.
