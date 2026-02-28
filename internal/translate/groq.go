package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"
)

type GroqClient struct {
	ApiKey string
	Model  string
	HTTP   *http.Client
	// NEW: Rate limiting fields
	lastReqTime time.Time
	mu          sync.Mutex
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

// GroqVerboseResponse represents the verbose_json structure from OpenAI/Groq APIs
type GroqVerboseResponse struct {
	Text     string        `json:"text"`
	Segments []GroqSegment `json:"segments"`
}

type GroqSegment struct {
	ID               int     `json:"id"`
	Start            float64 `json:"start"`
	End              float64 `json:"end"`
	Text             string  `json:"text"`
	AvgLogprob       float64 `json:"avg_logprob"`
	CompressionRatio float64 `json:"compression_ratio"`
	NoSpeechProb     float64 `json:"no_speech_prob"`
}

func (c *GroqClient) TranslateAudio(audioData []byte, filename string, prompt string) (string, string, error) {
	// --- RATE LIMITER START ---
	// 20 RPM = 1 request every 3 seconds.
	// We force a sleep if we are moving too fast.
	c.mu.Lock()
	elapsed := time.Since(c.lastReqTime)
	if elapsed < 3*time.Second {
		sleepTime := 3*time.Second - elapsed
		// Optional: Log if we are throttling heavily
		if sleepTime > 1*time.Second {
			log.Printf("Rate limiting: sleeping for %v", sleepTime)
		}
		time.Sleep(sleepTime)
	}
	c.lastReqTime = time.Now()
	c.mu.Unlock()
	// --- RATE LIMITER END ---

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// 1. Add the audio file
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", "", err
	}
	_, err = io.Copy(part, bytes.NewReader(audioData))
	if err != nil {
		return "", "", err
	}

	// 2. Add standard fields
	_ = writer.WriteField("model", c.Model)
	_ = writer.WriteField("response_format", "verbose_json")
	_ = writer.WriteField("temperature", "0")

	if prompt != "" {
		_ = writer.WriteField("prompt", prompt)
	}
	err = writer.Close()
	if err != nil {
		return "", "", err
	}

	req, err := http.NewRequest("POST", "https://api.groq.com/openai/v1/audio/translations", body)
	if err != nil {
		return "", "", err
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.ApiKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		// If we STILL hit a 429 despite our sleep, log it clearly
		if resp.StatusCode == 429 {
			return "", "", fmt.Errorf("RATE LIMIT HIT (429): %s", string(respBody))
		}
		return "", "", fmt.Errorf("groq api returned status: %d, body: %s", resp.StatusCode, string(respBody))
	}

	// ... (Rest of the JSON decoding and segment processing logic remains the same) ...
	var result GroqVerboseResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", err
	}

	// ... Copy the rest of your segment filtering logic here ...
	// (Omitted for brevity as it was correct in your original code)

	// Quick helper to return the result formatted as your original code expects:
	var validTextChunks []string
	var debugLogs []string
	for _, seg := range result.Segments {
		// ... include your logic to filter segments ...
		validTextChunks = append(validTextChunks, seg.Text)
	}

	finalText := strings.Join(validTextChunks, " ")
	return strings.TrimSpace(finalText), strings.Join(debugLogs, " | "), nil
}

// filterHallucinations catches common Whisperisms that slip past the math filters
func filterHallucinations(text string) string {
	original := text

	// Normalize for comparison
	lower := strings.ToLower(strings.TrimSpace(text))
	lower = strings.ReplaceAll(lower, ".", "")
	lower = strings.ReplaceAll(lower, "!", "")
	lower = strings.ReplaceAll(lower, "?", "")
	lower = strings.ReplaceAll(lower, ",", "")
	lower = strings.TrimSpace(lower)

	// A list of standard Youtube/Podcast training-data hallucinations
	hallucinations := map[string]bool{
		"thank you":                    true,
		"thanks":                       true,
		"bye":                          true,
		"goodbye":                      true,
		"thanks for watching":          true,
		"thank you for watching":       true,
		"please subscribe":             true,
		"subscribe":                    true,
		"subscribe to the channel":     true,
		"thank you very much":          true,
		"thanks guys":                  true,
		"i'll see you in the next one": true,
		"if you'd like to subscribe":   true,
	}

	if hallucinations[lower] {
		return "" // Drop this chunk completely
	}

	// If it's a valid string, return the original (with proper capitalization/punctuation)
	return original
}
