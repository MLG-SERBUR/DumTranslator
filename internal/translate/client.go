package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	lara_sdk "github.com/translated/lara-go/lara"
)

type Translator interface {
	Translate(text string, source string, context []string) (*TranslateResponse, error)
	DisplayName() string
}

type TranslateAPI struct {
	ApiKey  string
	BaseURL string
	HTTP    *http.Client
}

func NewTranslateAPI(apiKey string) *TranslateAPI {
	return &TranslateAPI{
		ApiKey:  apiKey,
		BaseURL: "https://api.translateapi.ai/api/v1",
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *TranslateAPI) DisplayName() string {
	return "TranslateAPI"
}

type MyMemory struct {
	Email string
	HTTP  *http.Client
}

func NewMyMemory(email string) *MyMemory {
	return &MyMemory{
		Email: email,
		HTTP:  &http.Client{Timeout: 10 * time.Second},
	}
}

func (m *MyMemory) DisplayName() string {
	return "MyMemory"
}

type TranslateRequest struct {
	Text   string `json:"text"`
	Source string `json:"source_language,omitempty"`
	Target string `json:"target_language"`
}

type TranslateResponse struct {
	TranslatedText string `json:"translated_text"`
	SourceLanguage string `json:"source_language"`
	TargetLanguage string `json:"target_language"`
	CharacterCount int    `json:"character_count"`
	Error          string `json:"error,omitempty"`
}

func (c *TranslateAPI) Translate(text string, source string, context []string) (*TranslateResponse, error) {
	log.Printf("Translating text with TranslateAPI: %s", text)
	reqBody := TranslateRequest{
		Text:   text,
		Source: source,
		Target: "en",
	}
	
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/translate/", c.BaseURL)
	
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.ApiKey) 

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("api returned status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var result TranslateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
    
	if result.Error != "" {
		return nil, fmt.Errorf("api error: %s", result.Error)
	}

	if result.TranslatedText == "" {
		return nil, fmt.Errorf("empty translation received")
	}

	return &result, nil
}

type MyMemoryResponse struct {
	ResponseData struct {
		TranslatedText string `json:"translatedText"`
	} `json:"responseData"`
	ResponseStatus int    `json:"responseStatus"`
	Matches        []struct {
		Segment        string `json:"segment"`
		Translation    string `json:"translation"`
		SourceLanguage string `json:"source"`
		TargetLanguage string `json:"target"`
	} `json:"matches"`
}

func (m *MyMemory) Translate(text string, source string, context []string) (*TranslateResponse, error) {
	log.Printf("Translating text with MyMemory: %s", text)
	
	if source == "" || source == "unknown" {
		source = "auto"
	}
	
	url := "https://api.mymemory.translated.net/get?q=" + fmt.Sprintf("%s", text) + "&langpair=" + source + "|en"
	if m.Email != "" {
		url += "&de=" + m.Email
	}

	resp, err := m.HTTP.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("mymemory returned status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var mmResp MyMemoryResponse
	if err := json.NewDecoder(resp.Body).Decode(&mmResp); err != nil {
		return nil, err
	}

	if mmResp.ResponseStatus != http.StatusOK {
		return nil, fmt.Errorf("mymemory error status: %d", mmResp.ResponseStatus)
	}

	// Extract source language from first match if available
	sourceLang := "unknown"
	if len(mmResp.Matches) > 0 {
		sourceLang = mmResp.Matches[0].SourceLanguage
		// MyMemory often returns ISO codes. TranslateAPI uses "ar", "ko". 
		// MyMemory might return "ar-SA" or just "ar".
		// We'll normalize to 2 chars for the check in handler.go.
		if len(sourceLang) > 2 {
			sourceLang = sourceLang[:2]
		}
	}

	return &TranslateResponse{
		TranslatedText: mmResp.ResponseData.TranslatedText,
		SourceLanguage: sourceLang,
		TargetLanguage: "en",
	}, nil
}

type LaraTranslate struct {
	Translator *lara_sdk.Translator
}

func NewLaraTranslate(keyID, keySecret string) *LaraTranslate {
	credentials := lara_sdk.NewCredentials(keyID, keySecret)
	lara := lara_sdk.NewTranslator(credentials, nil)
	return &LaraTranslate{
		Translator: lara,
	}
}

func (l *LaraTranslate) DisplayName() string {
	return "LaraTranslate"
}

func (l *LaraTranslate) Translate(text string, source string, context []string) (*TranslateResponse, error) {
	log.Printf("Translating text with LaraTranslate: %s", text)

	var blocks []lara_sdk.TextBlock

	// Add context messages as non-translatable blocks
	for _, ctxMsg := range context {
		blocks = append(blocks, lara_sdk.TextBlock{
			Text:         ctxMsg,
			Translatable: false,
		})
	}

	// Add target message as translatable block
	blocks = append(blocks, lara_sdk.TextBlock{
		Text:         text,
		Translatable: true,
	})

	if source == "" || source == "unknown" {
		source = "auto"
	}

	res, err := l.Translator.Translate(blocks, source, "en", lara_sdk.TranslateOptions{
		ContentType: "text/plain",
		TimeoutMs:   5000,
	})
	if err != nil {
		return nil, err
	}

	if res.Translation.String == nil {
		return nil, fmt.Errorf("lara returned no translation")
	}

	translatedText := *res.Translation.String

	return &TranslateResponse{
		TranslatedText: translatedText,
		SourceLanguage: source,
		TargetLanguage: "en",
	}, nil
}

type LaraTranslate2 struct {
	Translator *lara_sdk.Translator
}

func NewLaraTranslate2(keyID, keySecret string) *LaraTranslate2 {
	credentials := lara_sdk.NewCredentials(keyID, keySecret)
	lara := lara_sdk.NewTranslator(credentials, nil)
	return &LaraTranslate2{
		Translator: lara,
	}
}

func (l *LaraTranslate2) DisplayName() string {
	return "LaraTranslate2"
}

func (l *LaraTranslate2) Translate(text string, source string, context []string) (*TranslateResponse, error) {
	log.Printf("Translating text with LaraTranslate2: %s", text)

	if source == "" || source == "unknown" {
		source = "" // Omit for auto detection
	}

	res, err := l.Translator.Translate(text, source, "en", lara_sdk.TranslateOptions{
		ContentType: "text/plain",
		TimeoutMs:   2000,
	})
	if err != nil {
		return nil, err
	}

	if res.Translation.String == nil {
		return nil, fmt.Errorf("lara returned no translation")
	}

	return &TranslateResponse{
		TranslatedText: *res.Translation.String,
		SourceLanguage: source,
		TargetLanguage: "en",
	}, nil
}
