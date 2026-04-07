package storage

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// SQLiteStore is the production SQLite-backed Store implementation.
type SQLiteStore struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS messages (
	id           TEXT PRIMARY KEY,
	recipient    TEXT NOT NULL,
	payload      BLOB NOT NULL,
	size_bytes   INTEGER NOT NULL,
	received_at  INTEGER NOT NULL,
	expires_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_recipient ON messages(recipient);
CREATE INDEX IF NOT EXISTS idx_messages_expires_at ON messages(expires_at);
`

// NewSQLiteStore opens (or creates) the SQLite database at dbPath.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	// Enable WAL mode and foreign keys via DSN params
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite: %w", err)
	}

	// Single-writer, multiple readers
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating schema: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

// Deposit stores an encrypted message blob.
func (s *SQLiteStore) Deposit(msg *Message) error {
	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}
	now := time.Now().Unix()
	if msg.ReceivedAt == 0 {
		msg.ReceivedAt = now
	}

	_, err := s.db.Exec(
		`INSERT INTO messages (id, recipient, payload, size_bytes, received_at, expires_at)
         VALUES (?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.Recipient, msg.Payload, msg.SizeBytes, msg.ReceivedAt, msg.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("deposit: %w", err)
	}
	return nil
}

// Fetch returns all non-expired messages for a recipient, ordered oldest first.
func (s *SQLiteStore) Fetch(recipientHash string) ([]*Message, error) {
	now := time.Now().Unix()
	rows, err := s.db.Query(
		`SELECT id, recipient, payload, size_bytes, received_at, expires_at
         FROM messages
         WHERE recipient = ? AND expires_at > ?
         ORDER BY received_at ASC`,
		recipientHash, now,
	)
	if err != nil {
		return nil, fmt.Errorf("fetch query: %w", err)
	}
	defer rows.Close()

	var msgs []*Message
	for rows.Next() {
		m := &Message{}
		if err := rows.Scan(&m.ID, &m.Recipient, &m.Payload, &m.SizeBytes, &m.ReceivedAt, &m.ExpiresAt); err != nil {
			return nil, fmt.Errorf("fetch scan: %w", err)
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// Delete removes a message by ID (after confirmed delivery).
func (s *SQLiteStore) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM messages WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("message not found: %s", id)
	}
	return nil
}

// Expire removes all messages past their TTL. Returns the number deleted.
func (s *SQLiteStore) Expire() (int, error) {
	now := time.Now().Unix()
	res, err := s.db.Exec(`DELETE FROM messages WHERE expires_at <= ?`, now)
	if err != nil {
		return 0, fmt.Errorf("expire: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Stats returns aggregate storage statistics.
func (s *SQLiteStore) Stats() (*Stats, error) {
	row := s.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(size_bytes), 0), COUNT(DISTINCT recipient)
		FROM messages
		WHERE expires_at > ?`, time.Now().Unix())

	st := &Stats{}
	if err := row.Scan(&st.PendingMessages, &st.TotalSizeBytes, &st.Recipients); err != nil {
		return nil, fmt.Errorf("stats: %w", err)
	}
	return st, nil
}

// CountByRecipient returns the number of pending messages for a recipient.
func (s *SQLiteStore) CountByRecipient(recipientHash string) (int, error) {
	now := time.Now().Unix()
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM messages WHERE recipient = ? AND expires_at > ?`,
		recipientHash, now,
	).Scan(&n)
	return n, err
}

// TotalSizeByRecipient returns the total payload size for a recipient.
func (s *SQLiteStore) TotalSizeByRecipient(recipientHash string) (int64, error) {
	now := time.Now().Unix()
	var n int64
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(size_bytes), 0) FROM messages WHERE recipient = ? AND expires_at > ?`,
		recipientHash, now,
	).Scan(&n)
	return n, err
}

// Close closes the underlying database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
