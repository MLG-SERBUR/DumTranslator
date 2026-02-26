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
	ApiKey string
	HTTP   *http.Client
}

func NewCerebras(apiKey string) *Cerebras {
	return &Cerebras{
		ApiKey: apiKey,
		HTTP:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Cerebras) DisplayName() string {
	return "Cerebras"
}

func (c *Cerebras) Translate(text string, source string) (*TranslateResponse, error) {
	log.Printf("Translating text with Cerebras: %s", text)

	prompt := fmt.Sprintf("only translate this text to english, nothing else: %s", text)
	reqBody := ChatRequest{
		Model: "gpt-oss-120b",
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

