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

func (c *GroqClient) TranslateAudio(audioData []byte, filename string) (string, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// 1. Add the audio file
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(part, bytes.NewReader(audioData))
	if err != nil {
		return "", err
	}

	// 2. Add standard fields
	_ = writer.WriteField("model", c.Model)

	// REQUEST VERBOSE JSON to get access to segment probabilities
	_ = writer.WriteField("response_format", "verbose_json")

	// 3. Set Temperature to 0 for deterministic outputs
	_ = writer.WriteField("temperature", "0")

	// 4. POSITIVE PROMPT ONLY
	// Avoid negative prompts ("Do not say thank you"). Negative prompts prime Whisper's
	// short-term memory with the exact words you are trying to avoid.
	_ = writer.WriteField("prompt", "This is a transcript of an ongoing casual Discord voice conversation.")

	err = writer.Close()
	if err != nil {
		return "", err
	}

	// Note: We use translations to output English, or you can use transcriptions for original language
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

	var result GroqVerboseResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	// 5. PROCESS SEGMENTS
	var validTextChunks []string
	for _, seg := range result.Segments {
		// Rule A: If Whisper is > 60% sure there is no actual speech here, drop it.
		// This mathematically catches silence/breathing turning into "Thank you."
		if seg.NoSpeechProb > 0.6 {
			log.Printf("high no_speech_prob: '%s' (no_speech_prob=%.2f)", seg.Text, seg.NoSpeechProb)
			continue
		}

		// Rule B: If the compression ratio is unusually high, it's a repeating loop hallucination.
		// (e.g. "Thank you. Thank you. Thank you.")
		if seg.CompressionRatio > 2.4 {
			log.Printf("high compression_ratio: '%s' (compression_ratio=%.2f)", seg.Text, seg.CompressionRatio)
			continue
		}

		// Rule C: Pass the remaining text through our exact-match string filter
		cleanedText := filterHallucinations(seg.Text)
		if cleanedText == "" {
			log.Printf("Blacklisted text: %q", seg.Text)
			continue
		}

		validTextChunks = append(validTextChunks, cleanedText)
	}

	// Combine valid segments back together
	finalText := strings.Join(validTextChunks, " ")
	return strings.TrimSpace(finalText), nil
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
		"thank you": true,
		"thanks":    true,
		"bye":       true,
		"goodbye":   true,
	}

	if hallucinations[lower] {
		return "" // Drop this chunk completely
	}

	// If it's a valid string, return the original (with proper capitalization/punctuation)
	return original
}
