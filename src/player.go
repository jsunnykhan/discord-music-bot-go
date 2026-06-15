package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

// GuildPlayer manages voice connections and audio playback for one guild.
type GuildPlayer struct {
	GuildID        string
	mu             sync.Mutex
	session        *discordgo.Session
	voiceConn      *discordgo.VoiceConnection
	ffmpegCmd      *exec.Cmd
	isPlaying      bool
	isPaused       bool
	voiceChannelID string
	textChannelID  string
	playCtx        context.Context
	cancelPlay     context.CancelFunc
}

var (
	players   = make(map[string]*GuildPlayer)
	playersMu sync.RWMutex
)

// GetOrCreatePlayer retrieves or initializes the player for a guild.
func GetOrCreatePlayer(guildID string, s *discordgo.Session) *GuildPlayer {
	playersMu.Lock()
	defer playersMu.Unlock()

	p, ok := players[guildID]
	if !ok {
		log.Printf("⚙️ [INFO] Creating new GuildPlayer instance for Guild ID: %s", guildID)
		p = &GuildPlayer{GuildID: guildID, session: s}
		players[guildID] = p
	}
	return p
}

// Join connects the bot to the given voice channel.
func (p *GuildPlayer) Join(channelID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.voiceConn != nil && p.voiceChannelID == channelID {
		log.Printf("ℹ️ [INFO] Already connected to requested voice channel %s in Guild %s", channelID, p.GuildID)
		return nil
	}

	log.Printf("🔌 [INFO] Attempting to join voice channel %s (Guild: %s)...", channelID, p.GuildID)

	vc, err := p.session.ChannelVoiceJoin(context.Background(), p.GuildID, channelID, false, false)
	if err != nil {
		log.Printf("❌ [ERROR] Failed to join voice channel %s: %v", channelID, err)
		return fmt.Errorf("joining voice channel %s: %w", channelID, err)
	}

	p.voiceConn = vc
	p.voiceChannelID = channelID
	log.Printf("✅ [SUCCESS] Successfully connected to voice channel %s", channelID)
	return nil
}

// Leave disconnects from voice and stops any active playback.
func (p *GuildPlayer) Leave() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.voiceConn == nil {
		log.Printf("⚠️ [WARN] Leave requested, but bot is not in any voice channel for Guild %s", p.GuildID)
		return fmt.Errorf("not connected to any voice channel")
	}

	log.Printf("🚪 [INFO] Leaving voice channel %s (Guild: %s)", p.voiceChannelID, p.GuildID)
	p.stopPlaybackLocked()

	err := p.voiceConn.Disconnect(context.Background())
	if err != nil {
		log.Printf("❌ [ERROR] Error occurred during voice disconnect: %v", err)
	}

	p.voiceConn = nil
	p.isPlaying = false
	p.isPaused = false
	p.voiceChannelID = ""
	log.Printf("✅ [SUCCESS] Voice cleanup complete for Guild %s", p.GuildID)
	return err
}

// Play enqueues a track and starts the playback loop if it's idle.
func (p *GuildPlayer) Play(track *QueueTrack, textChannelID string) error {
	p.mu.Lock()
	p.textChannelID = textChannelID
	p.mu.Unlock()

	log.Printf("📥 [INFO] Enqueueing track '%s' into Redis for Guild %s", track.Title, p.GuildID)
	ctx := context.Background()
	if err := rdb.EnqueueTrack(ctx, p.GuildID, track); err != nil {
		log.Printf("❌ [ERROR] Redis enqueue failed: %v", err)
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.isPlaying {
		log.Printf("🚀 [INFO] Playback loop is idle. Spawning runPlaybackLoop() goroutine...")
		p.isPlaying = true
		go p.runPlaybackLoop()
	} else {
		log.Printf("📝 [INFO] Track added to active queue. Playback loop is already running.")
	}
	return nil
}

// Pause pauses the current ffmpeg process.
func (p *GuildPlayer) Pause() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.isPlaying || p.ffmpegCmd == nil || p.ffmpegCmd.Process == nil {
		log.Printf("⚠️ [WARN] Pause requested, but nothing is currently playing in Guild %s", p.GuildID)
		return fmt.Errorf("nothing is playing")
	}
	if p.isPaused {
		log.Printf("ℹ️ [INFO] Pause requested, but playback is already paused.")
		return nil
	}

	p.isPaused = true
	if err := p.ffmpegCmd.Process.Signal(syscall.SIGSTOP); err != nil {
		log.Printf("⚠️ [WARN] Failed to pause ffmpeg process: %v", err)
	}
	log.Printf("⏸️ [INFO] Playback paused for Guild %s", p.GuildID)

	rdb.SetPlayerState(context.Background(), p.GuildID, map[string]interface{}{"is_paused": "true"})
	return nil
}

// Resume unpauses the current ffmpeg process.
func (p *GuildPlayer) Resume() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.isPlaying || p.ffmpegCmd == nil || p.ffmpegCmd.Process == nil {
		log.Printf("⚠️ [WARN] Resume requested, but nothing is playing in Guild %s", p.GuildID)
		return fmt.Errorf("nothing is playing")
	}
	if !p.isPaused {
		log.Printf("ℹ️ [INFO] Resume requested, but playback is already running.")
		return nil
	}

	p.isPaused = false
	if err := p.ffmpegCmd.Process.Signal(syscall.SIGCONT); err != nil {
		log.Printf("⚠️ [WARN] Failed to resume ffmpeg process: %v", err)
	}
	log.Printf("▶️ [INFO] Playback resumed for Guild %s", p.GuildID)

	rdb.SetPlayerState(context.Background(), p.GuildID, map[string]interface{}{"is_paused": "false"})
	return nil
}

// Stop terminates playback and clears the entire queue.
func (p *GuildPlayer) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	log.Printf("⏹️ [INFO] Stopping playback and purging Redis queue for Guild %s", p.GuildID)
	ctx := context.Background()
	if err := rdb.ClearQueue(ctx, p.GuildID); err != nil {
		log.Printf("⚠️ [WARN] Error clearing Redis queue on stop: %v", err)
	}

	p.stopPlaybackLocked()
	rdb.ClearPlayerState(ctx, p.GuildID)
	log.Printf("✅ [SUCCESS] Active streams closed and state cleared for Guild %s", p.GuildID)
	return nil
}

// stopPlaybackLocked cancels the active context and cleans up references.
func (p *GuildPlayer) stopPlaybackLocked() {
	log.Printf("🧹 [INFO] Internal cleanup: Stopping encoders and destroying session contexts...")
	if p.cancelPlay != nil {
		p.cancelPlay()
		p.cancelPlay = nil
	}
	if p.ffmpegCmd != nil && p.ffmpegCmd.Process != nil {
		_ = p.ffmpegCmd.Process.Kill()
	}
	p.ffmpegCmd = nil
	p.isPaused = false
}

// runPlaybackLoop dequeues and plays tracks sequentially until the queue is empty.
func (p *GuildPlayer) runPlaybackLoop() {
	ctx := context.Background()
	log.Printf("🔄 [DEBUG-LOOP] runPlaybackLoop has successfully initialized for Guild %s", p.GuildID)

	for {
		log.Println("🔄 [DEBUG-LOOP] Querying Redis for next available track...")
		track, err := rdb.DequeueTrack(ctx, p.GuildID)
		if err != nil {
			log.Printf("❌ [DEBUG-LOOP] Critical Dequeue Error: %v", err)
			break
		}
		if track == nil {
			log.Println("ℹ️ [DEBUG-LOOP] Redis queue returned empty. Shutting down loop.")
			break
		}

		log.Printf("🎵 [DEBUG-LOOP] Track extracted successfully: '%s' (Source: %s)", track.Title, track.Source)

		p.mu.Lock()
		if p.voiceConn == nil {
			p.mu.Unlock()
			log.Printf("❌ [DEBUG-LOOP] Voice Connection is NIL/Dropped during active loop evaluation. Aborting loop.")
			break
		}
		p.mu.Unlock()

		log.Printf("🚀 [DEBUG-LOOP] Transporting track parameters to playTrack()...")
		if err := p.playTrack(track); err != nil {
			log.Printf("❌ [DEBUG-LOOP] Caught active playback crash on track '%s': %v", track.Title, err)
			p.sendError(err.Error())
		}
		log.Printf("🏁 [DEBUG-LOOP] Execution cycle completed for track: '%s'", track.Title)
	}

	p.mu.Lock()
	p.isPlaying = false
	p.stopPlaybackLocked()
	p.mu.Unlock()

	log.Printf("🛑 [DEBUG-LOOP] runPlaybackLoop context thread terminated for Guild %s.", p.GuildID)
	rdb.ClearPlayerState(ctx, p.GuildID)
}

func (p *GuildPlayer) playTrack(track *QueueTrack) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.playCtx = ctx
	p.cancelPlay = cancel
	p.mu.Unlock()

	defer func() {
		cancel()
		p.mu.Lock()
		p.ffmpegCmd = nil
		p.mu.Unlock()
	}()

	// 1. Resolve URL via yt-dlp
	streamURL, err := resolveWithYtDlp(track.URL)
	if err != nil {
		return fmt.Errorf("yt-dlp failed: %w", err)
	}

	// 2. Setup voice connection
	p.mu.Lock()
	vc := p.voiceConn
	p.mu.Unlock()

	if vc == nil {
		return fmt.Errorf("voice connection is nil")
	}

	_ = vc.Speaking(true)
	defer func() { _ = vc.Speaking(false) }()

	// 3. Run ffmpeg as a subprocess
	log.Printf("🎵 [FFMPEG] Starting ffmpeg subprocess for: %s", streamURL)
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner",
		"-loglevel", "warning",
		"-i", streamURL,
		"-f", "opus",
		"-c:a", "libopus",
		"-ar", "48000",
		"-ac", "2",
		"-b:a", "128k",
		"-application", "audio",
		"-frame_duration", "20",
		"-vbr", "on",
		"pipe:1",
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	p.mu.Lock()
	p.ffmpegCmd = cmd
	p.mu.Unlock()

	// Ensure ffmpeg is killed when done
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		errStr := strings.TrimSpace(stderr.String())
		if errStr != "" {
			log.Printf("⚠️ [FFMPEG] stderr: %s", errStr)
		}
	}()

	// 4. Parse OGG pages manually to extract Opus frames
	log.Printf("🔍 [OGG] Starting OGG page parsing...")
	frameCount := 0
	headerPackets := 0
	var packetBuf []byte
	lastLogTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			log.Println("🛑 [OGG] Context cancelled during page parsing")
			return fmt.Errorf("playback interrupted")
		default:
		}

		// Read OGG page header (27 bytes minimum)
		var header [27]byte
		_, err := io.ReadFull(stdout, header[:])
		if err != nil {
			if err == io.EOF {
				log.Printf("✅ [OGG] End of stream after %d frames", frameCount)
				break
			}
			log.Printf("❌ [OGG] Error reading page header: %v", err)
			return fmt.Errorf("reading OGG header: %w", err)
		}

		// Verify capture pattern "OggS"
		if string(header[0:4]) != "OggS" {
			log.Printf("❌ [OGG] Invalid capture pattern: %q", string(header[0:4]))
			return fmt.Errorf("invalid OGG capture pattern")
		}

		// Parse header fields
		headerType := header[5]
		nsegs := header[26]

		// Read segment table
		segTable := make([]byte, nsegs)
		_, err = io.ReadFull(stdout, segTable)
		if err != nil {
			log.Printf("❌ [OGG] Error reading segment table: %v", err)
			return fmt.Errorf("reading segment table: %w", err)
		}

		// Calculate total page data size
		pageDataSize := 0
		for _, seg := range segTable {
			pageDataSize += int(seg)
		}

		// Read page data
		pageData := make([]byte, pageDataSize)
		_, err = io.ReadFull(stdout, pageData)
		if err != nil {
			log.Printf("❌ [OGG] Error reading page data: %v", err)
			return fmt.Errorf("reading page data: %w", err)
		}

		// Extract packets using OGG segment lacing:
		// Segment size 255 = packet continues in next segment
		// Segment size < 255 = end of packet
		dataPos := 0
		for segIdx := 0; segIdx < int(nsegs); segIdx++ {
			segSize := int(segTable[segIdx])
			if dataPos+segSize > len(pageData) {
				break
			}

			packetBuf = append(packetBuf, pageData[dataPos:dataPos+segSize]...)
			dataPos += segSize

			// If segment < 255, this terminates the current packet
			if segSize < 255 {
				packet := make([]byte, len(packetBuf))
				copy(packet, packetBuf)
				packetBuf = packetBuf[:0]

				// Skip first 2 header packets (OpusHead and OpusTags)
				if headerPackets < 2 {
					headerPackets++
					log.Printf("📋 [OGG] Skipping header packet %d (size: %d bytes)", headerPackets, len(packet))
					continue
				}

				// Send complete opus frame to Discord
				frameCount++
				if frameCount <= 5 || frameCount%100 == 0 {
					log.Printf("🎵 [OGG] Sending frame #%d (size: %d bytes)", frameCount, len(packet))
				}

				// Log frame timing every second
				if time.Since(lastLogTime) >= time.Second {
					log.Printf("📊 [OGG] Sent %d frames so far", frameCount)
					lastLogTime = time.Now()
				}

				select {
				case vc.OpusSend <- packet:
				case <-time.After(2 * time.Second):
					log.Printf("⚠️ [OGG] Timeout sending frame #%d to OpusSend", frameCount)
					return fmt.Errorf("timeout sending frame to OpusSend")
				case <-ctx.Done():
					return fmt.Errorf("playback interrupted")
				}
			}
		}

		// Check for end of stream
		if headerType&0x04 != 0 {
			log.Printf("✅ [OGG] End of stream flag detected after %d frames", frameCount)
			break
		}
	}

	log.Printf("✅ [OGG] Finished streaming %d opus frames to Discord", frameCount)
	return nil
}

// sendError posts a red embed to the text channel.
func (p *GuildPlayer) sendError(msg string) {
	p.mu.Lock()
	ch := p.textChannelID
	p.mu.Unlock()
	if ch == "" {
		return
	}
	log.Printf("📬 [TEXT-CHANNEL] Dispatching Error message context payload into Text Channel: %s", ch)
	p.session.ChannelMessageSendEmbed(ch, &discordgo.MessageEmbed{
		Title:       "Playback Error",
		Description: fmt.Sprintf("⚠️ %s", msg),
		Color:       0xf04747,
	})
}

// resolveWithYtDlp invokes yt-dlp to extract the best audio stream URL.
func resolveWithYtDlp(target string) (string, error) {
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = "ytsearch:" + target
	}

	log.Printf("🔍 [YT-DLP] Executing native binary call for: %s", target)

	cmd := exec.Command("/usr/local/bin/yt-dlp", "-f", "bestaudio", "-g", target)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("yt-dlp binary processing error flag: %v (details: %s)", err, strings.TrimSpace(stderr.String()))
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return "", fmt.Errorf("yt-dlp completed successfully but raw engine returned zero lines of output")
	}
	return lines[0], nil
}
