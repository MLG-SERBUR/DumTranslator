package discord

import (
	"fmt"
	"log"
	"strings"
	"sync"


	"github.com/bwmarrin/discordgo"
	"github.com/user/dumtranslator/internal/config"
	"github.com/user/dumtranslator/internal/translate"
)

type Handler struct {
	TranslateAPI  *translate.TranslateAPI
	MyMemory      *translate.MyMemory
	Cerebras      *translate.Cerebras
	Mistral       *translate.Mistral
	ActiveBackend string
	Channels      *config.ChannelStore
	WebhookCache   map[string]string // map[channelID]webhookID
	Config         *config.Config
	ConfigPath     string
	mu             sync.Mutex 
}

func NewHandler(tAPI *translate.TranslateAPI, mm *translate.MyMemory, cer *translate.Cerebras, mis *translate.Mistral, cfg *config.Config, configPath string, cs *config.ChannelStore) *Handler {
	initialBackend := cfg.Backend
	if initialBackend == "" {
		initialBackend = "TranslateAPI"
	}
	return &Handler{
		TranslateAPI:  tAPI,
		MyMemory:      mm,
		Cerebras:      cer,
		Mistral:       mis,
		ActiveBackend: initialBackend,
		Channels:      cs,
		WebhookCache:   make(map[string]string),
		Config:         cfg,
		ConfigPath:     configPath,
	}
}

func (h *Handler) activeTranslator() translate.Translator {
	switch h.ActiveBackend {
	case "MyMemory":
		return h.MyMemory
	case "Cerebras":
		return h.Cerebras
	case "Mistral":
		return h.Mistral
	default:
		return h.TranslateAPI
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

	// Detect Language for MyMemory and TranslateAPI
	source := translate.DetectLanguage(m.Content)

	// Translate
	resp, err := h.activeTranslator().Translate(m.Content, source)
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
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		h.handleCommandInteraction(s, i)
	case discordgo.InteractionMessageComponent:
		h.handleComponentInteraction(s, i)
	}
}

func (h *Handler) handleCommandInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
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
	case "backend":
		options := data.Options
		if len(options) == 0 {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "Current backend: " + h.ActiveBackend,
				},
			})
			return
		}

		newBackend := options[0].StringValue()
		if newBackend != "TranslateAPI" && newBackend != "MyMemory" && newBackend != "Cerebras" && newBackend != "Mistral" {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "Invalid backend. Use 'TranslateAPI', 'MyMemory', 'Cerebras', or 'Mistral'.",
				},
			})
			return
		}

		h.ActiveBackend = newBackend
		h.Config.Backend = newBackend
		err := h.Config.Save(h.ConfigPath)
		response := "Backend switched to " + newBackend
		if err != nil {
			log.Printf("Error saving config: %v", err)
			response += " (failed to persist: " + err.Error() + ")"
		}

		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: response,
			},
		})
	}
}

func (h *Handler) handleComponentInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Handle select menu interaction
	if i.MessageComponentData().ComponentType != discordgo.SelectMenuComponent {
		return
	}

	// Check if this is our backend selection menu
	customID := i.MessageComponentData().CustomID
	if !strings.HasPrefix(customID, "backend_select:") {
		return
	}

	// Acknowledge the interaction immediately to prevent Discord timeout (3s)
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	})
	if err != nil {
		log.Printf("Error deferring interaction response: %v", err)
		return
	}

	// Get the original message ID from the CustomID
	parts := strings.Split(customID, ":")
	if len(parts) < 2 {
		return
	}
	originalMessageID := parts[1]

	// Get the selected backend from the interaction data
	if len(i.MessageComponentData().Values) == 0 {
		return
	}
	nextBackend := i.MessageComponentData().Values[0]

	// Update the default backend for everyone
	h.mu.Lock()
	h.ActiveBackend = nextBackend
	h.Config.Backend = nextBackend
	err = h.Config.Save(h.ConfigPath)
	h.mu.Unlock()

	if err != nil {
		log.Printf("Error saving config: %v", err)
	}

	// Get the original message that triggered this translation
	originalMsg, err := s.ChannelMessage(i.ChannelID, originalMessageID)
	if err != nil {
		log.Printf("Error getting original message: %v", err)
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: ptr(fmt.Sprintf("Could not find original message to re-translate: %v", err)),
		})
		return
	}
	originalContent := originalMsg.Content

	// Translate with the next backend
	translator := h.getTranslator(nextBackend)
	source := translate.DetectLanguage(originalContent)

	resp, err := translator.Translate(originalContent, source)
	if err != nil {
		log.Printf("Translation error with %s: %v", nextBackend, err)
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: ptr(fmt.Sprintf("Translation failed with %s: %v.", nextBackend, err)),
		})
		return
	}

	// Update the interaction response with new content and refreshed select menu
	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: ptr(resp.TranslatedText),
		Components: &[]discordgo.MessageComponent{
			h.createBackendSelectMenu(originalMessageID, nextBackend),
		},
	})
}

func (h *Handler) getTranslator(backend string) translate.Translator {
	switch backend {
	case "MyMemory":
		return h.MyMemory
	case "Cerebras":
		return h.Cerebras
	case "Mistral":
		return h.Mistral
	default:
		return h.TranslateAPI
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

	// Create select menu for backend selection
	actionRow := h.createBackendSelectMenu(m.ID, h.ActiveBackend)

	// Use display name instead of just username
	displayName := m.Author.Username
	if m.Member != nil && m.Member.Nick != "" {
		displayName = m.Member.Nick
	} else if m.Author.GlobalName != "" {
		displayName = m.Author.GlobalName
	}

	_, err = s.WebhookExecute(webhookID, webhookToken, true, &discordgo.WebhookParams{
		Content:    content,
		Username:   displayName + " (translated)",
		AvatarURL:  m.Author.AvatarURL(""),
		Components: []discordgo.MessageComponent{actionRow},
	})
	return err
}

func (h *Handler) createBackendSelectMenu(messageID string, activeBackend string) discordgo.ActionsRow {
	backends := []string{"TranslateAPI", "MyMemory", "Cerebras", "Mistral"}
	var options []discordgo.SelectMenuOption

	for _, b := range backends {
		translator := h.getTranslator(b)
		options = append(options, discordgo.SelectMenuOption{
			Label:   translator.DisplayName(),
			Value:   b,
			Default: b == activeBackend,
		})
	}

	return discordgo.ActionsRow{
		Components: []discordgo.MessageComponent{
			discordgo.SelectMenu{
				CustomID:    fmt.Sprintf("backend_select:%s", messageID),
				Placeholder: "Select Translation Backend",
				Options:     options,
			},
		},
	}
}

func ptr(s string) *string {
	return &s
}
