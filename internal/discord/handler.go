package discord

import (
	"fmt"
	"log"


	"github.com/bwmarrin/discordgo"
	"github.com/user/dumtranslator/internal/config"
	"github.com/user/dumtranslator/internal/translate"
)

type Handler struct {
	Translator   *translate.Client
	Channels     *config.ChannelStore
	WebhookCache map[string]string // map[channelID]webhookID
}

func NewHandler(t *translate.Client, cs *config.ChannelStore) *Handler {
	return &Handler{
		Translator:   t,
		Channels:     cs,
		WebhookCache: make(map[string]string),
	}
}

func (h *Handler) MessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignore all messages created by the bot itself
	if m.Author.ID == s.State.User.ID {
		return
	}
    
    // Ignore other bots to avoid loops
    if m.Author.Bot {
        return
    }

	// Check if we are listening to this channel
	if !h.Channels.Has(m.ChannelID) {
		return
	}

	// Check language (Cost saving)
    // We only translate if it is Arabic or Korean.
	if !translate.IsArabicOrKorean(m.Content) {
		return
	}

	// Translate
	resp, err := h.Translator.Translate(m.Content)
	if err != nil {
		log.Printf("Translation error: %v", err)
		return
	}

    // Double check with API response
    if resp.SourceLanguage != "ar" && resp.SourceLanguage != "ko" {
        log.Printf("Translation API returned source language %s, skipping webhook", resp.SourceLanguage)
        return
    }

	// Send Webhook
	err = h.sendWebhook(s, m, resp.TranslatedText)
	if err != nil {
		log.Printf("Webhook error: %v", err)
	}
}

func (h *Handler) InteractionCreate(s *discordgo.Session, i *discordgo.InteractionCreate) {
    if i.Type != discordgo.InteractionApplicationCommand {
        return
    }

    data := i.ApplicationCommandData()
    switch data.Name {
    case "listen":
        err := h.Channels.Add(i.ChannelID)
        response := "DumTranslator is now listening to this channel."
        if err != nil {
            response = "Error saving channel: " + err.Error()
        }
        s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
            Type: discordgo.InteractionResponseChannelMessageWithSource,
            Data: &discordgo.InteractionResponseData{
                Content: response,
            },
        })
    case "ignore":
        err := h.Channels.Remove(i.ChannelID)
        response := "DumTranslator stopped listening to this channel."
        if err != nil {
            response = "Error saving channel: " + err.Error()
        }
        s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
            Type: discordgo.InteractionResponseChannelMessageWithSource,
            Data: &discordgo.InteractionResponseData{
                Content: response,
            },
        })
    }
}

func (h *Handler) sendWebhook(s *discordgo.Session, m *discordgo.MessageCreate, content string) error {
	var webhookID string
	var err error

	// Check cache first (simple in-memory cache)
    // specific logic to find *our* webhook
    // We prefer a webhook named "DumTranslator"
	
    webhooks, err := s.ChannelWebhooks(m.ChannelID)
    if err != nil {
        return err
    }
    
    var targetWebhook *discordgo.Webhook
    for _, w := range webhooks {
        if w.Name == "DumTranslator" {
            targetWebhook = w
            break
        }
    }
    
    if targetWebhook == nil {
        // Create one
        targetWebhook, err = s.WebhookCreate(m.ChannelID, "DumTranslator", "")
        if err != nil {
            return fmt.Errorf("failed to create webhook: %w", err)
        }
    }
    webhookID = targetWebhook.ID
	webhookToken := targetWebhook.Token 
    
	_, err = s.WebhookExecute(webhookID, webhookToken, true, &discordgo.WebhookParams{
		Content:   content,
		Username:  m.Author.Username + " (Translated)",
		AvatarURL: m.Author.AvatarURL(""),
	})
	return err
}
