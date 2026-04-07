package storage

// Message represents an encrypted blob stored on the relay.
// The server never inspects Payload — it is opaque ciphertext.
type Message struct {
	ID          string // UUID
	Recipient   string // SHA-256 hash of recipient Ed25519 pubkey
	Payload     []byte // encrypted blob (opaque to the server)
	SizeBytes   int64
	ReceivedAt  int64 // unix timestamp
	ExpiresAt   int64 // unix timestamp (TTL)
}

// Store is the storage interface.
// Implementations: SQLite (default), in-memory (tests).
type Store interface {
	// Deposit stores an encrypted message blob.
	Deposit(msg *Message) error

	// Fetch returns all pending messages for a recipient.
	Fetch(recipientHash string) ([]*Message, error)

	// Delete removes a message after confirmed delivery.
	Delete(id string) error

	// Expire removes all messages past their TTL.
	Expire() (int, error)

	// Stats returns basic server statistics.
	Stats() (*Stats, error)

	Close() error
}

type Stats struct {
	PendingMessages int64
	TotalSizeBytes  int64
	Recipients      int64
}
