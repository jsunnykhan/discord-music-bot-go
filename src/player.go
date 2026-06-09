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
		return nil // already there
	}

	vc, err := p.session.ChannelVoiceJoin(context.Background(), p.GuildID, channelID, false, true)
	if err != nil {
		return fmt.Errorf("joining voice channel %s: %w", channelID, err)
	}
	p.voiceConn = vc
	p.voiceChannelID = channelID
	return nil
}

// Leave disconnects from voice and stops any active playback.
func (p *GuildPlayer) Leave() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.voiceConn == nil {
		return fmt.Errorf("not connected to any voice channel")
	}

	p.stopPlaybackLocked()
	err := p.voiceConn.Disconnect(context.Background())
	p.voiceConn = nil
	p.isPlaying = false
	p.isPaused = false
	p.voiceChannelID = ""
	return err
}

// Play enqueues a track and starts the playback loop if it's idle.
func (p *GuildPlayer) Play(track *QueueTrack, textChannelID string) error {
	p.mu.Lock()
	p.textChannelID = textChannelID
	p.mu.Unlock()

	ctx := context.Background()
	if err := rdb.EnqueueTrack(ctx, p.GuildID, track); err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.isPlaying {
		p.isPlaying = true
		go p.runPlaybackLoop()
	}
	return nil
}

// Pause pauses the current stream.
func (p *GuildPlayer) Pause() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.isPlaying {
		return fmt.Errorf("nothing is playing")
	}
	if p.isPaused {
		return nil
	}
	p.isPaused = true

	rdb.SetPlayerState(context.Background(), p.GuildID, map[string]interface{}{"is_paused": "true"})
	return nil
}

// Resume unpauses the current stream.
func (p *GuildPlayer) Resume() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.isPlaying {
		return fmt.Errorf("nothing is playing")
	}
	if !p.isPaused {
		return nil
	}
	p.isPaused = false

	rdb.SetPlayerState(context.Background(), p.GuildID, map[string]interface{}{"is_paused": "false"})
	return nil
}

// Stop terminates playback and clears the entire queue.
func (p *GuildPlayer) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	ctx := context.Background()
	if err := rdb.ClearQueue(ctx, p.GuildID); err != nil {
		log.Printf("Warning: clearing queue on stop: %v", err)
	}
	p.stopPlaybackLocked()
	rdb.ClearPlayerState(ctx, p.GuildID)
	return nil
}

// stopPlaybackLocked cancels the active context and cleans up the encoder.
// Must be called while holding p.mu.
func (p *GuildPlayer) stopPlaybackLocked() {
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
// func (p *GuildPlayer) runPlaybackLoop() {
// 	ctx := context.Background()

// 	for {
// 		track, err := rdb.DequeueTrack(ctx, p.GuildID)
// 		if err != nil {
// 			log.Printf("Dequeue error (guild %s): %v", p.GuildID, err)
// 			break
// 		}
// 		if track == nil {
// 			break // queue empty
// 		}

// 		p.mu.Lock()
// 		if p.voiceConn == nil {
// 			p.mu.Unlock()
// 			log.Printf("Playback loop ending (guild %s): no voice connection", p.GuildID)
// 			break
// 		}
// 		p.mu.Unlock()

// 		if err := p.playTrack(track); err != nil {
// 			log.Printf("Playback error on '%s': %v", track.Title, err)
// 			p.sendError(err.Error())
// 		}
// 	}

// 	p.mu.Lock()
// 	p.isPlaying = false
// 	p.stopPlaybackLocked()
// 	p.mu.Unlock()

// 	rdb.ClearPlayerState(ctx, p.GuildID)
// }

func (p *GuildPlayer) runPlaybackLoop() {
	ctx := context.Background()
	log.Println("🔄 [DEBUG] runPlaybackLoop has successfully started!")

	for {
		log.Println("🔄 [DEBUG] Attempting to fetch next track from Redis...")
		track, err := rdb.DequeueTrack(ctx, p.GuildID)
		if err != nil {
			log.Printf("❌ [DEBUG] Dequeue error (guild %s): %v", p.GuildID, err)
			break
		}
		if track == nil {
			log.Println("ℹ️ [DEBUG] Redis queue is empty. Ending loop.")
			break
		}

		log.Printf("🎵 [DEBUG] Found track in queue: %s (URL: %s)", track.Title, track.URL)

		p.mu.Lock()
		if p.voiceConn == nil {
			p.mu.Unlock()
			log.Printf("❌ [DEBUG] Voice connection is NIL for guild %s. Exiting loop.", p.GuildID)
			break
		}
		p.mu.Unlock()

		log.Println("🚀 [DEBUG] Handing track off to playTrack()...")
		if err := p.playTrack(track); err != nil {
			log.Printf("❌ [DEBUG] Playback error on '%s': %v", track.Title, err)
			p.sendError(err.Error())
		}
		log.Println("🏁 [DEBUG] finished processing playTrack() execution loop iteration.")
	}

	p.mu.Lock()
	p.isPlaying = false
	p.stopPlaybackLocked()
	p.mu.Unlock()
	log.Println("🛑 [DEBUG] runPlaybackLoop has shut down.")
	rdb.ClearPlayerState(ctx, p.GuildID)
}

// playTrack streams a single track through Discord voice.
func (p *GuildPlayer) playTrack(track *QueueTrack) error {
	ctx := context.Background()
	var minioStream io.ReadCloser

	p.mu.Lock()
	playCtx, cancel := context.WithCancel(context.Background())
	p.playCtx = playCtx
	p.cancelPlay = cancel
	p.mu.Unlock()

	defer func() {
		cancel()
		p.mu.Lock()
		if p.encodeSession != nil {
			p.encodeSession.Cleanup()
			p.encodeSession = nil
		}
		if minioStream != nil {
			minioStream.Close()
		}
		p.mu.Unlock()
	}()

	opts := dca.StdEncodeOptions
	opts.RawOutput = true
	opts.Bitrate = 96
	opts.Application = dca.AudioApplicationAudio

	var encSession *dca.EncodeSession
	var err error

	if track.Source == "minio" {
		minioStream, err = minioClient.GetLibrarySong(ctx, track.URL)
		if err != nil {
			return fmt.Errorf("fetching custom track: %w", err)
		}
		encSession, err = dca.EncodeMem(minioStream, opts)
	} else {
		streamURL, resolveErr := resolveWithYtDlp(track.URL)
		if resolveErr != nil {
			return fmt.Errorf("resolving audio source: %w", resolveErr)
		}
		encSession, err = dca.EncodeFile(streamURL, opts)
	}
	if err != nil {
		return fmt.Errorf("transcoding: %w", err)
	}

	p.mu.Lock()
	p.encodeSession = encSession
	p.isPaused = false
	vc := p.voiceConn
	p.mu.Unlock()

	// Direct native speaking trigger for modern discordgo engines
	if err := vc.Speaking(true); err != nil {
		log.Printf("Warning: failed to establish speaking state: %v", err)
	}
	defer func() {
		_ = vc.Speaking(false)
	}()

	// Publish "now playing" state in Redis.
	rdb.SetPlayerState(ctx, p.GuildID, map[string]interface{}{
		"current_song_title": fmt.Sprintf("**%s** - %s (Requested by: %s)", track.Title, track.Artist, track.RequestedBy),
		"is_paused":          "false",
	})

	// Frame-copy loop directly to the Opus Engine channel
	for {
		select {
		case <-playCtx.Done():
			return fmt.Errorf("playback interrupted")
		default:
		}

		p.mu.Lock()
		paused := p.isPaused
		p.mu.Unlock()

		if paused {
			// Small skip block to avoid thread-locking CPU when paused
			continue
		}

		frame, err := encSession.OpusFrame()
		if err != nil {
			if err == io.EOF {
				break // End of track
			}
			return fmt.Errorf("reading opus frame: %w", err)
		}

		select {
		case vc.OpusSend <- frame:
		case <-playCtx.Done():
			return fmt.Errorf("playback interrupted during packet stream")
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

	cmd := exec.Command("yt-dlp", "-f", "bestaudio", "-g", target)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("yt-dlp: %v (%s)", err, strings.TrimSpace(stderr.String()))
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return "", fmt.Errorf("yt-dlp returned no stream URL")
	}
	return lines[0], nil
}
