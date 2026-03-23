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

### Download a Binary

Download the latest release from [GitHub Releases](https://github.com/hookbridgehq/hookbridge-cli/releases) for your platform.

**macOS (Apple Silicon):**

```bash
curl -L https://github.com/hookbridgehq/hookbridge-cli/releases/latest/download/hb_latest_darwin_arm64.tar.gz | tar xz
sudo mv hb /usr/local/bin/
```

**macOS (Intel):**

```bash
curl -L https://github.com/hookbridgehq/hookbridge-cli/releases/latest/download/hb_latest_darwin_amd64.tar.gz | tar xz
sudo mv hb /usr/local/bin/
```

**Linux (x86_64):**

```bash
curl -L https://github.com/hookbridgehq/hookbridge-cli/releases/latest/download/hb_latest_linux_amd64.tar.gz | tar xz
sudo mv hb /usr/local/bin/
```

**Linux (ARM64):**

```bash
curl -L https://github.com/hookbridgehq/hookbridge-cli/releases/latest/download/hb_latest_linux_arm64.tar.gz | tar xz
sudo mv hb /usr/local/bin/
```

**Windows (x86_64):**

Download `hb_latest_windows_amd64.zip` from [GitHub Releases](https://github.com/hookbridgehq/hookbridge-cli/releases), extract `hb.exe`, and add it to a directory in your `PATH`. Use Windows Terminal or PowerShell for the best experience — color-coded output is not supported in the legacy Command Prompt.

### Go Install

If you have Go installed:

```bash
go install github.com/hookbridgehq/hookbridge-cli/cmd/hb@latest
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
HookBridge CLI v1.0.0
Endpoint: CLI Endpoint (ie_abc123)

Webhook URL: https://receive.hookbridge.io/v1/webhooks/receive/ie_abc123/sk_xyz789

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
hb listen --endpoint ie_abc123
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

### `hb endpoints`

List CLI-mode inbound endpoints in your project.

```bash
hb endpoints
```

```
ID                                       NAME                 ACTIVE
--------------------------------------   ------------------   ------
ie_abc123...                             CLI Endpoint         yes
```

### `hb endpoints create`

Create a new CLI-mode inbound endpoint.

```bash
hb endpoints create
hb endpoints create --name "Stripe Local"
```

| Flag | Default | Description |
|------|---------|-------------|
| `--name` | `CLI Endpoint` | Name for the new endpoint |

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

## Documentation

Full documentation is available at [docs.hookbridge.io](https://docs.hookbridge.io).

## License

This project is licensed under the Apache License 2.0. See [LICENSE](LICENSE) for details.
