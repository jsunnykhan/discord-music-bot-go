package main

import (
	"fmt"
	"log"

	"github.com/bwmarrin/discordgo"
)

// Global registry of created commands to delete on shutdown
var registeredCommands []*discordgo.ApplicationCommand

// InitBot initializes the Discord Session and registers handlers.
func InitBot(token string) (*discordgo.Session, error) {
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, fmt.Errorf("failed to create discord session: %w", err)
	}

	// Configure gateway intents
	// IntentMessageContent is required to monitor requests in #song-requests channel.
	s.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsGuildVoiceStates |
		discordgo.IntentMessageContent

	// Add event handlers
	s.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Printf("Discord Music Bot logged in as: %s#%s", s.State.User.Username, s.State.User.Discriminator)
	})

	s.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type == discordgo.InteractionApplicationCommand {
			if handler, ok := commandHandlers[i.ApplicationCommandData().Name]; ok {
				handler(s, i)
			}
		}
	})

	return s, nil
}

// RegisterCommands registers all defined slash commands to either a specific guild or globally.
// (Guild-specific registration is instant; global registration can take up to an hour).
func RegisterCommands(s *discordgo.Session, guildID string) error {
	log.Printf("Registering slash commands (Guild ID: '%s')...", guildID)
	registeredCommands = make([]*discordgo.ApplicationCommand, 0, len(commands))

	for _, cmd := range commands {
		createdCmd, err := s.ApplicationCommandCreate(s.State.User.ID, guildID, cmd)
		if err != nil {
			return fmt.Errorf("failed to create command '%s': %w", cmd.Name, err)
		}
		registeredCommands = append(registeredCommands, createdCmd)
	}

	log.Printf("Successfully registered %d slash commands.", len(registeredCommands))
	return nil
}

// CleanupCommands deletes registered slash commands on shutdown.
func CleanupCommands(s *discordgo.Session, guildID string) {
	if len(registeredCommands) == 0 {
		return
	}

	log.Printf("Cleaning up %d registered slash commands...", len(registeredCommands))
	for _, cmd := range registeredCommands {
		err := s.ApplicationCommandDelete(s.State.User.ID, guildID, cmd.ID)
		if err != nil {
			log.Printf("Warning: failed to delete command '%s': %v", cmd.Name, err)
		}
	}
	log.Println("Slash commands cleanup completed.")
}
