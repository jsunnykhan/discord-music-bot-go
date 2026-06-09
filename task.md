# Tasks: Restructure Discord Music Bot

- `[x]` Step 1: Create `src/config/config.go` (package config)
- `[x]` Step 2: Create `src/db/db.go` (package db)
- `[x]` Step 3: Create `src/redis.go` (package main, import config)
- `[x]` Step 4: Create `src/storage.go` (package main, import config)
- `[x]` Step 5: Create `src/spotify.go` (package main, import config)
- `[x]` Step 6: Create `src/email.go` (package main, import config)
- `[x]` Step 7: Create `src/player.go` (package main)
- `[x]` Step 8: Create `src/handlers.go` (package main, import config & db)
- `[x]` Step 9: Create `src/bot.go` (package main)
- `[x]` Step 10: Create `src/main.go` (package main, import config & db)
- `[x]` Step 11: Delete old root-level Go files
- `[x]` Step 12: Rewrite `.env.example` (comprehensive, no hardcoded values)
- `[x]` Step 13: Rewrite `README.md` (complete documentation)
- `[x]` Step 14: Run `go mod tidy` and compile
- `[x]` Step 15: Remove `GUILD_ID` and `MUSIC_REQUEST_CHANNEL_ID` and the dedicated channel monitor
- `[x]` Step 16: Verify compilation and build with docker-compose
- `[x]` Step 17: Update docker-compose.yml to load all service credentials and settings dynamically from .env
