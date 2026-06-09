package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

func strPtr(s string) *string {
	return &s
}

// SlashCommands definitions
var commands = []*discordgo.ApplicationCommand{
	{
		Name:        "join",
		Description: "Connect the bot to your current voice channel",
	},
	{
		Name:        "leave",
		Description: "Disconnect the bot from the voice channel",
	},
	{
		Name:        "play",
		Description: "Play a song from YouTube (search/URL), Spotify link, or MinIO custom library",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "query",
				Description: "Song name, YouTube URL, Spotify track link, or custom song ID",
				Required:    true,
			},
		},
	},
	{
		Name:        "pause",
		Description: "Pause current audio playback",
	},
	{
		Name:        "resume",
		Description: "Resume paused audio playback",
	},
	{
		Name:        "stop",
		Description: "Stop playback, clear the queue, and disconnect",
	},
	{
		Name:        "queue",
		Description: "Show the current music queue list",
	},
	{
		Name:        "upload-song",
		Description: "Upload a custom audio file (.mp3) for admin approval",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionAttachment,
				Name:        "file",
				Description: "The MP3 file to upload",
				Required:    true,
			},
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "title",
				Description: "Title of the song",
				Required:    true,
			},
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "artist",
				Description: "Artist of the song",
				Required:    true,
			},
		},
	},
	{
		Name:        "approve",
		Description: "Admin: Approve a pending song upload (ID)",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionInteger,
				Name:        "id",
				Description: "Database ID of the uploaded song",
				Required:    true,
			},
		},
	},
	{
		Name:        "reject",
		Description: "Admin: Reject a pending song upload (ID)",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionInteger,
				Name:        "id",
				Description: "Database ID of the uploaded song",
				Required:    true,
			},
		},
	},
}

// Handler mapping for Slash Commands
var commandHandlers = map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
	"join":        handleJoin,
	"leave":       handleLeave,
	"play":        handlePlay,
	"pause":       handlePause,
	"resume":      handleResume,
	"stop":        handleStop,
	"queue":       handleQueue,
	"upload-song": handleUploadSong,
	"approve":     handleApprove,
	"reject":      handleReject,
}

// Interaction response utility
func respondWithMessage(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
		},
	})
}

// Interaction response utility with embeds
func respondWithEmbed(s *discordgo.Session, i *discordgo.InteractionCreate, embed *discordgo.MessageEmbed) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
		},
	})
}

func handleJoin(s *discordgo.Session, i *discordgo.InteractionCreate) {
	vs, err := findUserVoiceState(s, i.GuildID, i.Member.User.ID)
	if err != nil {
		respondWithMessage(s, i, "❌ You must be in a voice channel to use this command!")
		return
	}

	player := GetOrCreatePlayer(i.GuildID, s)
	if err := player.Join(vs.ChannelID); err != nil {
		respondWithMessage(s, i, fmt.Sprintf("❌ Error joining voice channel: %v", err))
		return
	}

	respondWithMessage(s, i, fmt.Sprintf("🔊 Joined <#%s>!", vs.ChannelID))
}

func handleLeave(s *discordgo.Session, i *discordgo.InteractionCreate) {
	player := GetOrCreatePlayer(i.GuildID, s)
	if err := player.Leave(); err != nil {
		respondWithMessage(s, i, fmt.Sprintf("❌ Error leaving voice channel: %v", err))
		return
	}
	respondWithMessage(s, i, "👋 Disconnected from voice channel.")
}

func handlePlay(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Acknowledge interaction first since resolution might take > 3 seconds (yt-dlp call)
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	query := i.ApplicationCommandData().Options[0].StringValue()
	track, err := resolveTrack(query, i.Member.User.Username)
	if err != nil {
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: strPtr(fmt.Sprintf("❌ Failed to resolve song: %v", err)),
		})
		return
	}

	// Join voice if not joined
	vs, err := findUserVoiceState(s, i.GuildID, i.Member.User.ID)
	if err != nil {
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: strPtr("❌ You must be in a voice channel first!"),
		})
		return
	}

	player := GetOrCreatePlayer(i.GuildID, s)
	if err := player.Join(vs.ChannelID); err != nil {
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: strPtr(fmt.Sprintf("❌ Error joining voice: %v", err)),
		})
		return
	}

	if err := player.Play(track, i.ChannelID); err != nil {
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: strPtr(fmt.Sprintf("❌ Error playing song: %v", err)),
		})
		return
	}

	embed := &discordgo.MessageEmbed{
		Title:       "Track Queued",
		Description: fmt.Sprintf("➕ **%s** - %s\nRequested by: **%s**", track.Title, track.Artist, track.RequestedBy),
		Color:       0x43b581,
	}
	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Embeds: &[]*discordgo.MessageEmbed{embed},
	})
}

func handlePause(s *discordgo.Session, i *discordgo.InteractionCreate) {
	player := GetOrCreatePlayer(i.GuildID, s)
	if err := player.Pause(); err != nil {
		respondWithMessage(s, i, fmt.Sprintf("❌ Failed to pause: %v", err))
		return
	}
	respondWithMessage(s, i, "⏸️ Playback paused.")
}

func handleResume(s *discordgo.Session, i *discordgo.InteractionCreate) {
	player := GetOrCreatePlayer(i.GuildID, s)
	if err := player.Resume(); err != nil {
		respondWithMessage(s, i, fmt.Sprintf("❌ Failed to resume: %v", err))
		return
	}
	respondWithMessage(s, i, "▶️ Playback resumed.")
}

func handleStop(s *discordgo.Session, i *discordgo.InteractionCreate) {
	player := GetOrCreatePlayer(i.GuildID, s)
	if err := player.Stop(); err != nil {
		respondWithMessage(s, i, fmt.Sprintf("❌ Failed to stop: %v", err))
		return
	}
	respondWithMessage(s, i, "🛑 Playback stopped and queue cleared.")
}

func handleQueue(s *discordgo.Session, i *discordgo.InteractionCreate) {
	ctx := context.Background()
	tracks, err := rdb.GetQueue(ctx, i.GuildID)
	if err != nil {
		respondWithMessage(s, i, fmt.Sprintf("❌ Failed to retrieve queue: %v", err))
		return
	}

	// Fetch current song title from Redis State
	state, _ := rdb.GetPlayerState(ctx, i.GuildID)
	currentSongTitle := state["current_song_title"]
	isPaused := state["is_paused"] == "true"

	var sb strings.Builder
	if currentSongTitle != "" {
		statusSymbol := "▶️"
		if isPaused {
			statusSymbol = "⏸️"
		}
		sb.WriteString(fmt.Sprintf("**Now Playing:**\n%s %s\n\n", statusSymbol, currentSongTitle))
	} else {
		sb.WriteString("**Now Playing:** None\n\n")
	}

	sb.WriteString("**Queue:**\n")
	if len(tracks) == 0 {
		sb.WriteString("No tracks in queue. Add some with `/play`!")
	} else {
		for index, t := range tracks {
			if index >= 10 {
				sb.WriteString(fmt.Sprintf("\n...and %d more", len(tracks)-10))
				break
			}
			sb.WriteString(fmt.Sprintf("%d. **%s** - %s (Requested by: %s)\n", index+1, t.Title, t.Artist, t.RequestedBy))
		}
	}

	embed := &discordgo.MessageEmbed{
		Title:       "Music Queue",
		Description: sb.String(),
		Color:       0x7289da,
		Timestamp:   time.Now().Format(time.RFC3339),
	}
	respondWithEmbed(s, i, embed)
}

func handleUploadSong(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Acknowledge interaction as downloading and uploading files takes time
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	options := i.ApplicationCommandData().Options
	attID := options[0].Value.(string)
	title := options[1].StringValue()
	artist := options[2].StringValue()

	// Find the attachment object
	var att *discordgo.MessageAttachment
	resolved := i.ApplicationCommandData().Resolved
	if resolved != nil && resolved.Attachments != nil {
		att = resolved.Attachments[attID]
	}

	if att == nil {
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: strPtr("❌ Failed to locate the uploaded file attachment."),
		})
		return
	}

	// Validate file extension is MP3
	ext := strings.ToLower(filepath.Ext(att.Filename))
	if ext != ".mp3" {
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: strPtr("❌ Only MP3 files are supported for custom uploads."),
		})
		return
	}

	// Fetch file stream from Discord CDN
	resp, err := http.Get(att.URL)
	if err != nil {
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: strPtr(fmt.Sprintf("❌ Failed to download attachment from Discord: %v", err)),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: strPtr(fmt.Sprintf("❌ Discord CDN returned non-OK status: %s", resp.Status)),
		})
		return
	}

	ctx := context.Background()
	filename := fmt.Sprintf("%d_%s", time.Now().UnixNano(), att.Filename)

	// Save upload details in Postgres
	uploadID, err := db.InsertUpload(title, artist, filename, i.Member.User.ID)
	if err != nil {
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: strPtr(fmt.Sprintf("❌ Database logging failed: %v", err)),
		})
		return
	}

	// Upload to MinIO pending prefix
	err = minioClient.UploadPending(ctx, filename, resp.Body, int64(att.Size))
	if err != nil {
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: strPtr(fmt.Sprintf("❌ MinIO storage upload failed: %v", err)),
		})
		// Cleanup DB entry on storage failure
		db.UpdateUploadStatus(uploadID, "rejected")
		return
	}

	// Send Admin Email notification
	go func() {
		err := SendAdminUploadNotification(cfg, uploadID, title, artist, i.Member.User.Username)
		if err != nil {
			log.Printf("Warning: failed to send admin email notification for upload ID %d: %v", uploadID, err)
		}
	}()

	embed := &discordgo.MessageEmbed{
		Title:       "Upload Successful",
		Description: fmt.Sprintf("📥 **%s** - %s\nUploaded successfully! Admin notification sent for review.\n\n**Upload ID:** `%d`", title, artist, uploadID),
		Color:       0x7289da,
	}
	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Embeds: &[]*discordgo.MessageEmbed{embed},
	})
}

func handleApprove(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Check administrator permissions
	if i.Member.Permissions&discordgo.PermissionAdministrator == 0 {
		respondWithMessage(s, i, "❌ Only server administrators can approve custom song uploads.")
		return
	}

	uploadID := int(i.ApplicationCommandData().Options[0].IntValue())
	ctx := context.Background()

	upload, err := db.GetUpload(uploadID)
	if err != nil {
		respondWithMessage(s, i, fmt.Sprintf("❌ Song upload record not found: %v", err))
		return
	}

	if upload.Status != "pending" {
		respondWithMessage(s, i, fmt.Sprintf("❌ This song upload is already in state: `%s`", upload.Status))
		return
	}

	// Move file in MinIO from pending/ to library/
	err = minioClient.ApproveSong(ctx, upload.Filename)
	if err != nil {
		respondWithMessage(s, i, fmt.Sprintf("❌ Failed to move file in object storage: %v", err))
		return
	}

	// Update PostgreSQL
	err = db.UpdateUploadStatus(uploadID, "approved")
	if err != nil {
		respondWithMessage(s, i, fmt.Sprintf("❌ Failed to update status in database: %v", err))
		return
	}

	embed := &discordgo.MessageEmbed{
		Title:       "Song Upload Approved",
		Description: fmt.Sprintf("✅ Custom track **%s** by **%s** was approved!\nIt is now playable by searching for the title or using `custom:%d`", upload.Title, upload.Artist, upload.ID),
		Color:       0x43b581,
	}
	respondWithEmbed(s, i, embed)
}

func handleReject(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Check administrator permissions
	if i.Member.Permissions&discordgo.PermissionAdministrator == 0 {
		respondWithMessage(s, i, "❌ Only server administrators can reject custom song uploads.")
		return
	}

	uploadID := int(i.ApplicationCommandData().Options[0].IntValue())
	ctx := context.Background()

	upload, err := db.GetUpload(uploadID)
	if err != nil {
		respondWithMessage(s, i, fmt.Sprintf("❌ Song upload record not found: %v", err))
		return
	}

	if upload.Status != "pending" {
		respondWithMessage(s, i, fmt.Sprintf("❌ This song upload is already in state: `%s`", upload.Status))
		return
	}

	// Remove from MinIO pending
	err = minioClient.RejectSong(ctx, upload.Filename)
	if err != nil {
		log.Printf("Warning: failed to delete file from pending directory in MinIO: %v", err)
	}

	// Update PostgreSQL
	err = db.UpdateUploadStatus(uploadID, "rejected")
	if err != nil {
		respondWithMessage(s, i, fmt.Sprintf("❌ Failed to update status in database: %v", err))
		return
	}

	embed := &discordgo.MessageEmbed{
		Title:       "Song Upload Rejected",
		Description: fmt.Sprintf("❌ Custom track **%s** by **%s** was rejected and deleted.", upload.Title, upload.Artist),
		Color:       0xf04747,
	}
	respondWithEmbed(s, i, embed)
}

// resolveTrack maps search strings/links into a playable QueueTrack.
func resolveTrack(query, requester string) (*QueueTrack, error) {
	// Check if it's a Spotify link
	if trackID, ok := ParseSpotifyTrackID(query); ok {
		if spotifyClient == nil {
			return nil, fmt.Errorf("spotify client is not configured")
		}
		title, artist, err := spotifyClient.GetTrackMetadata(trackID)
		if err != nil {
			return nil, fmt.Errorf("spotify track metadata lookup failed: %w", err)
		}

		// Since Spotify doesn't stream, we query YouTube search during playback using "Title Artist"
		searchQuery := fmt.Sprintf("%s - %s", title, artist)
		return &QueueTrack{
			ID:          "spotify_" + trackID,
			Title:       title,
			Artist:      artist,
			URL:         searchQuery,
			Source:      "spotify",
			RequestedBy: requester,
		}, nil
	}

	// Check if it's a MinIO custom command like custom:<id>
	if strings.HasPrefix(query, "custom:") {
		idStr := strings.TrimPrefix(query, "custom:")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			return nil, fmt.Errorf("invalid custom track ID syntax")
		}

		upload, err := db.GetUpload(id)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch custom upload record: %w", err)
		}

		if upload.Status != "approved" {
			return nil, fmt.Errorf("custom upload ID %d is in status '%s' and is not approved for play", id, upload.Status)
		}

		return &QueueTrack{
			ID:          fmt.Sprintf("minio_%d", upload.ID),
			Title:       upload.Title,
			Artist:      upload.Artist,
			URL:         upload.Filename,
			Source:      "minio",
			RequestedBy: requester,
		}, nil
	}

	// Check if it matches an approved song title in the database
	if upload, err := db.SearchApprovedSong(query); err == nil {
		return &QueueTrack{
			ID:          fmt.Sprintf("minio_%d", upload.ID),
			Title:       upload.Title,
			Artist:      upload.Artist,
			URL:         upload.Filename,
			Source:      "minio",
			RequestedBy: requester,
		}, nil
	}

	// Otherwise, fallback to generic YouTube Search or Direct URL
	title := query
	artist := "YouTube"

	if strings.HasPrefix(query, "http://") || strings.HasPrefix(query, "https://") {
		title = "YouTube Video Stream"
		artist = "Web Link"
	}

	return &QueueTrack{
		ID:          fmt.Sprintf("youtube_%d", time.Now().UnixNano()),
		Title:       title,
		Artist:      artist,
		URL:         query,
		Source:      "youtube",
		RequestedBy: requester,
	}, nil
}

// findUserVoiceState retrieves the VoiceState of a user in a guild.
func findUserVoiceState(s *discordgo.Session, guildID, userID string) (*discordgo.VoiceState, error) {
	guild, err := s.State.Guild(guildID)
	if err != nil {
		guild, err = s.Guild(guildID)
		if err != nil {
			return nil, err
		}
	}

	for _, vs := range guild.VoiceStates {
		if vs.UserID == userID {
			return vs, nil
		}
	}
	return nil, fmt.Errorf("user is not in any voice channel")
}
