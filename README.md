# cc-bot

Three Claude-powered Telegram bots (小一/小二/小三) connected via MCP.

## Architecture

```
Telegram ──→ userbot (MTProto) ──→ relay file ──→ cc-bot-1/2/3 (MCP HTTP)
         ──→ Bot API polling   ──→ cc-bot-1/2/3
```

- **userbot**: Python MTProto client that monitors group chats and writes messages to a shared relay file
- **cc-bot-1/2/3**: Go MCP servers, one per bot token, that expose Telegram tools to Claude

## Configuration

Copy `.env.example` to `.env` and fill in:

| Variable | Description |
|---|---|
| `TELEGRAM_BOT_TOKEN_1/2/3` | Bot tokens from @BotFather |
| `MCP_AUTH_TOKEN` | Shared secret for MCP HTTP auth |
| `TELEGRAM_API_ID` / `TELEGRAM_API_HASH` | From my.telegram.org (for userbot) |
| `TELEGRAM_SESSION_STRING` | MTProto session (generate once with userbot) |
| `WATCHED_CHATS` | Comma-separated group chat IDs for userbot relay (empty = all) |
| `ALLOWED_USER_IDS` | Comma-separated user IDs for message filtering (see below) |

### Message Filtering (`ALLOWED_USER_IDS`)

When set, bots apply these rules:

- **Private messages** (chat_id > 0): only users in `ALLOWED_USER_IDS` can interact
- **Group messages** (chat_id < 0): allowed only if a user in `ALLOWED_USER_IDS` is a member of that group (checked via Telegram API, cached 5 min)

If `ALLOWED_USER_IDS` is empty, all messages pass through.

## Local Development

```bash
# Run a single bot (stdio mode)
TELEGRAM_BOT_TOKEN=xxx go run ./cmd/cc-bot

# Build binary
make build
```

## Deployment to GCP

> ⚠️ GCP VM is Linux x86_64. Mac is arm64. **Must cross-platform build** or the image won't run.

### One-time setup

```bash
# Ensure buildx multi-platform builder exists
docker buildx create --use --name multiplatform --platform linux/amd64,linux/arm64
```

### Deploy steps

```bash
# 1. Build for linux/amd64 (required for GCP)
docker buildx build --platform linux/amd64 -t cc-bot:latest --load .

# 2. Build userbot image (if changed)
docker buildx build --platform linux/amd64 -t cc-bot-userbot:latest --load ./userbot

# 3. Save and upload
docker save cc-bot:latest | gzip > /tmp/cc-bot.tar.gz
scp -i ~/.ssh/gcpmai /tmp/cc-bot.tar.gz gcpmai@<GCP_IP>:/tmp/

# 4. Load on GCP
ssh -i ~/.ssh/gcpmai gcpmai@<GCP_IP> "docker load < /tmp/cc-bot.tar.gz"

# 5. Restart containers
ssh -i ~/.ssh/gcpmai gcpmai@<GCP_IP> "cd ~/bot && docker compose up -d"
```

### Update .env on GCP

```bash
ssh -i ~/.ssh/gcpmai gcpmai@<GCP_IP> "nano ~/bot/.env"
# Then restart: cd ~/bot && docker compose up -d
```

## Ports

| Service | Port |
|---|---|
| cc-bot-1 | 8081 |
| cc-bot-2 | 8082 |
| cc-bot-3 | 8083 |
