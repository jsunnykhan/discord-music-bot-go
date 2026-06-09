package config

import (
	"bufio"
	"log"
	"os"
	"strconv"
	"strings"
)

// Config holds all the configuration parameters for the application.
// Every field is populated from environment variables or a .env file.
// There are NO hard-coded default values for credentials or secrets.
type Config struct {
	// Discord
	DiscordToken          string // Bot token from the Discord Developer Portal

	// PostgreSQL
	DatabaseURL string // Full connection string, e.g. postgres://user:pass@host:5432/dbname?sslmode=disable
	
	// Redis
	RedisAddr     string // host:port
	RedisPassword string
	RedisDB       int

	// MinIO (S3-compatible object storage)
	MinioEndpoint  string // host:port
	MinioAccessKey string
	MinioSecretKey string
	MinioBucket    string
	MinioUseSSL    bool

	// Spotify (optional — falls back to YouTube search if empty)
	SpotifyClientID     string
	SpotifyClientSecret string

	// SMTP email notifications
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string // Sender address
	AdminEmail   string // Recipient for upload approval notifications
}

// LoadConfig reads the environment variables (with an optional .env file)
// and returns a fully populated Config struct.
func LoadConfig() *Config {
	// Try to load a .env file first — variables already set in the
	// environment take precedence and will NOT be overwritten.
	loadDotEnv(".env")

	return &Config{
		DiscordToken:          getEnv("DISCORD_TOKEN", ""),

		DatabaseURL: getEnv("DATABASE_URL", ""),

		RedisAddr:     getEnv("REDIS_ADDR", ""),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),
		RedisDB:       getEnvInt("REDIS_DB", 0),

		MinioEndpoint:  getEnv("MINIO_ENDPOINT", ""),
		MinioAccessKey: getEnv("MINIO_ACCESS_KEY", ""),
		MinioSecretKey: getEnv("MINIO_SECRET_KEY", ""),
		MinioBucket:    getEnv("MINIO_BUCKET", ""),
		MinioUseSSL:    getEnvBool("MINIO_USE_SSL", false),

		SpotifyClientID:     getEnv("SPOTIFY_CLIENT_ID", ""),
		SpotifyClientSecret: getEnv("SPOTIFY_CLIENT_SECRET", ""),

		SMTPHost:     getEnv("SMTP_HOST", ""),
		SMTPPort:     getEnvInt("SMTP_PORT", 587),
		SMTPUsername: getEnv("SMTP_USERNAME", ""),
		SMTPPassword: getEnv("SMTP_PASSWORD", ""),
		SMTPFrom:     getEnv("SMTP_FROM", ""),
		AdminEmail:   getEnv("ADMIN_EMAIL", ""),
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// getEnv returns the value of the given environment variable, or defaultVal
// if the variable is empty or unset.
func getEnv(key, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val
	}
	return defaultVal
}

// getEnvInt parses an environment variable as an integer.
func getEnvInt(key string, defaultVal int) int {
	valStr := getEnv(key, "")
	if valStr == "" {
		return defaultVal
	}
	val, err := strconv.Atoi(valStr)
	if err != nil {
		return defaultVal
	}
	return val
}

// getEnvBool parses an environment variable as a boolean.
func getEnvBool(key string, defaultVal bool) bool {
	valStr := getEnv(key, "")
	if valStr == "" {
		return defaultVal
	}
	val, err := strconv.ParseBool(valStr)
	if err != nil {
		return defaultVal
	}
	return val
}

// loadDotEnv reads a .env file and sets environment variables for any key
// that is NOT already present in the environment. This means real env vars
// always take precedence over the file.
func loadDotEnv(filename string) {
	file, err := os.Open(filename)
	if err != nil {
		// Missing .env is fine — we just read from the real environment.
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip comments and empty lines
		if len(line) == 0 || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		// Strip surrounding quotes
		if len(val) >= 2 &&
			((val[0] == '"' && val[len(val)-1] == '"') ||
				(val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}

		// Only set if not already present in the real environment
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Warning: error reading %s: %v", filename, err)
	}
}
