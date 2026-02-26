package config

import (
	"encoding/json"
	"os"

)

type Config struct {
	DiscordToken    string   `json:"discord_token"`
	TranslateAPIKey string   `json:"translate_api_key"`
	MyMemoryEmail   string   `json:"mymemory_email"`
	CerebrasAPIKey  string   `json:"cerebras_api_key"`
	CerebrasModel   string   `json:"cerebras_model"`
	MistralAPIKey   string   `json:"mistral_api_key"`
	MistralModel    string   `json:"mistral_model"`
	Backend         string   `json:"backend"`          // "TranslateAPI", "MyMemory", "Cerebras", "Mistral"
	TargetChannels  []string `json:"target_channels"` // Initial channels from config
}

// ChannelStore manages persistent storage of channels to listen to
type ChannelStore struct {
	Channels map[string]bool `json:"channels"`
	FilePath string
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
	cs.Channels[channelID] = true
	return cs.Save()
}

func (cs *ChannelStore) Remove(channelID string) error {
	delete(cs.Channels, channelID)
	return cs.Save()
}

func (cs *ChannelStore) Has(channelID string) bool {
	return cs.Channels[channelID]
}
