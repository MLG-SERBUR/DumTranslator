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
		BaseURL: "https://translateapi.ai/api", // Based on user request
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}

type TranslateRequest struct {
	Text   string `json:"text"`
	Source string `json:"source_language,omitempty"` // Optional, auto-detect if empty
	Target string `json:"target_language"`
}

type TranslateResponse struct {
	Translation string `json:"translation"` 
    // Note: Actual field names strictly depend on the API. 
    // Common alternatives: "translated_text", "data". 
    // Since we don't have exact docs, we're assuming a sensible default 
    // and will need to debug if it differs.
    Error string `json:"error,omitempty"`
}

func (c *Client) Translate(text string) (string, error) {
	reqBody := TranslateRequest{
		Text:   text,
		Target: "en",
	}
	
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

    // Assuming endpoint /translate based on common REST patterns for translation APIs
    // If the user meant the base URL IS the endpoint, we might need to adjust.
    // But usually APIs are /v1/something. 
    // We will try /translate.
	url := fmt.Sprintf("%s/translate", c.BaseURL)
	
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.ApiKey) // Common auth header
    // Some APIs use X-API-KEY. We might need to make this configurable or supported.

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
        // Try to read body for error
        // buf := new(bytes.Buffer)
        // buf.ReadFrom(resp.Body)
		return "", fmt.Errorf("api returned status: %d", resp.StatusCode)
	}

	var result TranslateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
    
    if result.Error != "" {
        return "", fmt.Errorf("api error: %s", result.Error)
    }

    if result.Translation == "" {
        // Fallback or error?
        return "", fmt.Errorf("empty translation received")
    }

	return result.Translation, nil
}
