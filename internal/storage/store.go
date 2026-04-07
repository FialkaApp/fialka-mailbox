package storage

// Message represents an encrypted blob stored on the relay.
// The server never inspects Payload — it is opaque ciphertext.
type Message struct {
	ID         string // UUID
	Recipient  string // SHA-256 hash of recipient Ed25519 pubkey
	Payload    []byte // encrypted blob (opaque to the server)
	SizeBytes  int64
	ReceivedAt int64 // unix timestamp
	ExpiresAt  int64 // unix timestamp (TTL)
}

// Member is a registered mailbox participant.
// Role is either "owner" (exactly one) or "member".
type Member struct {
	PubkeyHash  string
	Pubkey      []byte // raw 32-byte Ed25519 public key
	Role        string // "owner" | "member"
	DisplayName string
	JoinedAt    int64 // unix timestamp
}

// Invite is a limited-use join token.
type Invite struct {
	Token     string
	Role      string // role granted on use: "owner" | "member"
	CreatedBy string // pubkey_hash of creator; "" = server admin CLI
	CreatedAt int64
	ExpiresAt int64 // 0 = no expiry
	MaxUses   int
	UseCount  int
}

// Store is the message storage interface.
// Implementations: SQLiteStore (default), in-memory (tests).
type Store interface {
	Deposit(msg *Message) error
	Fetch(recipientHash string) ([]*Message, error)
	Delete(id string) error
	Expire() (int, error)
	Stats() (*Stats, error)
	Close() error
}

// Stats aggregates server-wide storage statistics.
type Stats struct {
	PendingMessages int64
	TotalSizeBytes  int64
	Recipients      int64
}
