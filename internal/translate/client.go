package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

type Client struct {
	ApiKey  string
	BaseURL string
	HTTP    *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		ApiKey:  apiKey,
		BaseURL: "https://api.translateapi.ai/api/v1", // Updated to v1
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
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
    Error          string `json:"error,omitempty"` // Keeping just in case, though not in example success response
}

func (c *Client) Translate(text string) (*TranslateResponse, error) {
	log.Printf("Translating text: %s", text)
	reqBody := TranslateRequest{
		Text:   text,
		Target: "en",
        // Source left empty to default to "auto"
	}
	
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

    // Endpoint is /translate/ (trailing slash confirmed by user example)
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
