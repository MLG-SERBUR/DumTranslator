package translate

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTranslateAPI_Translate_UnknownSource(t *testing.T) {
	var capturedSource string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req TranslateRequest
		json.Unmarshal(body, &req)
		capturedSource = req.Source
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TranslateResponse{
			TranslatedText: "hello",
			SourceLanguage: "ar",
			TargetLanguage: "en",
		})
	}))
	defer server.Close()

	api := NewTranslateAPI("test-key")
	api.BaseURL = server.URL

	_, err := api.Translate("test", "unknown")
	if err != nil {
		t.Fatalf("Translate failed: %v", err)
	}

	if capturedSource != "" {
		t.Errorf("expected empty source language for 'unknown', got %q", capturedSource)
	}
}
