package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

type Translator interface {
	Translate(text string, source string) (*TranslateResponse, error)
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

func (c *TranslateAPI) Translate(text string, source string) (*TranslateResponse, error) {
	log.Printf("Translating text with TranslateAPI: %s", text)
	if source == "unknown" {
		source = ""
	}
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

func (m *MyMemory) Translate(text string, source string) (*TranslateResponse, error) {
	log.Printf("Translating text with MyMemory: %s", text)

	if source == "" || source == "unknown" {
		source = "auto"
	}

	// 1. Create a Values struct to hold query parameters
	params := url.Values{}
	params.Set("q", text)                       // This automatically encodes Arabic/Spaces
	params.Set("langpair", source+"|en")        // This handles the pipe character "|"
	if m.Email != "" {
		params.Set("de", m.Email)
	}

	// 2. Construct the full URL with encoded parameters
	// "Encode()" turns "هذا اختبار" into "%D9%85%D9%87..."
	requestURL := "https://api.mymemory.translated.net/get?" + params.Encode()

	// 3. DEBUG LOGGING (As requested)
	log.Printf("DEBUG: Requesting URL: %s", requestURL)

	resp, err := m.HTTP.Get(requestURL)
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
		// MyMemory sometimes returns 200 OK in headers but 403/500 in JSON body
		return nil, fmt.Errorf("mymemory error status: %d", mmResp.ResponseStatus)
	}

	// Extract source language from first match if available
	sourceLang := "unknown"
	if len(mmResp.Matches) > 0 {
		sourceLang = mmResp.Matches[0].SourceLanguage
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


type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model       string         `json:"model"`
	Messages    []ChatMessage  `json:"messages"`
	Temperature float64        `json:"temperature"`
	Stream      bool           `json:"stream"`
	MaxTokens   int            `json:"max_tokens,omitempty"`
}

type ChatResponse struct {
	Choices []struct {
		Message ChatMessage `json:"message"`
	} `json:"choices"`
}

type Cerebras struct {
	ApiKey              string
	Model               string
	Prompt              string
	DisplayNameOverride string
	HTTP                *http.Client
}

func NewCerebras(apiKey string, model string) *Cerebras {
	if model == "" {
		model = "gpt-oss-120b"
	}
	return &Cerebras{
		ApiKey: apiKey,
		Model:  model,
		Prompt: "Only translate this text to english; do not output anything else: %s",
		HTTP:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Cerebras) DisplayName() string {
	if c.DisplayNameOverride != "" {
		return c.DisplayNameOverride
	}
	return fmt.Sprintf("Cerebras (%s)", c.Model)
}

func (c *Cerebras) Translate(text string, source string) (*TranslateResponse, error) {
	log.Printf("Translating text with %s: %s", c.DisplayName(), text)

	prompt := fmt.Sprintf(c.Prompt, text)
	reqBody := ChatRequest{
		Model: c.Model,
		Messages: []ChatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature: 0,
		Stream:      false,
		MaxTokens:   -1,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", "https://api.cerebras.ai/v1/chat/completions", bytes.NewBuffer(jsonBody))
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
		return nil, fmt.Errorf("cerebras api returned status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, err
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices returned from Cerebras")
	}

	return &TranslateResponse{
		TranslatedText: chatResp.Choices[0].Message.Content,
		SourceLanguage: source, // Use detected source
		TargetLanguage: "en",
	}, nil
}

type Mistral struct {
	ApiKey              string
	Model               string
	Prompt              string
	DisplayNameOverride string
	HTTP                *http.Client
}

func NewMistral(apiKey string, model string) *Mistral {
	if model == "" {
		model = "mistral-large-latest"
	}
	return &Mistral{
		ApiKey: apiKey,
		Model:  model,
		Prompt: "Only translate this text to english; do not output anything else: %s",
		HTTP:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (m *Mistral) DisplayName() string {
	if m.DisplayNameOverride != "" {
		return m.DisplayNameOverride
	}
	return fmt.Sprintf("Mistral (%s)", m.Model)
}

func (m *Mistral) Translate(text string, source string) (*TranslateResponse, error) {
	log.Printf("Translating text with %s: %s", m.DisplayName(), text)

	prompt := fmt.Sprintf(m.Prompt, text)
	reqBody := ChatRequest{
		Model: m.Model,
		Messages: []ChatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature: 0,
		Stream:      false,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", "https://api.mistral.ai/v1/chat/completions", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.ApiKey)

	resp, err := m.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("mistral api returned status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, err
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices returned from Mistral")
	}

	return &TranslateResponse{
		TranslatedText: chatResp.Choices[0].Message.Content,
		SourceLanguage: source, // Use detected source
		TargetLanguage: "en",
	}, nil
}

// ArliAI implementation (DISABLED: Do not re-enable without explicit request)
type ArliAI struct {
	ApiKey              string
	Model               string
	Prompt              string
	DisplayNameOverride string
	HTTP                *http.Client
}

func NewArliAI(apiKey string, model string) *ArliAI {
	if model == "" {
		model = "Gemma-3-27B-it"
	}
	return &ArliAI{
		ApiKey: apiKey,
		Model:  model,
		Prompt: "Only translate this text to english; do not output anything else: %s",
		HTTP:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (a *ArliAI) DisplayName() string {
	if a.DisplayNameOverride != "" {
		return a.DisplayNameOverride
	}
	return fmt.Sprintf("ArliAI (%s)", a.Model)
}

func (a *ArliAI) Translate(text string, source string) (*TranslateResponse, error) {
	log.Printf("Translating text with %s: %s", a.DisplayName(), text)

	prompt := fmt.Sprintf(a.Prompt, text)
	reqBody := ChatRequest{
		Model: a.Model,
		Messages: []ChatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature: 0,
		Stream:      false,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", "https://api.arliai.com/v1/chat/completions", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.ApiKey)

	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("arliai api returned status: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, err
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices returned from ArliAI")
	}

	return &TranslateResponse{
		TranslatedText: chatResp.Choices[0].Message.Content,
		SourceLanguage: source, // Use detected source
		TargetLanguage: "en",
	}, nil
}

type GoogleTranslate struct {
	HTTP *http.Client
}

func NewGoogleTranslate() *GoogleTranslate {
	return &GoogleTranslate{
		HTTP: &http.Client{Timeout: 10 * time.Second},
	}
}

func (g *GoogleTranslate) DisplayName() string {
	return "Google Translate"
}

func (g *GoogleTranslate) Translate(text string, source string) (*TranslateResponse, error) {
	log.Printf("Translating text with Google Translate: %s", text)
	if source == "" || source == "unknown" {
		source = "auto"
	}

	params := url.Values{}
	params.Set("client", "gtx")
	params.Set("sl", source)
	params.Set("tl", "en")
	params.Set("dt", "t")
	params.Set("q", text)

	requestURL := "https://translate.googleapis.com/translate_a/single?" + params.Encode()
	resp, err := g.HTTP.Get(requestURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google translate returned status: %d", resp.StatusCode)
	}

	var result []interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("empty response from google translate")
	}

	// Google's unofficial API returns a nested array
	sentences, ok := result[0].([]interface{})
	if !ok || len(sentences) == 0 {
		return nil, fmt.Errorf("unexpected response format from google translate")
	}

	translatedText := ""
	for _, s := range sentences {
		sentence, ok := s.([]interface{})
		if ok && len(sentence) > 0 {
			if part, ok := sentence[0].(string); ok {
				translatedText += part
			}
		}
	}

	detectedSource := source
	if len(result) > 2 {
		if ds, ok := result[2].(string); ok {
			detectedSource = ds
		}
	}

	return &TranslateResponse{
		TranslatedText: translatedText,
		SourceLanguage: detectedSource,
		TargetLanguage: "en",
	}, nil
}



