# Stage 1: Build the Go binary
FROM golang:1.26-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git

# Set working directory
WORKDIR /app

# Copy dependency files and download
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY src/ ./src/

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o discord-music-bot ./src/*.go

# Stage 2: Final lightweight image
FROM alpine:3.21

# Install runtime dependencies (ffmpeg, yt-dlp, and ca-certificates for external API calls)
RUN apk add --no-cache \
    ffmpeg \
    yt-dlp \
    ca-certificates \
    python3

# Set working directory
WORKDIR /app

# Copy the compiled binary from stage 1
COPY --from=builder /app/discord-music-bot .

# Command to run the bot
ENTRYPOINT ["./discord-music-bot"]
