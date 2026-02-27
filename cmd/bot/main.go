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

	order := []string{"TranslateAPI", "MyMemory", "Cerebras", "Cerebras+", "Mistral", "Mistral+"}

	// Init Discord Handler
	handler := discord.NewHandler(translators, order, cfg, *configPath, channelStore)

	// Init Discord Session
	dg, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		log.Fatalf("Error creating Discord session: %v", err)
	}

	// Register Handlers
	dg.AddHandler(handler.MessageCreate)
    dg.AddHandler(handler.InteractionCreate)

    // Identify Intent
    dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent

	// Open Connection
	err = dg.Open()
	if err != nil {
		log.Fatalf("Error opening connection: %v", err)
	}

    // Register Slash Commands
    commands := []*discordgo.ApplicationCommand{
        {
            Name: "listen",
            Description: "Start translating messages in this channel",
        },
        {
            Name: "ignore",
            Description: "Stop translating messages in this channel",
        },
        {
            Name: "backend",
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
    
    // Bulk overwrite to ensure immediate update (GLOBAL commands take 1 hour, but guild commands are instant. 
    // For simplicity in a general bot, we use global but user should know about propagation.
    // Or we can just log it.) 
    // Edit: Since we don't know the GuildID from config, we register globally. 
    log.Println("Registering slash commands...")
    _, err = dg.ApplicationCommandBulkOverwrite(dg.State.User.ID, "", commands)
    if err != nil {
        log.Fatalf("Cannot create slash commands: %v", err)
    }

	fmt.Println("DumTranslator is now running. Press CTRL-C to exit.")
	
	// Wait here until CTRL-C or other term signal is received.
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	// Cleanly close down the Discord session.
	dg.Close()
}
