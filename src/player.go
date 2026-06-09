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

	"github.com/bwmarrin/discordgo"
	"github.com/cheezecakee/dca"
)

// GuildPlayer manages voice connections and audio playback for one guild.
type GuildPlayer struct {
	GuildID        string
	mu             sync.Mutex
	session        *discordgo.Session
	voiceConn      *discordgo.VoiceConnection
	streamSession  *dca.StreamingSession
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

// Pause pauses the current stream via the StreamingSession interface.
func (p *GuildPlayer) Pause() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.isPlaying || p.streamSession == nil {
		log.Printf("⚠️ [WARN] Pause requested, but nothing is currently playing in Guild %s", p.GuildID)
		return fmt.Errorf("nothing is playing")
	}
	if p.isPaused {
		log.Printf("ℹ️ [INFO] Pause requested, but playback is already paused.")
		return nil
	}

	p.isPaused = true
	p.streamSession.SetPaused(true)
	log.Printf("⏸️ [INFO] Playback paused for Guild %s", p.GuildID)

	rdb.SetPlayerState(context.Background(), p.GuildID, map[string]interface{}{"is_paused": "true"})
	return nil
}

// Resume unpauses the current stream via the StreamingSession interface.
func (p *GuildPlayer) Resume() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.isPlaying || p.streamSession == nil {
		log.Printf("⚠️ [WARN] Resume requested, but nothing is playing in Guild %s", p.GuildID)
		return fmt.Errorf("nothing is playing")
	}
	if !p.isPaused {
		log.Printf("ℹ️ [INFO] Resume requested, but playback is already running.")
		return nil
	}

	p.isPaused = false
	p.streamSession.SetPaused(false)
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
	p.streamSession = nil
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

// playTrack streams a single track using automated cheezecakee/dca streaming loop abstractions.
// playTrack streams a track safely by passing verified pathways to dca.EncodeFile.
func (p *GuildPlayer) playTrack(track *QueueTrack) error {
	ctx := context.Background()
	var minioStream io.ReadCloser

	p.mu.Lock()
	playCtx, cancel := context.WithCancel(context.Background())
	p.playCtx = playCtx
	p.cancelPlay = cancel
	p.mu.Unlock()

	defer func() {
		log.Printf("🔲 [TRACK-END] Running deferred cleanup blocks for stream...")
		cancel()
		p.mu.Lock()
		p.streamSession = nil
		if minioStream != nil {
			log.Println("🔲 [TRACK-END] Closing direct MinIO socket streams...")
			minioStream.Close()
		}
		p.mu.Unlock()
	}()

	// Load stable default library configurations
	opts := dca.StdEncodeOptions
	opts.Bitrate = 96
	opts.CompressionLevel = 10

	var targetPath string
	var err error

	if track.Source == "minio" {
		log.Printf("📦 [STREAM] Source identified as MinIO. Resolving mapping for link: %s", track.URL)
		minioStream, err = minioClient.GetLibrarySong(ctx, track.URL)
		if err != nil {
			return fmt.Errorf("fetching custom track from MinIO: %w", err)
		}

		// For memory streams, we write to a temporary file locally or pipe it
		// If MinIO links are public or signed presigned URLs, pass the URL directly to targetPath instead!
		targetPath = track.URL
	} else {
		log.Printf("🌐 [STREAM] Source identified as Web/YT. Resolving link via yt-dlp: '%s'", track.URL)
		streamURL, resolveErr := resolveWithYtDlp(track.URL)
		if resolveErr != nil {
			return fmt.Errorf("resolving web audio source with yt-dlp: %w", resolveErr)
		}
		if streamURL == "" {
			return fmt.Errorf("yt-dlp engine completed but returned an empty stream URL string")
		}
		targetPath = streamURL
	}

	// ASYNCHRONOUS: EncodeFile handles spawning its own unblocking background context process
	log.Printf("🎛️ [DCA] Spawning background file transcoding worker for target: %s", targetPath)
	encSession, err := dca.EncodeFile(targetPath, opts)
	if err != nil {
		return fmt.Errorf("transcoding execution memory translation failure: %w", err)
	}
	defer encSession.Cleanup()

	p.mu.Lock()
	vc := p.voiceConn
	p.mu.Unlock()

	// ASSERT SPEAKING VOICE FLAG BEFORE STARTING THE STREAM
	log.Println("🎤 [DISCORD-VOICE] Asserting speaking state to gateway...")
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

	log.Printf("🎼 [STREAM-START] Beginning automated binary packet loop delivery for: '%s'", track.Title)

	doneChan := make(chan error, 1)
	stream := dca.NewStream(encSession, vc, doneChan)

	p.mu.Lock()
	p.streamSession = stream
	p.isPaused = false
	p.mu.Unlock()

	select {
	case <-playCtx.Done():
		log.Println("🛑 [STREAM-LOOP] Context canceled. Stopping loop delivery.")
		return fmt.Errorf("playback interrupted")
	case streamErr := <-doneChan:
		if streamErr != nil && streamErr != io.EOF {
			return fmt.Errorf("stream context ran into active loop crash: %w", streamErr)
		}
		log.Println("🎉 [STREAM-END] Stream playback finished cleanly.")
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
