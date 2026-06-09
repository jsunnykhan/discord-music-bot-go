package db

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	_ "github.com/lib/pq" // PostgreSQL driver
)

// SongUpload represents a custom user-uploaded track stored in the database.
type SongUpload struct {
	ID         int
	Title      string
	Artist     string
	Filename   string
	Status     string // "pending", "approved", "rejected"
	UploadedBy string // Discord User ID
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Client wraps a *sql.DB and provides domain-specific query helpers.
type Client struct {
	db *sql.DB
}

// Init opens a connection to PostgreSQL, pings it (with retries), and
// auto-migrates the schema.
func Init(databaseURL string) (*Client, error) {
	conn, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("opening postgres: %w", err)
	}

	// Retry ping up to 5 times for slow container startups.
	var pingErr error
	for i := 0; i < 5; i++ {
		pingErr = conn.Ping()
		if pingErr == nil {
			break
		}
		log.Printf("Waiting for Postgres to start… (%d/5)", i+1)
		time.Sleep(2 * time.Second)
	}
	if pingErr != nil {
		return nil, fmt.Errorf("postgres ping failed: %w", pingErr)
	}

	c := &Client{db: conn}
	if err := c.autoMigrate(); err != nil {
		return nil, fmt.Errorf("postgres migration failed: %w", err)
	}

	log.Println("PostgreSQL connected and migrated successfully.")
	return c, nil
}

// Close closes the underlying database connection.
func (c *Client) Close() error {
	return c.db.Close()
}

// autoMigrate creates tables if they don't already exist.
func (c *Client) autoMigrate() error {
	query := `
	CREATE TABLE IF NOT EXISTS song_uploads (
		id          SERIAL PRIMARY KEY,
		title       VARCHAR(255) NOT NULL,
		artist      VARCHAR(255) NOT NULL,
		filename    VARCHAR(255) NOT NULL,
		status      VARCHAR(50)  NOT NULL DEFAULT 'pending',
		uploaded_by VARCHAR(50)  NOT NULL,
		created_at  TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
		updated_at  TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
	);`
	_, err := c.db.Exec(query)
	return err
}

// InsertUpload inserts a new pending upload and returns the generated ID.
func (c *Client) InsertUpload(title, artist, filename, uploadedBy string) (int, error) {
	query := `
	INSERT INTO song_uploads (title, artist, filename, status, uploaded_by, created_at, updated_at)
	VALUES ($1, $2, $3, 'pending', $4, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	RETURNING id;`

	var id int
	err := c.db.QueryRow(query, title, artist, filename, uploadedBy).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("inserting upload: %w", err)
	}
	return id, nil
}

// GetUpload retrieves a single upload by its database ID.
func (c *Client) GetUpload(id int) (*SongUpload, error) {
	query := `
	SELECT id, title, artist, filename, status, uploaded_by, created_at, updated_at
	FROM song_uploads
	WHERE id = $1;`

	var u SongUpload
	err := c.db.QueryRow(query, id).Scan(
		&u.ID, &u.Title, &u.Artist, &u.Filename,
		&u.Status, &u.UploadedBy, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("upload with ID %d not found", id)
		}
		return nil, fmt.Errorf("fetching upload: %w", err)
	}
	return &u, nil
}

// UpdateUploadStatus changes the approval status of an upload.
func (c *Client) UpdateUploadStatus(id int, status string) error {
	query := `
	UPDATE song_uploads
	SET status = $1, updated_at = CURRENT_TIMESTAMP
	WHERE id = $2;`

	res, err := c.db.Exec(query, status, id)
	if err != nil {
		return fmt.Errorf("updating upload status: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("no upload found with ID %d", id)
	}
	return nil
}

// SearchApprovedSong finds the first approved upload whose title matches
// (exact or partial, case-insensitive).
func (c *Client) SearchApprovedSong(queryStr string) (*SongUpload, error) {
	query := `
	SELECT id, title, artist, filename, status, uploaded_by, created_at, updated_at
	FROM song_uploads
	WHERE (LOWER(title) = LOWER($1) OR LOWER(title) LIKE LOWER($2))
	  AND status = 'approved'
	LIMIT 1;`

	var u SongUpload
	err := c.db.QueryRow(query, queryStr, "%"+queryStr+"%").Scan(
		&u.ID, &u.Title, &u.Artist, &u.Filename,
		&u.Status, &u.UploadedBy, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("no approved upload matching '%s'", queryStr)
		}
		return nil, fmt.Errorf("searching approved song: %w", err)
	}
	return &u, nil
}
