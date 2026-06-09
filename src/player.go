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
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jonas747/dca"
)

// GuildPlayer manages voice connections and audio playback for one guild.
type GuildPlayer struct {
	GuildID        string
	mu             sync.Mutex
	session        *discordgo.Session
	voiceConn      *discordgo.VoiceConnection
	encodeSession  *dca.EncodeSession
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

// GetOrCreatePlayer retrieves or initialises the player for a guild.
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
	
	// Set manual engine flag (last parameter to false) to allow direct chunk delivery
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

// Pause pauses the current stream.
func (p *GuildPlayer) Pause() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.isPlaying {
		log.Printf("⚠️ [WARN] Pause requested, but nothing is currently playing in Guild %s", p.GuildID)
		return fmt.Errorf("nothing is playing")
	}
	if p.isPaused {
		log.Printf("ℹ️ [INFO] Pause requested, but playback is already paused.")
		return nil
	}
	
	p.isPaused = true
	log.Printf("⏸️ [INFO] Playback paused for Guild %s", p.GuildID)

	rdb.SetPlayerState(context.Background(), p.GuildID, map[string]interface{}{"is_paused": "true"})
	return nil
}

// Resume unpauses the current stream.
func (p *GuildPlayer) Resume() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.isPlaying {
		log.Printf("⚠️ [WARN] Resume requested, but nothing is playing in Guild %s", p.GuildID)
		return fmt.Errorf("nothing is playing")
	}
	if !p.isPaused {
		log.Printf("ℹ️ [INFO] Resume requested, but playback is already running.")
		return nil
	}
	
	p.isPaused = false
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

// stopPlaybackLocked cancels the active context and cleans up the encoder.
func (p *GuildPlayer) stopPlaybackLocked() {
	log.Printf("🧹 [INFO] Internal cleanup: Stopping encoders and destroying session contexts...")
	if p.cancelPlay != nil {
		p.cancelPlay()
		p.cancelPlay = nil
	}
	if p.encodeSession != nil {
		p.encodeSession.Cleanup()
		p.encodeSession = nil
	}
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

// playTrack streams a single track through Discord voice using a robust Unix Named Pipe.
func (p *GuildPlayer) playTrack(track *QueueTrack) error {
	ctx := context.Background()
	var minioStream io.ReadCloser

	p.mu.Lock()
	playCtx, cancel := context.WithCancel(context.Background())
	p.playCtx = playCtx
	p.cancelPlay = cancel
	p.mu.Unlock()

	// Unique named pipe path inside the Docker container's temporary directory
	fifoPath := fmt.Sprintf("/tmp/discord_audio_%s_%d.fifo", p.GuildID, time.Now().UnixNano())

	// Native Linux command to create a secure named pipe
	_ = exec.Command("mkfifo", fifoPath).Run()

	defer func() {
		log.Printf("🔲 [TRACK-END] Running deferred cleanup blocks for stream...")
		cancel()
		p.mu.Lock()
		if p.encodeSession != nil {
			log.Println("🔲 [TRACK-END] Destroying active DCA session frames...")
			p.encodeSession.Cleanup()
			p.encodeSession = nil
		}
		if minioStream != nil {
			log.Println("🔲 [TRACK-END] Closing direct MinIO socket streams...")
			minioStream.Close()
		}
		p.mu.Unlock()
		
		// Remove the temporary named pipe from the container storage filesystem
		_ = exec.Command("rm", "-f", fifoPath).Run()
	}()

	opts := dca.StdEncodeOptions
	opts.RawOutput = true
	opts.Bitrate = 96
	opts.Application = dca.AudioApplicationAudio

	var ffmpegCmd *exec.Cmd
	var err error

	if track.Source == "minio" {
		log.Printf("📦 [STREAM] Source identified as MinIO. Resolving mapping for link: %s", track.URL)
		minioStream, err = minioClient.GetLibrarySong(ctx, track.URL)
		if err != nil {
			return fmt.Errorf("fetching custom track from MinIO: %w", err)
		}
		
		// Directing output straight into our named pipe file
		ffmpegCmd = exec.Command("ffmpeg", "-y", "-i", "pipe:0", "-f", "s16le", "-ar", "48000", "-ac", "2", "-loglevel", "error", "-nostats", fifoPath)
		ffmpegCmd.Stdin = minioStream
	} else {
		log.Printf("🌐 [STREAM] Source identified as Web/YT. Resolving link via yt-dlp: '%s'", track.URL)
		streamURL, resolveErr := resolveWithYtDlp(track.URL)
		if resolveErr != nil {
			return fmt.Errorf("resolving web audio source with yt-dlp: %w", resolveErr)
		}
		
		if streamURL == "" {
			return fmt.Errorf("yt-dlp engine completed but returned an empty stream URL string")
		}

		log.Printf("🔗 [STREAM] yt-dlp link extraction resolved successfully. Length: %d chars", len(streamURL))
		
		// FFmpeg streams directly to the FIFO path instead of standard pipe output
		ffmpegCmd = exec.Command("ffmpeg", "-y",
			"-reconnect", "1", 
			"-reconnect_streamed", "1", 
			"-reconnect_delay_max", "5", 
			"-i", streamURL, 
			"-f", "s16le", 
			"-ar", "48000", 
			"-ac", "2", 
			"-loglevel", "error", 
			"-nostats", 
			fifoPath,
		)
	}

	var ffmpegStderr bytes.Buffer
	ffmpegCmd.Stderr = &ffmpegStderr

	log.Println("🎬 [FFMPEG] Initializing external FFmpeg engine process targeting FIFO...")
	if err := ffmpegCmd.Start(); err != nil {
		return fmt.Errorf("failed to execute background ffmpeg context instantiation: %w", err)
	}

	defer func() {
		if ffmpegCmd.Process != nil {
			log.Println("🎬 [FFMPEG] Terminating background FFmpeg worker sub-process.")
			_ = ffmpegCmd.Process.Kill()
		}
	}()

	// CRITICAL SYNCHRONIZATION STEP: Give FFmpeg a fractional moment to resolve 
	// HTTPS handshake routes and begin filling the FIFO pipe layout.
	time.Sleep(250 * time.Millisecond)

	// EncodeFile reads from the Unix FIFO. This lets DCA initialize cleanly without early EOF drops.
	log.Println("🎛️ [DCA] Compiling FIFO file target into DCA tracking structure...")
	encSession, err := dca.EncodeFile(fifoPath, opts)
	if err != nil {
		stderrStr := strings.TrimSpace(ffmpegStderr.String())
		log.Printf("❌ [FFMPEG-CRASH] Underlying process log error caught during EncodeFile: %s", stderrStr)
		return fmt.Errorf("transcoding execution memory translation failure: %w (stderr: %s)", err, stderrStr)
	}

	if ffmpegStderr.Len() > 0 {
		log.Printf("⚠️ [FFMPEG-WARN] Process generated error output during initialization: %s", strings.TrimSpace(ffmpegStderr.String()))
	}

	p.mu.Lock()
	p.encodeSession = encSession
	p.isPaused = false
	vc := p.voiceConn
	p.mu.Unlock()

	log.Println("🎤 [DISCORD-VOICE] Signaling active transmitting payload channel flag...")
	if err := vc.Speaking(true); err != nil {
		log.Printf("⚠️ [DISCORD-VOICE] Warning: Failed to assert voice gateway payload speaking token: %v", err)
	}
	defer func() {
		log.Println("🎤 [DISCORD-VOICE] Stripping active transmission gateway flag tokens.")
		_ = vc.Speaking(false)
	}()

	rdb.SetPlayerState(ctx, p.GuildID, map[string]interface{}{
		"current_song_title": fmt.Sprintf("**%s** - %s (Requested by: %s)", track.Title, track.Artist, track.RequestedBy),
		"is_paused":          "false",
	})

	log.Printf("🎼 [STREAM-START] Beginning binary packet loop delivery for: '%s'", track.Title)
	frameCount := 0

	for {
		select {
		case <-playCtx.Done():
			log.Println("🛑 [STREAM-LOOP] Context canceled. Stopping loop delivery.")
			return fmt.Errorf("playback interrupted")
		default:
		}

		p.mu.Lock()
		paused := p.isPaused
		p.mu.Unlock()

		if paused {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		frame, err := encSession.OpusFrame()
		if err != nil {
			if err == io.EOF {
				log.Printf("🎉 [STREAM-END] Reached natural EOF of audio stream. Sent total of %d packets.", frameCount)
				break 
			}
			return fmt.Errorf("error reading next frame slice from dca buffer stream: %w", err)
		}

		select {
		case vc.OpusSend <- frame:
			frameCount++
			if frameCount%500 == 0 {
				log.Printf("🔊 [STREAM-TRACE] Successfully routed %d packets into Opus Engine queue buffers...", frameCount)
			}
		case <-playCtx.Done():
			log.Println("🛑 [STREAM-LOOP] Context dropped during actively running frame transmission.")
			return fmt.Errorf("playback interrupted during packet stream pipeline delivery")
		}
	}

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