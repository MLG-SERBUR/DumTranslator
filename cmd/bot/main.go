package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	channelStore, err := config.NewChannelStore("channels.json", cfg.TargetChannels, config.DefaultChannelSettings(cfg))
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

	order := []string{"TranslateAPI", "MyMemory", "Cerebras", "Cerebras+", "Mistral", "Mistral+", "Google"}

	// Init Discord Handler
	handler := discord.NewHandler(translators, order, cfg, channelStore)

	// Init Discord Session
	dg, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		log.Fatalf("Error creating Discord session: %v", err)
	}

	// Set discordgo logging to use the standard log package
	// This will log internal gateway errors, heartbeats, and reconnection attempts.
	dg.LogLevel = discordgo.LogWarning // Log warnings and errors
	discordgo.Logger = func(msgL, caller int, format string, a ...interface{}) {
		log.Printf("DISCORDGO [%d]: %s", msgL, fmt.Sprintf(format, a...))
	}

	// Enable automatic gateway/websocket reconnection
	dg.ShouldReconnectOnError = true

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

	// Register Connection logging handlers
	dg.AddHandler(func(s *discordgo.Session, c *discordgo.Connect) {
		log.Println("Connected to Discord Gateway.")
	})
	dg.AddHandler(func(s *discordgo.Session, d *discordgo.Disconnect) {
		log.Println("Disconnected from Discord Gateway.")
	})
	dg.AddHandler(func(s *discordgo.Session, r *discordgo.Resumed) {
		log.Println("Discord session resumed.")
	})
	dg.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Printf("Logged in as: %v#%v", s.State.User.Username, s.State.User.Discriminator)
	})

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

	// Define the slash commands this process manages.
	backendChoices := make([]*discordgo.ApplicationCommandOptionChoice, 0, len(order))
	for _, backend := range order {
		backendChoices = append(backendChoices, &discordgo.ApplicationCommandOptionChoice{
			Name:  translators[backend].DisplayName(),
			Value: backend,
		})
	}

	onOffChoices := []*discordgo.ApplicationCommandOptionChoice{
		{
			Name:  "on",
			Value: "on",
		},
		{
			Name:  "off",
			Value: "off",
		},
	}

	ownedCommands := []*discordgo.ApplicationCommand{
		{
			Name:        "translate",
			Description: "Manage translation settings for this channel",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "enabled",
					Description: "Turn translation on or off for this channel",
					Required:    false,
					Choices:     onOffChoices,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "backend",
					Description: "Backend to use for this channel when translation is on",
					Required:    false,
					Choices:     backendChoices,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "interaction_selection",
					Description: "Enable or disable the backend select dropdown on translated messages",
					Required:    false,
					Choices:     onOffChoices,
				},
			},
		},
	}

	// Selectively register only our commands (don't overwrite commands from other projects).
	// Fetch existing global commands and only replace `/translate`.
	log.Println("Registering slash commands...")
	appID := dg.State.User.ID
	existingCmds, err := dg.ApplicationCommands(appID, "")
	if err != nil {
		log.Fatalf("Cannot fetch existing commands: %v", err)
	}

	// Delete the existing `/translate` command so we can re-create it fresh.
	for _, existing := range existingCmds {
		if existing.Name == "translate" {
			log.Printf("  Deleting existing command: %s", existing.Name)
			err = dg.ApplicationCommandDelete(appID, "", existing.ID)
			if err != nil {
				log.Printf("  Warning: failed to delete command %s: %v", existing.Name, err)
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
	// Since the bot can become stuck trying to disconnect while the network is down or the gateway is reconnecting,
	// we enforce a timeout limit on the Close() method.
	shutdownDone := make(chan struct{})
	go func() {
		dg.Close()
		close(shutdownDone)
	}()

	select {
	case <-shutdownDone:
		log.Println("Disconnected cleanly.")
	case <-time.After(5 * time.Second):
		log.Println("Timeout while disconnecting. Exiting forcefully.")
	}
}
