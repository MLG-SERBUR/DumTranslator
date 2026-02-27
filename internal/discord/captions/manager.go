package captions

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/pion/opus"
	"github.com/user/dumtranslator/internal/translate"
)

type Manager struct {
	Session  *discordgo.Session
	Groq     *translate.GroqClient
	Sessions map[string]*VoiceSession // GuildID -> VoiceSession
	mu       sync.Mutex
}

type VoiceSession struct {
	GuildID    string
	ChannelID  string
	VC         *discordgo.VoiceConnection
	UserLogs   []string // List of "User: Text"
	EmbedMsgID string
	TextMsgID  string // The channel where the commands are typed
	SSRCtoUser map[uint32]string
	Done       chan bool
	mu         sync.Mutex
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

	vc, err := m.Session.ChannelVoiceJoin(guildID, channelID, false, false)
	if err != nil {
		return err
	}

	vs := &VoiceSession{
		GuildID:    guildID,
		ChannelID:  channelID,
		VC:         vc,
		UserLogs:   []string{},
		SSRCtoUser: make(map[uint32]string),
		Done:       make(chan bool),
		TextMsgID:  tcID,
	}

	// Disable internal retries if possible, or handle disconnection
	vc.LogLevel = discordgo.LogDebug 
	// Note: discordgo's internal loops will still try to reconnect if not closed.
	// We'll watch for OpisRecv closure as a sign of permanent failure or Stop call.

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
					// Connection dropped, enforced STOP (no reconnect).
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
			Text: "Powered by Groq STT",
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

	// Double check done signal to avoid race
	select {
	case <-vs.Done:
		m.mu.Unlock()
		return nil
	default:
	}

	close(vs.Done)
	vs.VC.Close() // Use Close() to ensure it stops immediately
	delete(m.Sessions, guildID)
	m.mu.Unlock()

	return nil
}

func (m *Manager) listenLoop(vs *VoiceSession) {
	// Map to track per-user audio buffers
	userAudio := make(map[uint32]*AudioBuffer)

	// Handler for SSRC mapping
	vs.VC.AddHandler(func(vc *discordgo.VoiceConnection, vsUpdate *discordgo.VoiceSpeakingUpdate) {
		vs.mu.Lock()
		vs.SSRCtoUser[uint32(vsUpdate.SSRC)] = vsUpdate.UserID
		vs.mu.Unlock()
	})

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
				buf = NewAudioBuffer(p.SSRC)
				userAudio[p.SSRC] = buf
			}
			buf.Push(p.Opus)

		case <-ticker.C:
			for ssrc, buf := range userAudio {
				if buf.ShouldProcess() {
					go m.processChunk(vs, ssrc, buf.Pop())
				}
			}
		}
	}
}

func (m *Manager) processChunk(vs *VoiceSession, ssrc uint32, opusPackets [][]byte) {
	if len(opusPackets) == 0 {
		return
	}

	vs.mu.Lock()
	userID := vs.SSRCtoUser[ssrc]
	vs.mu.Unlock()

	username := fmt.Sprintf("User %d", ssrc)
	if userID != "" {
		user, _ := m.Session.User(userID)
		if user != nil {
			username = user.Username
		}
	}

	// 1. Decode Opus to Mono PCM
	pcm, err := DecodeOpusToPCM(opusPackets)
	if err != nil {
		log.Printf("Opus decode error: %v", err)
		return
	}

	// 2. Encode PCM to MP3 using ffmpeg
	mp3Data, err := EncodePCMToMP3(pcm)
	if err != nil {
		log.Printf("MP3 encode error: %v", err)
		return
	}

	// 3. Send to Groq
	text, err := m.Groq.TranslateAudio(mp3Data, "audio.mp3")
	if err != nil {
		log.Printf("Groq error: %v", err)
		return
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	m.addCaption(vs, username, text)
}

func (m *Manager) addCaption(vs *VoiceSession, username, text string) {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	line := fmt.Sprintf("**%s**: %s", username, text)
	vs.UserLogs = append(vs.UserLogs, line)
	if len(vs.UserLogs) > 20 {
		vs.UserLogs = vs.UserLogs[len(vs.UserLogs)-20:]
	}

	content := strings.Join(vs.UserLogs, "\n")
	
	embed := &discordgo.MessageEmbed{
		Title:       "Translated Captions",
		Description: content,
		Color:       0x00ff00,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Powered by Groq STT",
		},
	}

	_, err := m.Session.ChannelMessageEditEmbed(vs.TextMsgID, vs.EmbedMsgID, embed)
	if err != nil {
		log.Printf("Embed edit error: %v", err)
	}
}

// AudioBuffer helpers
type AudioBuffer struct {
	SSRC       uint32
	Packets    [][]byte
	LastPush   time.Time
	FirstPush  time.Time
	mu         sync.Mutex
}

func NewAudioBuffer(ssrc uint32) *AudioBuffer {
	return &AudioBuffer{SSRC: ssrc}
}

func (b *AudioBuffer) Push(p []byte) {
	b.mu.Lock()
	if b.FirstPush.IsZero() {
		b.FirstPush = time.Now()
	}
	b.Packets = append(b.Packets, p)
	b.LastPush = time.Now()
	b.mu.Unlock()
}

func (b *AudioBuffer) ShouldProcess() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.Packets) == 0 {
		return false
	}
	duration := time.Since(b.FirstPush)
	silence := time.Since(b.LastPush)
	
	// Min 10s audio + 800ms silence OR max 25s
	if duration > 10*time.Second && silence > 800*time.Millisecond {
		return true
	}
	if duration > 25*time.Second {
		return true
	}
	return false
}

func (b *AudioBuffer) Pop() [][]byte {
	b.mu.Lock()
	p := b.Packets
	b.Packets = nil
	b.FirstPush = time.Time{}
	b.mu.Unlock()
	return p
}

// Audio Processing Functions

func DecodeOpusToPCM(packets [][]byte) ([]byte, error) {
	decoder := captionsOpusDecoder()
	var allPcm []byte

	for _, p := range packets {
		pcm := make([]byte, 1920*2*2) // Max 60ms at 48k, 2 bytes/sample, 2 channels
		n, isStereo, err := decoder.Decode(p, pcm)
		if err != nil {
			continue // Skip bad packets
		}
		
		numSamples := int(n)
		if isStereo {
			// Downmix stereo to mono
			mono := make([]byte, numSamples*2)
			for i := 0; i < numSamples; i++ {
				left := int16(binary.LittleEndian.Uint16(pcm[i*4 : i*4+2]))
				right := int16(binary.LittleEndian.Uint16(pcm[i*4+2 : i*4+4]))
				// Average the channels
				avg := int16((int32(left) + int32(right)) / 2)
				binary.LittleEndian.PutUint16(mono[i*2:i*2+2], uint16(avg))
			}
			allPcm = append(allPcm, mono...)
		} else {
			// Already mono, just append the used portion
			allPcm = append(allPcm, pcm[:numSamples*2]...)
		}
	}
	return allPcm, nil
}

func captionsOpusDecoder() *opus.Decoder {
	d := opus.NewDecoder()
	return &d
}

func EncodePCMToMP3(pcm []byte) ([]byte, error) {
	// Use ffmpeg to encode Mono PCM (48kHz) to MP3
	cmd := exec.Command("ffmpeg", "-f", "s16le", "-ar", "48000", "-ac", "1", "-i", "pipe:0", "-f", "mp3", "pipe:1")
	cmd.Stdin = bytes.NewReader(pcm)
	
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
