package discord

import (
	"fmt"
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/user/dumtranslator/internal/config"
	"github.com/user/dumtranslator/internal/discord/captions"
	"github.com/user/dumtranslator/internal/translate"
)

type Handler struct {
	Translators  map[string]translate.Translator
	BackendOrder []string
	Channels     *config.ChannelStore
	WebhookCache map[string]string // map[channelID]webhookID
	Config       *config.Config
	Captions     *captions.Manager
}

func NewHandler(translators map[string]translate.Translator, order []string, cfg *config.Config, cs *config.ChannelStore) *Handler {
	return &Handler{
		Translators:  translators,
		BackendOrder: order,
		Channels:     cs,
		WebhookCache: make(map[string]string),
		Config:       cfg,
		Captions:     nil, // Will be set in main
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

	channelSettings, ok := h.Channels.Get(m.ChannelID)
	if !ok || !channelSettings.Enabled {
		return
	}

	// Check language (Cost saving)
	// We only translate if it is Arabic or Korean.
	if !translate.IsArabicOrKorean(m.Content) {
		return
	}

	// Detect Language for MyMemory and TranslateAPI
	source := translate.DetectLanguage(m.Content)

	backend := h.resolveBackend(channelSettings.Backend)

	// Translate
	resp, err := h.getTranslator(backend).Translate(m.Content, source)
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
	err = h.sendWebhook(s, m, resp.TranslatedText, backend, channelSettings.InteractionSelectEnabled)
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
	case "translate":
		h.handleTranslateCommand(s, i, data)
	case "captions":
		if h.Config.CaptionsEnabled != nil && !*h.Config.CaptionsEnabled {
			return
		}

		options := data.Options[0]
		switch options.Name {
		case "on":
			// 1. Fetch the channel where the command was typed
			// Try cache first, fallback to REST API if the bot just restarted
			channel, err := s.State.Channel(i.ChannelID)
			if err != nil {
				channel, err = s.Channel(i.ChannelID)
			}

			if err != nil {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: "Error verifying channel: " + err.Error(),
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				})
				return
			}

			// 2. Verify that the command was typed inside a Voice Channel's text chat.
			// By checking this, we completely bypass the buggy VoiceState cache!
			if channel.Type != discordgo.ChannelTypeGuildVoice && channel.Type != discordgo.ChannelTypeGuildStageVoice {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: "Please use this command directly inside the voice channel's text chat.",
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				})
				return
			}

			// 3. Since we know they typed it in a VC, i.ChannelID IS the voice channel!
			// We pass i.ChannelID as both the Voice Channel ID and the Text Output ID.
			err = h.Captions.Start(i.GuildID, i.ChannelID, i.ChannelID)
			if err != nil {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: "Error starting captions: " + err.Error(),
					},
				})
				return
			}

			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "Captions enabled. I've joined the voice channel.",
				},
			})
			log.Printf("Started captions for guild %s in channel %s", i.GuildID, i.ChannelID)
		case "off":
			err := h.Captions.Stop(i.GuildID)
			response := "Captions disabled. I've left the voice channel."
			if err != nil {
				response = "Error stopping captions: " + err.Error()
			}
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: response,
				},
			})
		}
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
	if _, ok := h.Translators[nextBackend]; !ok {
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: ptr("Invalid translation backend selection."),
		})
		return
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
			Components: &[]discordgo.MessageComponent{
				h.createBackendSelectMenu(originalMessageID, nextBackend),
			},
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
	if t, ok := h.Translators[backend]; ok {
		return t
	}
	return h.Translators["TranslateAPI"]
}

func (h *Handler) resolveBackend(backend string) string {
	if _, ok := h.Translators[backend]; ok {
		return backend
	}
	if h.Config != nil {
		if _, ok := h.Translators[h.Config.Backend]; ok {
			if backend != "" && backend != h.Config.Backend {
				log.Printf("Unknown backend %q, falling back to configured default %q", backend, h.Config.Backend)
			}
			return h.Config.Backend
		}
	}
	if backend != "" && backend != "TranslateAPI" {
		log.Printf("Unknown backend %q, falling back to TranslateAPI", backend)
	}
	return "TranslateAPI"
}

func (h *Handler) sendWebhook(s *discordgo.Session, m *discordgo.MessageCreate, content string, activeBackend string, interactionSelectEnabled bool) error {
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

	// Use display name instead of just username
	displayName := m.Author.Username
	if m.Member != nil && m.Member.Nick != "" {
		displayName = m.Member.Nick
	} else if m.Author.GlobalName != "" {
		displayName = m.Author.GlobalName
	}

	params := &discordgo.WebhookParams{
		Content:   content,
		Username:  displayName,
		AvatarURL: m.Author.AvatarURL(""),
	}
	if interactionSelectEnabled {
		params.Components = []discordgo.MessageComponent{
			h.createBackendSelectMenu(m.ID, activeBackend),
		}
	}

	_, err = s.WebhookExecute(webhookID, webhookToken, true, params)
	return err
}

func (h *Handler) createBackendSelectMenu(messageID string, activeBackend string) discordgo.ActionsRow {
	var options []discordgo.SelectMenuOption

	for _, b := range h.BackendOrder {
		translator := h.getTranslator(b)
		options = append(options, discordgo.SelectMenuOption{
			Label:   translator.DisplayName(),
			Value:   b,
			Default: activeBackend == b,
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

func (h *Handler) handleTranslateCommand(s *discordgo.Session, i *discordgo.InteractionCreate, data discordgo.ApplicationCommandInteractionData) {
	enabledSetting, hasEnabledSetting := optionStringValue(data.Options, "enabled")
	backend, hasBackend := optionStringValue(data.Options, "backend")
	interactionSelection, hasInteractionSelection := optionStringValue(data.Options, "interaction_selection")

	if !hasEnabledSetting {
		if hasBackend || hasInteractionSelection {
			h.respondToInteraction(s, i, "Set `enabled` to `on` or `off` when changing translation settings for this channel.")
			return
		}
		h.respondToInteraction(s, i, h.translateStatusMessage(i.ChannelID))
		return
	}

	switch enabledSetting {
	case "on":
		if hasBackend {
			if _, ok := h.Translators[backend]; !ok {
				h.respondToInteraction(s, i, fmt.Sprintf("Invalid backend. Available backends: %s", strings.Join(h.availableBackends(), ", ")))
				return
			}
		}

		var interactionSelectEnabled *bool
		if hasInteractionSelection {
			enabled := interactionSelection == "on"
			interactionSelectEnabled = &enabled
		}

		settings, err := h.Channels.Enable(i.ChannelID, backend, interactionSelectEnabled)
		if err != nil {
			h.respondToInteraction(s, i, "Error saving channel settings: "+err.Error())
			return
		}

		h.respondToInteraction(s, i, fmt.Sprintf(
			"Translation is on for this channel.\nBackend: %s\nInteraction select dropdown: %s",
			h.resolveBackend(settings.Backend),
			onOff(settings.InteractionSelectEnabled),
		))
	case "off":
		settings, err := h.Channels.Disable(i.ChannelID)
		if err != nil {
			h.respondToInteraction(s, i, "Error saving channel settings: "+err.Error())
			return
		}

		h.respondToInteraction(s, i, fmt.Sprintf(
			"Translation is off for this channel.\nSaved backend: %s\nSaved interaction select dropdown: %s",
			h.resolveBackend(settings.Backend),
			onOff(settings.InteractionSelectEnabled),
		))
	default:
		h.respondToInteraction(s, i, "Invalid enabled value. Use `on` or `off`.")
	}
}

func (h *Handler) translateStatusMessage(channelID string) string {
	settings, ok := h.Channels.Get(channelID)
	if !ok {
		return fmt.Sprintf(
			"Translation is off for this channel.\nDefaults when enabled:\nBackend: %s\nInteraction select dropdown: %s",
			h.resolveBackend(settings.Backend),
			onOff(settings.InteractionSelectEnabled),
		)
	}

	status := "off"
	if settings.Enabled {
		status = "on"
	}

	return fmt.Sprintf(
		"Translation is %s for this channel.\nBackend: %s\nInteraction select dropdown: %s",
		status,
		h.resolveBackend(settings.Backend),
		onOff(settings.InteractionSelectEnabled),
	)
}

func (h *Handler) availableBackends() []string {
	available := make([]string, 0, len(h.BackendOrder))
	for _, backend := range h.BackendOrder {
		available = append(available, backend)
	}
	return available
}

func (h *Handler) respondToInteraction(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
		},
	})
}

func optionStringValue(options []*discordgo.ApplicationCommandInteractionDataOption, name string) (string, bool) {
	for _, option := range options {
		if option.Name == name {
			return option.StringValue(), true
		}
	}
	return "", false
}

func onOff(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

func ptr(s string) *string {
	return &s
}
