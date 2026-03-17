package config

import (
	"encoding/json"
	"os"
	"sync"
)

type Config struct {
	DiscordToken    string   `json:"discord_token"`
	TranslateAPIKey string   `json:"translate_api_key"`
	MyMemoryEmail   string   `json:"mymemory_email"`
	CerebrasAPIKey  string   `json:"cerebras_api_key"`
	CerebrasModel   string   `json:"cerebras_model"`
	MistralAPIKey   string   `json:"mistral_api_key"`
	MistralModel    string   `json:"mistral_model"`
	ArliAIAPIKey    string   `json:"arliai_api_key"`
	ArliAIModel     string   `json:"arliai_model"`
	GroqAPIKey      string   `json:"groq_api_key"`
	CaptionsEnabled *bool    `json:"captions_enabled"` // Pointer to distinguish between missing and false
	STTModel        string   `json:"stt_model"`        // Default: whisper-large-v3-turbo
	Backend         string   `json:"backend"`          // "TranslateAPI", "MyMemory", "Cerebras", "Mistral", "ArliAI", "Google"
	TargetChannels  []string `json:"target_channels"` // Initial channels from config
}

// ChannelStore manages persistent storage of channels to listen to
type ChannelStore struct {
	Channels map[string]bool `json:"channels"`
	FilePath string
	mu       sync.RWMutex
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

	return &cfg, nil
}

func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func NewChannelStore(path string, initial []string) (*ChannelStore, error) {
	store := &ChannelStore{
		Channels: make(map[string]bool),
		FilePath: path,
	}

	// Try to load existing
	file, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(file, &store)
	}

	// Add initial from config if not present (optional logic, or just merge)
	for _, ch := range initial {
		store.Channels[ch] = true
	}
    
    // Save immediately to ensure file exists and is consistent
    _ = store.Save()

	return store, nil
}

func (cs *ChannelStore) Save() error {
	data, err := json.MarshalIndent(cs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cs.FilePath, data, 0644)
}

func (cs *ChannelStore) Add(channelID string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.Channels[channelID] = true
	return cs.Save()
}

func (cs *ChannelStore) Remove(channelID string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	delete(cs.Channels, channelID)
	return cs.Save()
}

func (cs *ChannelStore) Has(channelID string) bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.Channels[channelID]
}
