package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
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

	// REQUEST VERBOSE JSON to get access to segment probabilities
	_ = writer.WriteField("response_format", "verbose_json")

	// 3. Set Temperature to 0 for deterministic outputs
	_ = writer.WriteField("temperature", "0")

	// 4. Add the prompt if one is provided
	if prompt != "" {
		_ = writer.WriteField("prompt", prompt)
	}
	err = writer.Close()
	if err != nil {
		return "", "", err
	}

	// Note: We use translations to output English, or you can use transcriptions for original language
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
		return "", "", fmt.Errorf("groq api returned status: %d, body: %s", resp.StatusCode, string(respBody))
	}

	var result GroqVerboseResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", err
	}

	// 5. PROCESS SEGMENTS
	var validSegments []GroqSegment
	var debugLogs []string
	for _, seg := range result.Segments {
		// Collect debug info for the footer
		debugLogs = append(debugLogs, fmt.Sprintf("no_speech: %.2f, comp: %.2f", seg.NoSpeechProb, seg.CompressionRatio))

		// Rule A: If Whisper is > 60% sure there is no actual speech here, drop it.
		// This mathematically catches silence/breathing turning into "Thank you."
		if seg.NoSpeechProb > 0.2 {
			log.Printf("high no_speech_prob: '%s' (no_speech_prob=%.2f, compression_ratio=%.2f, avg_logprob=%.2f)", seg.Text, seg.NoSpeechProb, seg.CompressionRatio, seg.AvgLogprob)
			continue
		}

		if seg.AvgLogprob < -0.5 {
			log.Printf("low avg_logprob: '%s' (no_speech_prob=%.2f, compression_ratio=%.2f, avg_logprob=%.2f)", seg.Text, seg.NoSpeechProb, seg.CompressionRatio, seg.AvgLogprob)
			continue
		}

		// Rule B: If the compression ratio is unusually high, it's a repeating loop hallucination.
		// (e.g. "Thank you. Thank you. Thank you.")
		if seg.CompressionRatio > 2.0 {
			log.Printf("high compression_ratio: '%s' (no_speech_prob=%.2f, compression_ratio=%.2f, avg_logprob=%.2f)", seg.Text, seg.NoSpeechProb, seg.CompressionRatio, seg.AvgLogprob)
			continue
		}

		// Rule C: Pass the remaining text through our exact-match string filter
		cleanedText := filterHallucinations(seg.Text)
		if cleanedText == "" {
			log.Printf("Blacklisted text: %q (no_speech_prob=%.2f, compression_ratio=%.2f, avg_logprob=%.2f)", seg.Text, seg.NoSpeechProb, seg.CompressionRatio, seg.AvgLogprob)
			continue
		}

		log.Printf("'%s' (no_speech_prob=%.2f, compression_ratio=%.2f, avg_logprob=%.2f)", seg.Text, seg.NoSpeechProb, seg.CompressionRatio, seg.AvgLogprob)

		// Update segment text with cleaned version
		seg.Text = cleanedText

		// Check for overlapping segments
		if len(validSegments) == 0 {
			validSegments = append(validSegments, seg)
		} else {
			lastIdx := len(validSegments) - 1
			lastSeg := validSegments[lastIdx]

			overlapStart := math.Max(lastSeg.Start, seg.Start)
			overlapEnd := math.Min(lastSeg.End, seg.End)
			overlapDuration := overlapEnd - overlapStart
			if overlapDuration < 0 {
				overlapDuration = 0
			}

			lastDuration := lastSeg.End - lastSeg.Start
			segDuration := seg.End - seg.Start
			minDuration := math.Min(lastDuration, segDuration)

			// If segments overlap by 50% or more of the smaller segment's duration,
			// they are describing the same audio. Replace the last segment if the new one is longer.
			if minDuration > 0 && overlapDuration >= 0.5*minDuration {
				if len(strings.TrimSpace(seg.Text)) > len(strings.TrimSpace(lastSeg.Text)) {
					log.Printf("Overlapping segments detected. Replacing '%s' with '%s'", lastSeg.Text, seg.Text)
					validSegments[lastIdx] = seg
				}
			} else {
				validSegments = append(validSegments, seg)
			}
		}
	}

	var validTextChunks []string
	for _, seg := range validSegments {
		validTextChunks = append(validTextChunks, seg.Text)
	}

	// Combine valid segments back together
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
