# Discord Music Bot

A highly readable, clean, and pragmatically structured Go Discord Music Bot application utilizing `bwmarrin/discordgo`. The architecture separates configuration and database client logics into dedicated sub-packages under `src/`, keeping the remaining bot handlers and player execution under package `main` within the `src/` folder to maintain modularity without over-engineered complexity.

## Features

1. **Redis-Backed State & Queue**: Employs Redis Lists (`LPUSH`, `RPOP`) for high-performance guild-level music queuing, and Redis Hashes for active playback states.
2. **Spotify Metadata & MinIO Fallback**: Resolves Spotify track links using the Spotify Web API to obtain metadata (Title, Artist) and resolves it to a stream using `yt-dlp`. Plays custom tracks directly from MinIO object storage.
3. **Song Upload & Admin Approval**: Allows users to upload MP3s via `/upload-song`, which logs to PostgreSQL as `pending` and uploads to MinIO. The bot sends an automated SMTP email to the Admin. Admins can run `/approve <id>` or `/reject <id>` to move/remove the track.

---

## Directory Layout

- `src/`
  - [main.go](file:///Users/sunny/jsunnykhan/src/main.go): App entrypoint, loads configs, initializes database/storage clients, registers commands, and handles OS graceful shutdown.
  - [bot.go](file:///Users/sunny/jsunnykhan/src/bot.go): Creates the Discord session, registers gateway intents, slash commands, and cleans up commands on shutdown.
  - [handlers.go](file:///Users/sunny/jsunnykhan/src/handlers.go): Handles all slash commands dynamically across guilds and channels.
  - [player.go](file:///Users/sunny/jsunnykhan/src/player.go): Coordinates audio voice channel joining, real-time transcoding using `dca`, and managing active playback loops.
  - [redis.go](file:///Users/sunny/jsunnykhan/src/redis.go): Wrapper for Redis lists (queues) and hashes (active state).
  - [storage.go](file:///Users/sunny/jsunnykhan/src/storage.go): Wrapper for MinIO Go SDK (pending and library objects management).
  - [spotify.go](file:///Users/sunny/jsunnykhan/src/spotify.go): Web API client for Spotify client credentials and track metadata lookup.
  - [email.go](file:///Users/sunny/jsunnykhan/src/email.go): Standard Go `net/smtp` mail notification sender.
  - `config/`
    - [config.go](file:///Users/sunny/jsunnykhan/src/config/config.go): Pure Go loader for configuration variables (checks environment variables and parses `.env`).
  - `db/`
    - [db.go](file:///Users/sunny/jsunnykhan/src/db/db.go): Wrapper for PostgreSQL database schema migrations and song upload logs.

- [docker-compose.yml](file:///Users/sunny/jsunnykhan/docker-compose.yml): Local services container configuration (PostgreSQL, Redis, MinIO).
- [go.mod](file:///Users/sunny/jsunnykhan/go.mod): Go module definition.
- [task.md](file:///Users/sunny/jsunnykhan/task.md): Project checklist and task list tracking file.

---

## Prerequisites & Installation

### 1. System Dependencies
This bot transcodes audio in real-time. You must have **`ffmpeg`** and **`yt-dlp`** installed and available in your system's `PATH`.

#### On macOS (using Homebrew)
```bash
brew install ffmpeg yt-dlp
```

#### On Linux (Debian/Ubuntu)
```bash
sudo apt update
sudo apt install -y ffmpeg
# Install yt-dlp
sudo wget https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -O /usr/local/bin/yt-dlp
sudo chmod a+rx /usr/local/bin/yt-dlp
```

### 2. Configure Environment
Create a `.env` file in the root directory from the provided example template:
```bash
cp .env.example .env
```
Fill in your `DISCORD_TOKEN` and any other configurations (like Spotify client details and SMTP admin emails).

---

## Running the Bot

### 1. Via Docker Compose (Recommended)
You can run the entire stack (PostgreSQL, Redis, MinIO, and the bot itself) in a single command. Once your `.env` file is set up, run:
```bash
docker-compose up -d --build
```
This command builds the Alpine-based bot container, links it to Postgres, Redis, and MinIO within the internal Docker network, and launches the entire application.

### 2. Running Locally (Development)
If you want to run the bot locally on your host machine while using Docker for the background services (PostgreSQL, Redis, MinIO):

First, spin up only the database, cache, and storage containers:
```bash
docker-compose up -d postgres redis minio
```

Then, run the Go application directly from the root directory targeting all package main files in the `src` folder:
```bash
go run ./src/*.go
```

To register Slash Commands instantly to a single guild for testing, you can use the `-guild` flag:
```bash
go run ./src/*.go -guild <your_test_guild_id>
```

---

## Command Reference

### Voice & Playback Commands
- `/join` : Connects the bot to your current voice channel.
- `/leave` : Disconnects the bot from the voice channel.
- `/play <query/url>` : Enqueues a track and starts the playback loop if idle.
  - Resolves YouTube URLs, search queries, Spotify links, or custom songs (using `custom:<id>` or title matching).
- `/pause` : Pauses the active track.
- `/resume` : Resumes the paused track.
- `/stop` : Terminates playback, deletes the Redis queue, and resets the player state.
- `/queue` : Lists upcoming tracks in the queue and the now playing track details.

### Custom Song Upload & Admin Approval
- `/upload-song <file> <title> <artist>` : Uploads an MP3 file to storage under `pending/`, logs the request, and notifies the admin by email.
- `/approve <id>` : (Admin Only) Approves the upload ID, moving the file in storage to `library/` and making it playable.
- `/reject <id>` : (Admin Only) Rejects and deletes the pending song file from storage.
