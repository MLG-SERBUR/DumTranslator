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
	"github.com/user/dumtranslator/internal/translate"
)

type Manager struct {
	Session  *discordgo.Session
	Groq     *translate.GroqClient
	Sessions map[string]*VoiceSession // GuildID -> VoiceSession
	mu       sync.Mutex
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

	// Monitoring goroutine to enforce "NEVER reconnect"
	go func() {
		// Wait for initial connection or timeout
		timeout := time.After(10 * time.Second)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-vs.Done:
				return
			case <-timeout:
				if !vs.VC.Ready {
					log.Printf("Voice connection failed to become ready in time, stopping.")
					m.Stop(guildID)
					return
				}
			case <-ticker.C:
				if !vs.VC.Ready {
					log.Printf("Voice connection not ready, enforced STOP (no reconnect).")
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
		// Prevent discordgo's internal reconnect() loop from re-joining.
		// When the sleeping loop wakes up and tries to join "",
		// it will safely disconnect and terminate the goroutine.
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

	// NOTE: vs.VC.AddHandler(...) has been successfully removed from here
	// to prevent race conditions. It is handled in Start() instead.

	ticker := time.NewTicker(500 * time.Millisecond)
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
				// Receive both the process flag and the hard cutoff flag
				shouldProcess, isHardCutoff := buf.ShouldProcess()

				if shouldProcess {
					// Pass the hard cutoff flag into Pop
					go m.processChunk(vs, ssrc, buf.Pop(isHardCutoff))
				}
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

	// 1. Write the raw Opus packets directly into an Ogg container in-memory
	var buf bytes.Buffer

	// Discord sends 48kHz, 2-channel audio
	ogg, err := oggwriter.NewWith(&buf, 48000, 2)
	if err != nil {
		log.Printf("Ogg writer error: %v", err)
		return
	}

	for _, p := range packets {
		// Reconstruct standard RTP packet for the oggwriter
		rtpPacket := &rtp.Packet{
			Header: rtp.Header{
				SequenceNumber: p.Sequence,
				Timestamp:      p.Timestamp,
				SSRC:           p.SSRC,
			},
			Payload: p.Opus, // No decoding required!
		}
		if err := ogg.WriteRTP(rtpPacket); err != nil {
			log.Printf("Failed to write RTP packet: %v", err)
		}
	}

	// Close is REQUIRED to write the End of Stream (EOS) flags for Ogg!
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

	// 3. Send to Groq with the new prompt parameter
	text, debugStr, err := m.Groq.TranslateAudio(oggData, "audio.ogg", prompt)
	if err != nil {
		log.Printf("Groq error: %v", err)
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
func (b *AudioBuffer) ShouldProcess() (bool, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.Packets) == 0 {
		return false, false
	}

	duration := time.Since(b.FirstPush)
	silence := time.Since(b.LastPush)

	// 1. Hard Cutoff: 30 seconds
	// If the buffer grows larger than 30s, force a process event.
	// Returns isHardCutoff = true to trigger the overlap retention.
	if duration > 30*time.Second {
		return true, true
	}

	// 2. Natural Silence + 10s Minimum Duration
	// Wait until at least 10 seconds of time has passed since the first word,
	// AND there has been a 2-second natural pause in speech.
	if silence > 2*time.Second && duration >= 10*time.Second {
		return true, false
	}

	return false, false
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
