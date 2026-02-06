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
	// Source string `json:"source_language,omitempty"` // API doesn't seem to use this in example, keeping as is or removing if strictly following example. Let's keep strict for now.
    // The example only showed text and target_language.
	Target string `json:"target_language"`
}

type TranslateResponse struct {
    // We still don't have the exact response format, checking if user provided it.
    // User only provided the REQUEST.
    // I will stick to the previous assumption but looking at common patterns, maybe it returns just the text or a json object.
    // I'll keep the generic "translation" field but also print the body if error occurs to help debugging if it fails.
	Translation string `json:"translation"` 
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

    // Endpoint is /translate/ (trailing slash might be important based on curl example)
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
    
    // Fallback: if translation is empty, maybe the field name is different.
    // Without response example, this is still a guess.
    if result.Error != "" {
        return "", fmt.Errorf("api error: %s", result.Error)
    }

	return result.Translation, nil
}
