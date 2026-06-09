# Stage 1: Build the Go binary
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY src/ ./src/
RUN CGO_ENABLED=0 GOOS=linux go build -o discord-music-bot ./src/

# Stage 2: Final lightweight image using Debian-Slim (Includes GLIBC for audio engines)
FROM debian:bookworm-slim

# Install system dependencies and clean up apt cache to keep image small
RUN apt-get update && apt-get install -y \
    ffmpeg \
    ca-certificates \
    python3 \
    wget \
    && rm -rf /var/lib/apt/lists/*

# Download the absolute latest release of yt-dlp into standard binary path
RUN wget https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -O /usr/local/bin/yt-dlp \
    && chmod a+rx /usr/local/bin/yt-dlp

WORKDIR /app

# Copy the compiled binary from stage 1
COPY --from=builder /app/discord-music-bot .

# Command to run the bot
ENTRYPOINT ["./discord-music-bot"]