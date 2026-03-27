package config

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
)

type Config struct {
	DiscordToken             string   `json:"discord_token"`
	TranslateAPIKey          string   `json:"translate_api_key"`
	MyMemoryEmail            string   `json:"mymemory_email"`
	CerebrasAPIKey           string   `json:"cerebras_api_key"`
	CerebrasModel            string   `json:"cerebras_model"`
	MistralAPIKey            string   `json:"mistral_api_key"`
	MistralModel             string   `json:"mistral_model"`
	ArliAIAPIKey             string   `json:"arliai_api_key"`
	ArliAIModel              string   `json:"arliai_model"`
	GroqAPIKey               string   `json:"groq_api_key"`
	CaptionsEnabled          *bool    `json:"captions_enabled"`           // Pointer to distinguish between missing and false
	STTModel                 string   `json:"stt_model"`                  // Default: whisper-large-v3-turbo
	Backend                  string   `json:"backend"`                    // Default backend for newly enabled channels
	InteractionSelectEnabled *bool    `json:"interaction_select_enabled"` // Default dropdown state for newly enabled channels
	TargetChannels           []string `json:"target_channels"`            // Initial channels from config
}

type ChannelSettings struct {
	Enabled                  bool   `json:"enabled"`
	Backend                  string `json:"backend"`
	InteractionSelectEnabled bool   `json:"interaction_select_enabled"`
}

// ChannelStore manages persistent storage of per-channel translation settings.
type ChannelStore struct {
	Channels map[string]ChannelSettings `json:"channels"`
	filePath string
	defaults ChannelSettings
	mu       sync.RWMutex
}

type persistedChannelStore struct {
	Channels map[string]persistedChannelSettings `json:"channels"`
}

type persistedChannelSettings struct {
	Enabled                  *bool  `json:"enabled,omitempty"`
	Backend                  string `json:"backend,omitempty"`
	InteractionSelectEnabled *bool  `json:"interaction_select_enabled,omitempty"`
}

func LoadConfig(path string) (*Config, error) {
	file, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	err = json.Unmarshal(file, &cfg)
	if err != nil {
		return nil, err
	}

	// Defaults
	if cfg.CaptionsEnabled == nil {
		enabled := true
		cfg.CaptionsEnabled = &enabled
	}
	if cfg.STTModel == "" {
		cfg.STTModel = "whisper-large-v3-turbo"
	}
	if cfg.Backend == "" {
		cfg.Backend = "TranslateAPI"
	}
	if cfg.InteractionSelectEnabled == nil {
		enabled := true
		cfg.InteractionSelectEnabled = &enabled
	}

	return &cfg, nil
}

func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func DefaultChannelSettings(cfg *Config) ChannelSettings {
	settings := ChannelSettings{
		Backend:                  "TranslateAPI",
		InteractionSelectEnabled: true,
	}
	if cfg == nil {
		return settings
	}
	if cfg.Backend != "" {
		settings.Backend = cfg.Backend
	}
	if cfg.InteractionSelectEnabled != nil {
		settings.InteractionSelectEnabled = *cfg.InteractionSelectEnabled
	}
	return settings
}

func NewChannelStore(path string, initial []string, defaults ChannelSettings) (*ChannelStore, error) {
	defaults = normalizeDefaults(defaults)
	store := &ChannelStore{
		Channels: make(map[string]ChannelSettings),
		filePath: path,
		defaults: defaults,
	}

	file, err := os.ReadFile(path)
	switch {
	case err == nil:
		store.Channels, err = loadChannelSettings(file, defaults)
		if err != nil {
			return nil, err
		}
	case os.IsNotExist(err):
		err = nil
	default:
		return nil, err
	}

	for _, ch := range initial {
		settings, ok := store.Channels[ch]
		if !ok {
			settings = store.defaultSettings()
		}
		settings.Enabled = true
		if settings.Backend == "" {
			settings.Backend = defaults.Backend
		}
		store.Channels[ch] = settings
	}

	if err := store.Save(); err != nil {
		return nil, err
	}

	return store, nil
}

func (cs *ChannelStore) Save() error {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.saveLocked()
}

func (cs *ChannelStore) saveLocked() error {
	data, err := json.MarshalIndent(persistedChannelStore{
		Channels: toPersistedChannelSettings(cs.Channels),
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cs.filePath, data, 0644)
}

func (cs *ChannelStore) Enable(channelID string, backend string, interactionSelectEnabled *bool) (ChannelSettings, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	settings, ok := cs.Channels[channelID]
	if !ok {
		settings = cs.defaultSettings()
	}

	settings.Enabled = true
	if backend != "" {
		settings.Backend = backend
	}
	if settings.Backend == "" {
		settings.Backend = cs.defaults.Backend
	}
	if interactionSelectEnabled != nil {
		settings.InteractionSelectEnabled = *interactionSelectEnabled
	}

	cs.Channels[channelID] = settings
	return settings, cs.saveLocked()
}

func (cs *ChannelStore) Disable(channelID string) (ChannelSettings, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	settings, ok := cs.Channels[channelID]
	if !ok {
		settings = cs.defaultSettings()
	}

	settings.Enabled = false
	if settings.Backend == "" {
		settings.Backend = cs.defaults.Backend
	}

	cs.Channels[channelID] = settings
	return settings, cs.saveLocked()
}

func (cs *ChannelStore) Has(channelID string) bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	settings, ok := cs.Channels[channelID]
	return ok && settings.Enabled
}

func (cs *ChannelStore) Get(channelID string) (ChannelSettings, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	settings, ok := cs.Channels[channelID]
	if !ok {
		return cs.defaultSettings(), false
	}
	if settings.Backend == "" {
		settings.Backend = cs.defaults.Backend
	}
	return settings, true
}

func (cs *ChannelStore) defaultSettings() ChannelSettings {
	return ChannelSettings{
		Enabled:                  false,
		Backend:                  cs.defaults.Backend,
		InteractionSelectEnabled: cs.defaults.InteractionSelectEnabled,
	}
}

func normalizeDefaults(defaults ChannelSettings) ChannelSettings {
	if defaults.Backend == "" {
		defaults.Backend = "TranslateAPI"
	}
	defaults.Enabled = false
	return defaults
}

func loadChannelSettings(data []byte, defaults ChannelSettings) (map[string]ChannelSettings, error) {
	var current persistedChannelStore
	if err := json.Unmarshal(data, &current); err == nil && current.Channels != nil {
		settings := make(map[string]ChannelSettings, len(current.Channels))
		for channelID, persisted := range current.Channels {
			enabled := true
			if persisted.Enabled != nil {
				enabled = *persisted.Enabled
			}

			interactionSelectEnabled := defaults.InteractionSelectEnabled
			if persisted.InteractionSelectEnabled != nil {
				interactionSelectEnabled = *persisted.InteractionSelectEnabled
			}

			backend := defaults.Backend
			if persisted.Backend != "" {
				backend = persisted.Backend
			}

			settings[channelID] = ChannelSettings{
				Enabled:                  enabled,
				Backend:                  backend,
				InteractionSelectEnabled: interactionSelectEnabled,
			}
		}
		return settings, nil
	}

	return nil, errors.New("could not parse channel settings file")
}

func toPersistedChannelSettings(channels map[string]ChannelSettings) map[string]persistedChannelSettings {
	result := make(map[string]persistedChannelSettings, len(channels))
	for channelID, settings := range channels {
		enabled := settings.Enabled
		interactionSelectEnabled := settings.InteractionSelectEnabled
		result[channelID] = persistedChannelSettings{
			Enabled:                  &enabled,
			Backend:                  settings.Backend,
			InteractionSelectEnabled: &interactionSelectEnabled,
		}
	}
	return result
}
