package captions

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4/pkg/media/oggwriter"
	"github.com/user/dumtranslator/internal/discord/captions/tenvad"
	"github.com/user/dumtranslator/internal/translate"
	"layeh.com/gopus"
)

// RateLimitInterval controls how often we send audio to Groq.
// You can lower this (e.g., 1 * time.Second) for faster captions,
// but it will use more API credits.
//
// NOTE: This is different from the Discord "wsHeartbeat". The Discord heartbeat
// is automatic and cannot be changed manually (it is negotiated with the server).
const RateLimitInterval = 3 * time.Second

type Manager struct {
	Session  *discordgo.Session
	Groq     *translate.GroqClient
	Sessions map[string]*VoiceSession // GuildID -> VoiceSession
	// Global Rate Limit Tracker
	NextReqTime time.Time
	ReqMu       sync.Mutex
	mu          sync.Mutex
}

type VoiceSession struct {
	GuildID      string
	ChannelID    string
	VC           *discordgo.VoiceConnection
	UserLogs     []string // List of "User: Text"
	EmbedMsgID   string
	TextMsgID    string // The channel where the commands are typed
	SSRCtoUser   map[uint32]string
	LastUserText map[uint32]string // Tracks the previous text per-user for the Whisper prompt
	Done         chan bool
	mu           sync.Mutex
}

func NewManager(s *discordgo.Session, groq *translate.GroqClient) *Manager {
	return &Manager{
		Session:  s,
		Groq:     groq,
		Sessions: make(map[string]*VoiceSession),
	}
}

func (m *Manager) Start(guildID, channelID string, tcID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.Sessions[guildID]; ok {
		return fmt.Errorf("captions already running in this guild")
	}

	vs := &VoiceSession{
		GuildID:      guildID,
		ChannelID:    channelID,
		UserLogs:     []string{},
		SSRCtoUser:   make(map[uint32]string),
		LastUserText: make(map[uint32]string), // Initialize the new map
		Done:         make(chan bool),
		TextMsgID:    tcID,
	}

	// 1. Pre-create the VoiceConnection and attach the handler BEFORE connecting.
	// This prevents the race condition where we miss the initial SSRC mapping
	// payloads sent by Discord during the connection handshake.
	m.Session.RLock()
	vc, ok := m.Session.VoiceConnections[guildID]
	m.Session.RUnlock()

	if !ok || vc == nil {
		vc = &discordgo.VoiceConnection{}
		m.Session.Lock()
		if m.Session.VoiceConnections == nil {
			m.Session.VoiceConnections = make(map[string]*discordgo.VoiceConnection)
		}
		m.Session.VoiceConnections[guildID] = vc
		m.Session.Unlock()
	}
	vs.VC = vc

	// Register the handler early
	vc.AddHandler(func(vc *discordgo.VoiceConnection, vsUpdate *discordgo.VoiceSpeakingUpdate) {
		// Discord sometimes omits the UserID in subsequent speaking updates to save bandwidth.
		// We only want to update our map if the UserID is actually provided!
		if vsUpdate.UserID != "" {
			vs.mu.Lock()
			vs.SSRCtoUser[uint32(vsUpdate.SSRC)] = vsUpdate.UserID
			vs.mu.Unlock()
		}
	})

	// 2. Now join the channel. It will inherit the pre-created VoiceConnection.
	// Because the handler is already there, it will catch the first initial events!
	vc, err := m.Session.ChannelVoiceJoin(guildID, channelID, false, false)
	if err != nil {
		// Cleanup the pre-allocated vc on failure
		m.Session.Lock()
		delete(m.Session.VoiceConnections, guildID)
		m.Session.Unlock()
		return err
	}
	vs.VC = vc // Ensure we assign the connected interface

	// Disable internal retries if possible, or handle disconnection
	vc.LogLevel = discordgo.LogDebug

	m.Sessions[guildID] = vs

	// 3. Start the Connection Monitor
	// This ensures we do NOT attempt reconnect loops. If the connection drops, we kill it.
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		// Phase 1: Wait for Initial Connection (up to 10 seconds)
		connected := false
		attempts := 0
		for !connected {
			select {
			case <-vs.Done:
				return // Manual stop
			case <-ticker.C:
				attempts++
				if vs.VC != nil && vs.VC.Ready {
					connected = true
					log.Printf("Voice connection ready.")
				} else if attempts > 20 { // 10 seconds (20 * 500ms)
					log.Printf("Voice connection timed out (Initial), stopping.")
					m.Stop(guildID)
					return
				}
			}
		}

		// Phase 2: Monitor for Drops
		// If Ready becomes false after being true, we Stop immediately.
		for {
			select {
			case <-vs.Done:
				return
			case <-ticker.C:
				if vs.VC == nil || !vs.VC.Ready {
					log.Printf("Voice connection dropped (Ready=false), enforcing STOP to prevent reconnect.")
					m.Stop(guildID)
					return
				}
			}
		}
	}()

	go m.listenLoop(vs)

	// Create initial embed
	embed := &discordgo.MessageEmbed{
		Title:       "Translated Captions",
		Description: "Listening for voices...",
		Color:       0x00ff00,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Powered by Groq (Large-Whisper-v3)",
		},
	}
	msg, err := m.Session.ChannelMessageSendEmbed(tcID, embed)
	if err != nil {
		vc.Disconnect()
		return err
	}
	vs.EmbedMsgID = msg.ID

	return nil
}

func (m *Manager) Stop(guildID string) error {
	m.mu.Lock()
	vs, ok := m.Sessions[guildID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("no captions running in this guild")
	}

	// 1. Check if already closed
	select {
	case <-vs.Done:
		m.mu.Unlock()
		return nil
	default:
	}

	// 2. Signal goroutines to stop
	close(vs.Done)

	// 3. PHYSICALLY LEAVE the channel
	// We call Disconnect() which sends the Opcode 4 (Gateway Voice State Update)
	// to Discord telling them we are leaving.
	if vs.VC != nil {
		// IMPORTANT: Setting ChannelID to empty BEFORE Disconnect
		// tells discordgo's internal loop: "We are intentionally leaving."
		// This prevents the library from triggering its automatic reconnect logic.
		vs.VC.Lock()
		vs.VC.ChannelID = ""
		vs.VC.Unlock()

		vs.VC.Disconnect()
	}

	// Delete the captions message
	if vs.TextMsgID != "" && vs.EmbedMsgID != "" {
		err := m.Session.ChannelMessageDelete(vs.TextMsgID, vs.EmbedMsgID)
		if err != nil {
			log.Printf("Warning: failed to delete caption message: %v", err)
		}
	}

	// 4. Cleanup map
	delete(m.Sessions, guildID)
	m.mu.Unlock()

	return nil
}

func (m *Manager) listenLoop(vs *VoiceSession) {
	// Map to track per-user audio buffers
	userAudio := make(map[uint32]*AudioBuffer)

	ticker := time.NewTicker(200 * time.Millisecond) // Checked more frequently for smoother processing
	defer ticker.Stop()
	defer m.Stop(vs.GuildID) // Ensure cleanup if loop exits

	for {
		select {
		case <-vs.Done:
			return
		case p, ok := <-vs.VC.OpusRecv:
			if !ok {
				return
			}

			buf, ok := userAudio[p.SSRC]
			if !ok {
				buf = NewAudioBuffer(p.SSRC) // Create new buffer for new user
				userAudio[p.SSRC] = buf
			}
			buf.Push(p) // Add packet to THAT user's buffer

		case <-ticker.C:
			for ssrc, buf := range userAudio {

				// 1. Check if the buffer WANTS to be processed
				// (Silence > 2s OR Duration > 30s)
				shouldProcess, isHardCutoff, isStale := buf.ShouldProcess()

				if shouldProcess {
					// If it's a hard cutoff OR the data is getting stale (waiting too long),
					// we force it through (the GroqClient mutex will handle the sleep).
					if isHardCutoff || isStale {
						go m.processChunk(vs, ssrc, buf.Pop(isHardCutoff))
					} else {
						// Otherwise, be polite and wait for a free slot
						if m.CanRequest() {
							go m.processChunk(vs, ssrc, buf.Pop(false))
						}
						// Else: Wait, let buffer merge with potential future speech.
						// It will naturally loop back here in 200ms and check CanRequest() again.
					}
				}

				// I DELETED BLOCK 2 ("CHECK RATE LIMIT") FROM HERE!
			}
		}
	}
}

func (m *Manager) processChunk(vs *VoiceSession, ssrc uint32, packets []*discordgo.Packet) {
	// Add this check!
	// 25 packets * 20ms = 500ms. Don't waste API calls on mic clicks.
	if len(packets) < 25 {
		return
	}

	vs.mu.Lock()
	userID := vs.SSRCtoUser[ssrc]
	lastText := vs.LastUserText[ssrc] // Fetch the previous text for this specific user
	vs.mu.Unlock()

	// Default fallback
	username := fmt.Sprintf("User %d", ssrc)

	if userID != "" {
		// 1. Try to get the user from the state (cache) to find their Server Nickname
		member, err := m.Session.State.Member(vs.GuildID, userID)
		if err == nil && member.Nick != "" {
			username = member.Nick
		} else if err == nil && member.User != nil {
			// 2. If no nickname, try Global Display Name or Username
			if member.User.GlobalName != "" {
				username = member.User.GlobalName
			} else {
				username = member.User.Username
			}
		} else {
			// 3. Fallback: If not in state, try a quick API fetch
			user, err := m.Session.User(userID)
			if err == nil {
				if user.GlobalName != "" {
					username = user.GlobalName
				} else {
					username = user.Username
				}
			}
		}
	}

	// --- VAD Filtering ---
	vad, err := tenvad.NewVad(960, 0.5) // 960 samples = 20ms at 48kHz. threshold = 0.5
	if err == nil {
		defer vad.Close()
		decoder, err := gopus.NewDecoder(48000, 1) // 1 channel downmix for VAD
		if err == nil {
			speechFrames := 0
			totalFrames := 0

			for _, p := range packets {
				pcm, decErr := decoder.Decode(p.Opus, 960, false)
				if decErr == nil {
					totalFrames++
					_, isSpeech, vadErr := vad.Process(pcm)
					if vadErr == nil && isSpeech {
						speechFrames++
					}
				}
			}

			// If less than 5% of the frames contain speech, drop the buffer
			if totalFrames > 0 && float64(speechFrames)/float64(totalFrames) < 0.05 {
				log.Printf("[VAD] Dropped buffer for %s: mostly silence/noise (%d/%d speech frames)", username, speechFrames, totalFrames)
				return
			}
		} else {
			log.Printf("[VAD] Failed to create opus decoder: %v", err)
		}
	} else {
		log.Printf("[VAD] Failed to init TEN-VAD: %v", err)
	}
	// ---------------------

	// 1. Write the raw Opus packets directly into an Ogg container in-memory
	var buf bytes.Buffer

	// Discord sends 48kHz, 2-channel audio
	ogg, err := oggwriter.NewWith(&buf, 48000, 2)
	if err != nil {
		log.Printf("Ogg writer error: %v", err)
		return
	}

	// FIXED: Normalize timestamps and sequences
	var fakeTimestamp uint32 = 0
	var fakeSequence uint16 = 0

	for _, p := range packets {
		// Reconstruct standard RTP packet for the oggwriter
		rtpPacket := &rtp.Packet{
			Header: rtp.Header{
				SequenceNumber: fakeSequence,
				Timestamp:      fakeTimestamp,
				SSRC:           p.SSRC,
			},
			Payload: p.Opus,
		}
		if err := ogg.WriteRTP(rtpPacket); err != nil {
			log.Printf("Failed to write RTP packet: %v", err)
		}
		fakeValues := 960 // 20ms at 48kHz
		fakeTimestamp += uint32(fakeValues)
		fakeSequence++
	}
	ogg.Close()

	oggData := buf.Bytes()

	// 2. Build the prompt using the user's previous transcription
	// We provide a base context, and append their last spoken text.
	var prompt string
	if lastText != "" {
		// Whisper has a 224 token limit for prompts. We safely trim to the last ~400 chars.
		if len(lastText) > 400 {
			lastText = lastText[len(lastText)-400:]
		}
		prompt = lastText
	}

	// 2.5: Logging for debugging "Request too large"
	durationEst := time.Duration(len(packets)*20) * time.Millisecond
	log.Printf("[DEBUG] Sending to Groq: User=%s, Packets=%d, EstDuration=%v, Bytes=%d",
		username, len(packets), durationEst, len(oggData))

	// 3. Send to Groq with the new prompt parameter
	text, debugStr, err := m.Groq.TranslateAudio(oggData, "audio.ogg", prompt)
	if err != nil {
		// Log the full error and the size of the problematic data
		log.Printf("Groq error: %v | Data Size: %d bytes | Packets: %d", err, len(oggData), len(packets))
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	// 4. Save this successful text as the prompt for the user's NEXT audio chunk
	vs.mu.Lock()
	vs.LastUserText[ssrc] = text
	vs.mu.Unlock()

	// Send to Discord channel
	m.addCaption(vs, username, text, debugStr)
}

func (m *Manager) addCaption(vs *VoiceSession, username, text string, debugStr string) {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	line := fmt.Sprintf("**%s**: %s", username, text)
	vs.UserLogs = append(vs.UserLogs, line)
	if len(vs.UserLogs) > 10 {
		vs.UserLogs = vs.UserLogs[len(vs.UserLogs)-10:]
	}

	content := strings.Join(vs.UserLogs, "\n")

	footerText := debugStr

	// Discord limits footer length to 2048 chars, truncate just in case
	if len(footerText) > 2048 {
		footerText = footerText[:2045] + "..."
	}

	embed := &discordgo.MessageEmbed{
		Title:       "Groq (Large-Whisper-v3)",
		Description: content,
		Color:       0x00ff00,
		Footer: &discordgo.MessageEmbedFooter{
			Text: footerText,
		},
	}

	_, err := m.Session.ChannelMessageEditEmbed(vs.TextMsgID, vs.EmbedMsgID, embed)
	if err != nil {
		log.Printf("Embed edit error: %v", err)
	}
}

// AudioBuffer helpers
type AudioBuffer struct {
	SSRC      uint32
	Packets   []*discordgo.Packet
	LastPush  time.Time
	FirstPush time.Time
	mu        sync.Mutex
}

func NewAudioBuffer(ssrc uint32) *AudioBuffer {
	return &AudioBuffer{SSRC: ssrc}
}

func (b *AudioBuffer) Push(p *discordgo.Packet) {
	b.mu.Lock()
	if b.FirstPush.IsZero() {
		b.FirstPush = time.Now()
	}
	b.Packets = append(b.Packets, p)
	b.LastPush = time.Now()
	b.mu.Unlock()
}

// ShouldProcess returns (shouldProcess bool, isHardCutoff bool)
// Returns: shouldProcess, isHardCutoff, isStale
func (b *AudioBuffer) ShouldProcess() (bool, bool, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.Packets) == 0 {
		return false, false, false
	}

	duration := time.Since(b.FirstPush)
	silence := time.Since(b.LastPush)

	// 1. Hard Cutoff: 30 seconds
	// If the buffer grows larger than 30s, force a process event.
	// Returns isHardCutoff = true to trigger the overlap retention.
	if duration > 30*time.Second {
		return true, true, false
	}

	// 2. Stale Data (> 6s silence)
	// If they haven't spoken in 6 seconds, we should just queue it up.
	// We don't want to wait forever for a "merge" that isn't coming.
	if silence > 6*time.Second {
		return true, false, true
	}

	// 3. Natural Silence (> 1s)
	// Ready to send, but willing to wait for a rate limit slot.
	if silence > 1*time.Second {
		return true, false, false
	}

	return false, false, false
}

func (b *AudioBuffer) Pop(isHardCutoff bool) []*discordgo.Packet {
	b.mu.Lock()
	defer b.mu.Unlock()

	p := b.Packets

	// Discord sends Opus packets @ 20ms
	// 100 packets * 20ms = 2,000ms (2 seconds)
	// 2 seconds is the standard overlap to prevent cut-off words
	overlapSize := 100

	if isHardCutoff && len(p) > overlapSize {
		b.Packets = append([]*discordgo.Packet(nil), p[len(p)-overlapSize:]...)
		// Reset FirstPush to 2 seconds ago
		b.FirstPush = time.Now().Add(-2 * time.Second)
	} else {
		// Standard behavior: clear the buffer completely on natural silence.
		b.Packets = nil
		b.FirstPush = time.Time{}
	}

	return p
}

// CanRequest checks if the rate limit allows a request.
func (m *Manager) CanRequest() bool {
	m.ReqMu.Lock()
	defer m.ReqMu.Unlock()

	now := time.Now()
	if now.Before(m.NextReqTime) {
		return false
	}

	// Use the constant defined at the top (RateLimitInterval)
	m.NextReqTime = now.Add(RateLimitInterval)
	return true
}
