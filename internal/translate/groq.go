package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

type GroqClient struct {
	ApiKey string
	Model  string
	HTTP   *http.Client
}

func NewGroqClient(apiKey string, model string) *GroqClient {
	if model == "" {
		model = "whisper-large-v3-turbo"
	}
	return &GroqClient{
		ApiKey: apiKey,
		Model:  model,
		HTTP:   &http.Client{Timeout: 30 * time.Second},
	}
}

type GroqSTTResponse struct {
	Text string `json:"text"`
}

func (c *GroqClient) TranslateAudio(audioData []byte, filename string) (string, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(part, bytes.NewReader(audioData))
	if err != nil {
		return "", err
	}

	_ = writer.WriteField("model", c.Model)
	_ = writer.WriteField("response_format", "json")

	err = writer.Close()
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", "https://api.groq.com/openai/v1/audio/translations", body)
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.ApiKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("groq api returned status: %d, body: %s", resp.StatusCode, string(respBody))
	}

	var result GroqSTTResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	return result.Text, nil
}
