package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigAppliesDefaults(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"discord_token":"token"}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	if cfg.Backend != "TranslateAPI" {
		t.Fatalf("expected default backend TranslateAPI, got %q", cfg.Backend)
	}
	if cfg.InteractionSelectEnabled == nil || !*cfg.InteractionSelectEnabled {
		t.Fatalf("expected interaction select default to be enabled")
	}
	if cfg.CaptionsEnabled == nil || !*cfg.CaptionsEnabled {
		t.Fatalf("expected captions default to be enabled")
	}
	if cfg.STTModel != "whisper-large-v3-turbo" {
		t.Fatalf("expected default stt model, got %q", cfg.STTModel)
	}
}

func TestNewChannelStoreLoadsCurrentFormat(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "channels.json")
	if err := os.WriteFile(path, []byte(`{"channels":{"123":{"enabled":true,"backend":"Google","interaction_select_enabled":false}}}`), 0644); err != nil {
		t.Fatalf("write channels file: %v", err)
	}

	defaults := ChannelSettings{
		Backend:                  "Google",
		InteractionSelectEnabled: false,
	}

	store, err := NewChannelStore(path, nil, defaults)
	if err != nil {
		t.Fatalf("NewChannelStore returned error: %v", err)
	}

	settings, ok := store.Get("123")
	if !ok {
		t.Fatalf("expected channel to exist")
	}
	if !settings.Enabled {
		t.Fatalf("expected channel to remain enabled")
	}
	if settings.Backend != "Google" {
		t.Fatalf("expected backend to be loaded, got %q", settings.Backend)
	}
	if settings.InteractionSelectEnabled {
		t.Fatalf("expected interaction select to be loaded")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read channels file: %v", err)
	}
	expected := `{"channels":{"123":{"enabled":true,"backend":"Google","interaction_select_enabled":false}}}`
	if compactJSON(string(data)) != expected {
		t.Fatalf("unexpected file contents: %s", data)
	}
}

func TestChannelStorePersistsPerChannelSettings(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "channels.json")
	defaults := ChannelSettings{
		Backend:                  "TranslateAPI",
		InteractionSelectEnabled: true,
	}

	store, err := NewChannelStore(path, nil, defaults)
	if err != nil {
		t.Fatalf("NewChannelStore returned error: %v", err)
	}

	updated, err := store.Enable("alpha", "Google", boolPtr(false))
	if err != nil {
		t.Fatalf("Enable returned error: %v", err)
	}
	if !updated.Enabled || updated.Backend != "Google" || updated.InteractionSelectEnabled {
		t.Fatalf("unexpected enabled settings: %+v", updated)
	}

	updated, err = store.Disable("alpha")
	if err != nil {
		t.Fatalf("Disable returned error: %v", err)
	}
	if updated.Enabled {
		t.Fatalf("expected channel to be disabled")
	}

	reloaded, err := NewChannelStore(path, nil, defaults)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}

	settings, ok := reloaded.Get("alpha")
	if !ok {
		t.Fatalf("expected disabled channel settings to persist")
	}
	if settings.Enabled {
		t.Fatalf("expected channel to remain disabled after reload")
	}
	if settings.Backend != "Google" {
		t.Fatalf("expected backend to persist, got %q", settings.Backend)
	}
	if settings.InteractionSelectEnabled {
		t.Fatalf("expected interaction select setting to persist")
	}

	reenabled, err := reloaded.Enable("alpha", "", nil)
	if err != nil {
		t.Fatalf("re-enable channel: %v", err)
	}
	if !reenabled.Enabled {
		t.Fatalf("expected channel to be re-enabled")
	}
	if reenabled.Backend != "Google" || reenabled.InteractionSelectEnabled {
		t.Fatalf("expected saved settings to be reused on re-enable, got %+v", reenabled)
	}

	fresh, err := reloaded.Enable("beta", "", nil)
	if err != nil {
		t.Fatalf("enable new channel: %v", err)
	}
	if !fresh.Enabled || fresh.Backend != "TranslateAPI" || !fresh.InteractionSelectEnabled {
		t.Fatalf("expected defaults for new channel, got %+v", fresh)
	}
}

func boolPtr(v bool) *bool {
	return &v
}

func compactJSON(value string) string {
	result := make([]rune, 0, len(value))
	for _, r := range value {
		switch r {
		case ' ', '\n', '\r', '\t':
			continue
		default:
			result = append(result, r)
		}
	}
	return string(result)
}
