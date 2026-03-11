package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/bwmarrin/discordgo"
	"github.com/user/dumtranslator/internal/config"
	"github.com/user/dumtranslator/internal/discord"
	"github.com/user/dumtranslator/internal/discord/captions"
	"github.com/user/dumtranslator/internal/translate"
)

func main() {
    configPath := flag.String("config", "config.json", "Path to config file")
    flag.Parse()

	// Load Config
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

    // Load/Init Channel Store
    // We use a separate file "channels.json" for persistence
    channelStore, err := config.NewChannelStore("channels.json", cfg.TargetChannels)
    if err != nil {
        log.Fatalf("Error loading channel store: %v", err)
    }

	// Init Translators
	tAPI := translate.NewTranslateAPI(cfg.TranslateAPIKey)
	mm := translate.NewMyMemory(cfg.MyMemoryEmail)
	cer := translate.NewCerebras(cfg.CerebrasAPIKey, cfg.CerebrasModel)
	mis := translate.NewMistral(cfg.MistralAPIKey, cfg.MistralModel)
	// arliai := translate.NewArliAI(cfg.ArliAIAPIKey, cfg.ArliAIModel)
    google := translate.NewGoogleTranslate()

	// Init Specialized (+) Translators
	plusPrompt := "Translate the following text to natural, fluent, idiomatic English while preserving the original tone, intent, and cultural nuances; do not output anything else: %s"
	
	cerPlus := translate.NewCerebras(cfg.CerebrasAPIKey, cfg.CerebrasModel)
	cerPlus.Prompt = plusPrompt
	cerPlus.DisplayNameOverride = fmt.Sprintf("Cerebras (%s) (+)", cfg.CerebrasModel)

	misPlus := translate.NewMistral(cfg.MistralAPIKey, cfg.MistralModel)
	misPlus.Prompt = plusPrompt
	misPlus.DisplayNameOverride = fmt.Sprintf("Mistral (%s) (+)", cfg.MistralModel)

	/*
	arliaiPlus := translate.NewArliAI(cfg.ArliAIAPIKey, cfg.ArliAIModel)
	arliaiPlus.Prompt = plusPrompt
	arliaiPlus.DisplayNameOverride = fmt.Sprintf("ArliAI (%s) (+)", cfg.ArliAIModel)
	*/

	translators := map[string]translate.Translator{
		"TranslateAPI": tAPI,
		"MyMemory":     mm,
		"Cerebras":     cer,
		"Cerebras+":    cerPlus,
		"Mistral":      mis,
		"Mistral+":     misPlus,
		"Google":       google,
	}

	// Init Groq STT
	groq := translate.NewGroqClient(cfg.GroqAPIKey, cfg.STTModel)

	order := []string{"TranslateAPI", "MyMemory", "Cerebras", "Cerebras+", "Mistral", "Mistral+"}

	// Init Discord Handler
	handler := discord.NewHandler(translators, order, cfg, *configPath, channelStore)

	// Init Discord Session
	dg, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		log.Fatalf("Error creating Discord session: %v", err)
	}

	// Disable automatic gateway/websocket reconnection
	dg.ShouldReconnectOnError = false

	// Disable automatic voice channel reconnection
	dg.ShouldReconnectVoiceOnSessionError = false

	// Init Captions Manager (only if enabled)
	captionsOn := cfg.CaptionsEnabled != nil && *cfg.CaptionsEnabled
	if captionsOn {
		captionsMgr := captions.NewManager(dg, groq)
		handler.Captions = captionsMgr
	}

	// Register Handlers
	dg.AddHandler(handler.MessageCreate)
	dg.AddHandler(handler.InteractionCreate)

	// Identify Intent
	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent
	if captionsOn {
		dg.Identify.Intents |= discordgo.IntentsGuildVoiceStates
	}

	// Open Connection
	err = dg.Open()
	if err != nil {
		log.Fatalf("Error opening connection: %v", err)
	}

	// Define the slash commands this bot owns
	ownedCommands := []*discordgo.ApplicationCommand{
		{
			Name:        "listen",
			Description: "Start translating messages in this channel",
		},
		{
			Name:        "ignore",
			Description: "Stop translating messages in this channel",
		},
		{
			Name:        "backend",
			Description: "Switch translation backend",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "name",
					Description: "Backend name (TranslateAPI, MyMemory, Cerebras, Cerebras+, Mistral, Mistral+)",
					Required:    false,
				},
			},
		},
	}

	// Conditionally include the captions command
	if captionsOn {
		ownedCommands = append(ownedCommands, &discordgo.ApplicationCommand{
			Name:        "captions",
			Description: "Manage real-time translated captions in voice channels",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "on",
					Description: "Start captions in your current voice channel",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "off",
					Description: "Stop captions and leave the voice channel",
				},
			},
		})
	}

	// Selectively register only our commands (don't overwrite commands from other projects).
	// Fetch existing global commands, delete+re-create ours, and remove captions if disabled.
	log.Println("Registering slash commands...")
	appID := dg.State.User.ID
	existingCmds, err := dg.ApplicationCommands(appID, "")
	if err != nil {
		log.Fatalf("Cannot fetch existing commands: %v", err)
	}

	// Build a lookup of existing commands by name
	existingByName := make(map[string]*discordgo.ApplicationCommand)
	for _, cmd := range existingCmds {
		existingByName[cmd.Name] = cmd
	}

	// Build a set of the command names we want to register
	ownedNames := make(map[string]bool)
	for _, cmd := range ownedCommands {
		ownedNames[cmd.Name] = true
	}

	// Delete existing versions of our commands so we can re-create them fresh
	for _, cmd := range ownedCommands {
		if existing, ok := existingByName[cmd.Name]; ok {
			log.Printf("  Deleting existing command: %s", cmd.Name)
			err = dg.ApplicationCommandDelete(appID, "", existing.ID)
			if err != nil {
				log.Printf("  Warning: failed to delete command %s: %v", cmd.Name, err)
			}
		}
	}

	// If captions is disabled, also clean up any leftover captions command from a previous run
	if !captionsOn {
		if existing, ok := existingByName["captions"]; ok {
			log.Printf("  Deleting leftover captions command (captions_enabled=false)")
			err = dg.ApplicationCommandDelete(appID, "", existing.ID)
			if err != nil {
				log.Printf("  Warning: failed to delete captions command: %v", err)
			}
		}
	}

	// Create our commands
	for _, cmd := range ownedCommands {
		log.Printf("  Creating command: %s", cmd.Name)
		_, err = dg.ApplicationCommandCreate(appID, "", cmd)
		if err != nil {
			log.Fatalf("Cannot create slash command %s: %v", cmd.Name, err)
		}
	}

	fmt.Println("DumTranslator is now running. Press CTRL-C to exit.")
	
	// Wait here until CTRL-C or other term signal is received.
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	// Cleanly close down the Discord session.
	dg.Close()
}
