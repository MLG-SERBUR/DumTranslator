# DumTranslator

A resource-efficient Discord bot written in Go that automatically translates non-English messages to English using `translateapi.ai`.

## Features
- **Automatic Translation**: Translates any non-English message to English.
- **Webhook Integration**: Reposts translated messages with the original user's name and avatar.
- **Cost Saving**: Local language detection ensures English messages are not sent to the API.
- **Configurable**: Choose which channels to translate via commands.

## Setup

### Prerequisites
1. **Go 1.22+**: [Install Go](https://go.dev/doc/install)
2. **Discord Bot Token**: Create a bot at the [Discord Developer Portal](https://discord.com/developers/applications).
3. **TranslateAPI Key**: Get a key from [translateapi.ai](https://translateapi.ai).

### Installation
1. Clone this repository (or download the files).
2. Build the bot:
   ```bash
   go build ./cmd/bot
   ```

3. Create a `config.json` file (copy from `config.example.json`) and fill in your keys:
   ```json
   {
     "discord_token": "YOUR_DISCORD_BOT_TOKEN",
     "translate_api_key": "YOUR_TRANSLATEAPI_KEY",
     "target_channels": []
   }
   ```
   *Note: `target_channels` can be left empty. You can add channels dynamically using commands.*

### Invite the Bot
Replace `YOUR_CLIENT_ID` with your Application ID from the Discord Developer Portal:

`https://discord.com/api/oauth2/authorize?client_id=YOUR_CLIENT_ID&permissions=536873984&scope=bot`

**Permissions required:**
- Manage Webhooks
- View Channels
- Send Messages

### Usage
Run the bot:
```bash
./bot -config config.json
```

**Commands:**
- `/listen`: Start translating messages in the current channel.
- `/ignore`: Stop translating messages in the current channel.


## Development
To run locally:
```bash
go run ./cmd/bot/main.go
```
