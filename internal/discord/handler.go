package discord

import (
	"fmt"
	"log"
	"strings"


	"github.com/bwmarrin/discordgo"
	"github.com/user/dumtranslator/internal/config"
	"github.com/user/dumtranslator/internal/translate"
)

type Handler struct {
	TranslateAPI *translate.TranslateAPI
	MyMemory     *translate.MyMemory
	ActiveBackend string
	Channels     *config.ChannelStore
	WebhookCache map[string]string // map[channelID]webhookID
	Config       *config.Config
	ConfigPath   string
}

func NewHandler(tAPI *translate.TranslateAPI, mm *translate.MyMemory, cfg *config.Config, configPath string, cs *config.ChannelStore) *Handler {
	initialBackend := cfg.Backend
	if initialBackend == "" {
		initialBackend = "TranslateAPI"
	}
	return &Handler{
		TranslateAPI: tAPI,
		MyMemory:     mm,
		ActiveBackend: initialBackend,
		Channels:     cs,
		WebhookCache: make(map[string]string),
		Config:       cfg,
		ConfigPath:   configPath,
	}
}

func (h *Handler) activeTranslator() translate.Translator {
	if h.ActiveBackend == "MyMemory" {
		return h.MyMemory
	}
	return h.TranslateAPI
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
		if newBackend != "TranslateAPI" && newBackend != "MyMemory" {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "Invalid backend. Use 'TranslateAPI' or 'MyMemory'.",
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
	if i.Type != discordgo.InteractionMessageComponent {
		return
	}

	// Check if this is our backend select menu
	customID := i.MessageComponentData().CustomID
	if !strings.HasPrefix(customID, "backend_select:") {
		return
	}

	// Get the original message ID from the CustomID
	parts := strings.Split(customID, ":")
	if len(parts) < 2 {
		return
	}
	originalMessageID := parts[1]

	// Get the selected backend
	values := i.MessageComponentData().Values
	if len(values) == 0 {
		return
	}
	selectedBackend := values[0]

	// Get the original message that triggered this translation
	originalMsg, err := s.ChannelMessage(i.ChannelID, originalMessageID)
	if err != nil {
		log.Printf("Error getting original message: %v", err)
		// Fallback to the old logic or ephemeral error
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Could not find original message to re-translate.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}
	originalContent := originalMsg.Content

	// Translate with the selected backend
	translator := h.getTranslator(selectedBackend)
	source := translate.DetectLanguage(originalContent)

	resp, err := translator.Translate(originalContent, source)
	if err != nil {
		log.Printf("Translation error with %s: %v", selectedBackend, err)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Translation failed: " + err.Error(),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	// Update the webhook message with the new translation
	_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		ID:      i.Message.ID,
		Channel: i.ChannelID,
		Content: &resp.TranslatedText,
	})

	if err != nil {
		log.Printf("Error editing message: %v", err)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Failed to update message: " + err.Error(),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	// Respond to the interaction to acknowledge it
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content: resp.TranslatedText,
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						&discordgo.SelectMenu{
							CustomID:    customID, // Keep the same CustomID with message ID
							Placeholder: "Select translation backend...",
							Options: []discordgo.SelectMenuOption{
								{
									Label:       "TranslateAPI",
									Value:       "TranslateAPI",
									Description: "Use TranslateAPI backend",
									Default:     selectedBackend == "TranslateAPI",
								},
								{
									Label:       "MyMemory",
									Value:       "MyMemory",
									Description: "Use MyMemory backend",
									Default:     selectedBackend == "MyMemory",
								},
							},
						},
					},
				},
			},
		},
	})
}

func (h *Handler) getTranslator(backend string) translate.Translator {
	if backend == "MyMemory" {
		return h.MyMemory
	}
	return h.TranslateAPI
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

	// Create select menu component for backend selection
	// Encode original message ID into CustomID
	customID := fmt.Sprintf("backend_select:%s", m.ID)
	backendSelect := &discordgo.SelectMenu{
		CustomID:    customID,
		Placeholder: "Select translation backend...",
		Options: []discordgo.SelectMenuOption{
			{
				Label:       "TranslateAPI",
				Value:       "TranslateAPI",
				Description: "Use TranslateAPI backend",
			},
			{
				Label:       "MyMemory",
				Value:       "MyMemory",
				Description: "Use MyMemory backend",
			},
		},
	}

	// Create action row with the select menu
	actionRow := discordgo.ActionsRow{
		Components: []discordgo.MessageComponent{backendSelect},
	}

	_, err = s.WebhookExecute(webhookID, webhookToken, true, &discordgo.WebhookParams{
		Content:    content,
		Username:   m.Author.Username + " (" + h.activeTranslator().DisplayName() + ")",
		AvatarURL:  m.Author.AvatarURL(""),
		Components: []discordgo.MessageComponent{actionRow},
	})
	return err
}
