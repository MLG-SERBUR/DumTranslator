package discord

import (
	"fmt"
	"log"
	"time"


	"github.com/bwmarrin/discordgo"
	"github.com/user/dumtranslator/internal/config"
	"github.com/user/dumtranslator/internal/translate"
)

type Handler struct {
	TranslateAPI  *translate.TranslateAPI
	MyMemory      *translate.MyMemory
	LaraTranslate *translate.LaraTranslate
	LaraTranslate2 *translate.LaraTranslate2
	ActiveBackend string
	Channels      *config.ChannelStore
	WebhookCache  map[string]string // map[channelID]webhookID
	Config        *config.Config
	ConfigPath    string
}

func NewHandler(tAPI *translate.TranslateAPI, mm *translate.MyMemory, lara *translate.LaraTranslate, lara2 *translate.LaraTranslate2, cfg *config.Config, configPath string, cs *config.ChannelStore) *Handler {
	initialBackend := cfg.Backend
	if initialBackend == "" {
		initialBackend = "TranslateAPI"
	}
	return &Handler{
		TranslateAPI:   tAPI,
		MyMemory:       mm,
		LaraTranslate:  lara,
		LaraTranslate2: lara2,
		ActiveBackend:  initialBackend,
		Channels:       cs,
		WebhookCache:   make(map[string]string),
		Config:         cfg,
		ConfigPath:     configPath,
	}
}

func (h *Handler) activeTranslator() translate.Translator {
	switch h.ActiveBackend {
	case "MyMemory":
		return h.MyMemory
	case "LaraTranslate":
		return h.LaraTranslate
	case "LaraTranslate2":
		return h.LaraTranslate2
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

	// Context collection for LaraTranslate
	var context []string
	if h.ActiveBackend == "LaraTranslate" {
		// Fetch recent messages (past 2 minutes)
		// discordgo.Session.ChannelMessages(channelID, limit, beforeID, afterID, aroundID)
		messages, err := s.ChannelMessages(m.ChannelID, 10, m.ID, "", "")
		if err == nil {
			// Messages are returned newest first. We want them in chronological order.
			// And filtered by time.
			now := time.Now()
			for i := len(messages) - 1; i >= 0; i-- {
				msg := messages[i]
				msgTime := msg.Timestamp
				if now.Sub(msgTime) < 2*time.Minute {
					context = append(context, msg.Content)
				}
			}
		} else {
			log.Printf("Error fetching context messages: %v", err)
		}
	}

	// Translate
	resp, err := h.activeTranslator().Translate(m.Content, source, context)
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
        if newBackend != "TranslateAPI" && newBackend != "MyMemory" && newBackend != "LaraTranslate" && newBackend != "LaraTranslate2" {
            s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
                Type: discordgo.InteractionResponseChannelMessageWithSource,
                Data: &discordgo.InteractionResponseData{
                    Content: "Invalid backend. Use 'TranslateAPI', 'MyMemory', 'LaraTranslate', or 'LaraTranslate2'.",
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
		Username:  m.Author.Username + " (" + h.activeTranslator().DisplayName() + ")",
		AvatarURL: m.Author.AvatarURL(""),
	})
	return err
}
