package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
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
		BaseURL: "https://translateapi.ai/api/v1", // Updated to v1
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

func (c *Client) Translate(text string) (string, error) {
	reqBody := TranslateRequest{
		Text:   text,
		Target: "en",
        // Source left empty to default to "auto"
	}
	
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

    // Endpoint is /translate/ (trailing slash confirmed by user example)
	url := fmt.Sprintf("%s/translate/", c.BaseURL)
	
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.ApiKey) 

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("api returned status: %d", resp.StatusCode)
	}

	var result TranslateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
    
    if result.Error != "" {
        return "", fmt.Errorf("api error: %s", result.Error)
    }

    if result.TranslatedText == "" {
         return "", fmt.Errorf("empty translation received")
    }

	return result.TranslatedText, nil
}
