package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// QueueTrack represents a single track in the guild playback queue.
type QueueTrack struct {
	ID          string `json:"id"`           // Unique track identifier
	Title       string `json:"title"`        // Song title
	Artist      string `json:"artist"`       // Artist name(s)
	URL         string `json:"url"`          // Playback source URL or search query
	Source      string `json:"source"`       // "spotify", "youtube", or "minio"
	RequestedBy string `json:"requested_by"` // Discord username of the requester
	Duration    string `json:"duration"`     // Human-readable duration (if available)
}

// RedisClient wraps go-redis and provides queue / player-state helpers.
type RedisClient struct {
	rdb *redis.Client
}

// InitRedis connects to Redis and verifies the connection with a ping.
func InitRedis(addr, password string, db int) (*RedisClient, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping failed at %s: %w", addr, err)
	}

	log.Printf("Redis connected at %s (DB %d).", addr, db)
	return &RedisClient{rdb: rdb}, nil
}

// Close shuts down the Redis connection.
func (c *RedisClient) Close() error { return c.rdb.Close() }

// Key helpers
func queueKey(guildID string) string { return "guild_queue:" + guildID }
func stateKey(guildID string) string { return "guild_player:" + guildID }

// EnqueueTrack pushes a track to the head of the guild queue (LPUSH).
func (c *RedisClient) EnqueueTrack(ctx context.Context, guildID string, track *QueueTrack) error {
	data, err := json.Marshal(track)
	if err != nil {
		return fmt.Errorf("serializing track: %w", err)
	}
	return c.rdb.LPush(ctx, queueKey(guildID), data).Err()
}

// DequeueTrack pops a track from the tail of the guild queue (RPOP / FIFO).
// Returns (nil, nil) when the queue is empty.
func (c *RedisClient) DequeueTrack(ctx context.Context, guildID string) (*QueueTrack, error) {
	data, err := c.rdb.RPop(ctx, queueKey(guildID)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("RPOP queue: %w", err)
	}

	var track QueueTrack
	if err := json.Unmarshal([]byte(data), &track); err != nil {
		return nil, fmt.Errorf("deserializing track: %w", err)
	}
	return &track, nil
}

// GetQueue returns every track currently in the queue, ordered from
// next-to-play → last-to-play.
func (c *RedisClient) GetQueue(ctx context.Context, guildID string) ([]QueueTrack, error) {
	list, err := c.rdb.LRange(ctx, queueKey(guildID), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("LRANGE queue: %w", err)
	}

	tracks := make([]QueueTrack, len(list))
	for i, item := range list {
		var t QueueTrack
		if err := json.Unmarshal([]byte(item), &t); err != nil {
			log.Printf("Warning: skipping malformed queue entry: %v", err)
			continue
		}
		// LPUSH prepends, RPOP removes from the tail — so index 0 is the
		// *last* item added. Reverse so the result is play-order.
		tracks[len(list)-1-i] = t
	}
	return tracks, nil
}

// ClearQueue deletes the entire queue for a guild.
func (c *RedisClient) ClearQueue(ctx context.Context, guildID string) error {
	return c.rdb.Del(ctx, queueKey(guildID)).Err()
}

// SetPlayerState sets one or more fields in the guild player-state hash.
func (c *RedisClient) SetPlayerState(ctx context.Context, guildID string, state map[string]interface{}) error {
	if len(state) == 0 {
		return nil
	}
	return c.rdb.HSet(ctx, stateKey(guildID), state).Err()
}

// GetPlayerState returns the full player-state hash for a guild.
func (c *RedisClient) GetPlayerState(ctx context.Context, guildID string) (map[string]string, error) {
	res, err := c.rdb.HGetAll(ctx, stateKey(guildID)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("HGETALL player state: %w", err)
	}
	return res, nil
}

// ClearPlayerState deletes the player-state hash for a guild.
func (c *RedisClient) ClearPlayerState(ctx context.Context, guildID string) error {
	return c.rdb.Del(ctx, stateKey(guildID)).Err()
}
