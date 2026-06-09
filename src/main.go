package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"discord-music-bot/src/config"
	dbpkg "discord-music-bot/src/db"
)

// Global resource clients
var (
	cfg           *config.Config
	db            *dbpkg.Client
	rdb           *RedisClient
	minioClient   *MinioClient
	spotifyClient *SpotifyClient
)

func main() {
	log.Println("Starting Discord Music Bot...")

	// CLI Flags
	guildIDFlag := flag.String("guild", "", "Guild ID to register slash commands instantly (leave empty for global command registration)")
	flag.Parse()

	// 1. Load Configurations
	cfg = config.LoadConfig()
	if cfg.DiscordToken == "" {
		log.Fatalf("Error: DISCORD_TOKEN is not configured in the environment or .env file.")
	}

	// 2. Initialize PostgreSQL
	var err error
	db, err = dbpkg.Init(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Error initializing PostgreSQL: %v", err)
	}
	defer db.Close()

	// 3. Initialize Redis
	rdb, err = InitRedis(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		log.Fatalf("Error initializing Redis: %v", err)
	}
	defer rdb.Close()

	// 4. Initialize MinIO S3 Object Storage
	minioClient, err = InitMinio(cfg.MinioEndpoint, cfg.MinioAccessKey, cfg.MinioSecretKey, cfg.MinioBucket, cfg.MinioUseSSL)
	if err != nil {
		log.Fatalf("Error initializing MinIO: %v", err)
	}

	// 5. Initialize Spotify API client (if configured)
	if cfg.SpotifyClientID != "" && cfg.SpotifyClientSecret != "" {
		spotifyClient = NewSpotifyClient(cfg.SpotifyClientID, cfg.SpotifyClientSecret)
		log.Println("Spotify Integration initialized successfully.")
	} else {
		log.Println("Warning: Spotify Client Credentials not configured. Spotify track lookups will be unavailable (falling back to YouTube).")
	}

	// 6. Initialize and open Discord session
	dgSession, err := InitBot(cfg.DiscordToken)
	if err != nil {
		log.Fatalf("Error initializing Discord Session: %v", err)
	}

	err = dgSession.Open()
	if err != nil {
		log.Fatalf("Error opening connection to Discord gateway: %v", err)
	}
	defer dgSession.Close()

	// Determine command registration scope (Guild vs Global)
	guildID := *guildIDFlag

	// 7. Register Slash Commands
	err = RegisterCommands(dgSession, guildID)
	if err != nil {
		log.Fatalf("Error registering slash commands: %v", err)
	}

	// Clean up slash commands on shutdown
	defer CleanupCommands(dgSession, guildID)

	// Keep all active player voice connections cleaned up on shutdown
	defer func() {
		playersMu.RLock()
		defer playersMu.RUnlock()
		log.Printf("Disconnecting %d active voice connections on shutdown...", len(players))
		for _, player := range players {
			if err := player.Leave(); err != nil {
				log.Printf("Warning: error disconnecting from guild %s on shutdown: %v", player.GuildID, err)
			}
		}
	}()

	log.Println("Bot is running. Press CTRL+C to terminate.")

	// 8. Graceful shutdown handler
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	log.Println("Shutting down Discord Music Bot gracefully...")
}
