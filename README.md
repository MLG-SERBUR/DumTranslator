# DumTranslator

A powerful Discord bot that automatically translates messages and provides real-time voice captions!

## Screenshot Showcase

![DumTranslator in action - automatic message translation with multiple translation backends](https://via.placeholder.com/800x400?text=DumTranslator+Message+Translation+Demo)

![Real-time voice captions with automatic translation](https://via.placeholder.com/800x400?text=Real-time+Voice+Captions+with+Backend+Selection)

## Key Features

### Automatic Message Translation
- **Language Detection**: Currently checks for Arabic and Korean characters (may be modified to detect more in the future)
- **Multiple Translation Backends**: Choose from 3 different translation services
- **Interactive Backend Selection**: Switch translation providers on-the-fly with dropdown menus

### Real-Time Voice Captions
- **Live Speech-to-Text**: Transcribes voice chat conversations in real-time
- **Multi-User Support**: Shows who's speaking with usernames/nicknames
- **Powered by Groq**: Uses whisper-large-v3 for transcription and translation

### Flexible Configuration
- **Channel Management**: Use `/listen` and `/ignore` commands to control where translations happen
- **Backend Customization**: Set default translation backend or switch per-message
- **Resource Efficient**: Written in Go for minimal resource usage

## Translation Backends

**AI-Powered (Prompted):**

- Cerebras
- Mistral

**Direct API:**
- TranslateAPI
- MyMemory

- Google

For API key setup, refer to each service's documentation.

## Commands

### Channel Management
- `/listen` - Start translating messages in the current channel
- `/ignore` - Stop translating messages in the current channel

### Translation Control
- `/backend [name]` - Switch default translation backend
- `/backend` (no args) - Show current backend

### Voice Captions
- `/captions on` - Start real-time captions in your voice channel
- `/captions off` - Stop captions and leave voice channel

## Setup

### Prerequisites
1. **Go 1.22+**: [Install Go](https://go.dev/doc/install)
2. **Discord Bot Token**: Create at [Discord Developer Portal](https://discord.com/developers/applications)
3. **API Keys**: Get keys for your preferred translation services

### Installation
1. Clone this repository
2. Build the bot:
   ```bash
   go build ./cmd/bot
   ```

3. Create `config.json` (copy from `config.example.json`) and add your keys:
   ```json
   {
     "discord_token": "YOUR_DISCORD_BOT_TOKEN",
     "translate_api_key": "YOUR_TRANSLATEAPI_KEY",
     "cerebras_api_key": "YOUR_CEREBRAS_API_KEY",
     "cerebras_model": "gpt-oss-120b",
     "mistral_api_key": "YOUR_MISTRAL_API_KEY",
     "mistral_model": "mistral-large-latest",
     "groq_api_key": "YOUR_GROQ_API_KEY",
     "mymemory_email": "YOUR_EMAIL@example.com",
     "backend": "TranslateAPI",
     "target_channels": []
   }
   ```

### Invite the Bot
Replace `YOUR_CLIENT_ID` with your bot's Application ID:

`https://discord.com/api/oauth2/authorize?client_id=YOUR_CLIENT_ID&permissions=536873984&scope=bot`

**Required Permissions:**
- Manage Webhooks
- View Channels
- Send Messages

## Usage

Start the bot:
```bash
./bot -config config.json
```

### Quick Start Guide
1. Invite the bot to your server
2. Use `/listen` in any channel to enable translations
3. Arabic and Korean messages will be automatically translated to English
4. Use the dropdown menu on translated messages to try different backends
5. For voice chats, use `/captions on` in the voice channel chat to enable live transcription

## Advanced Features

### Backend Selection
Each translated message includes a dropdown menu to instantly re-translate using different backends - perfect for comparing translation quality!

### Cost Optimization
The bot only processes Arabic and Korean messages, saving API costs by skipping English and other languages.

### Voice Caption Customization
Captions show speaker names, support multiple users, and maintain conversation history for context.

## Development

Run locally:
```bash
go run ./cmd/bot/main.go
```

## Notes
- Voice captions require `captions_enabled: true` in config
- Translation backends can be configured in `config.json`
- Channel preferences persist between bot restarts
- Language detection can be modified to support additional languages

---

Built with Go and DiscordGo | [GitHub](https://github.com/MLG-SERBUR/DumTranslator)
