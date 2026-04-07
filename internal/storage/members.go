package storage

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// --- Token helper ---

// GenerateToken returns a cryptographically random 64-char hex token.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// --- Members ---

// HasOwner returns true if the mailbox already has an owner.
func (s *SQLiteStore) HasOwner() (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM members WHERE role = 'owner'`).Scan(&n)
	return n > 0, err
}

// AddMember inserts a new member. Fails if pubkey_hash already exists.
func (s *SQLiteStore) AddMember(m *Member) error {
	if m.JoinedAt == 0 {
		m.JoinedAt = time.Now().Unix()
	}
	_, err := s.db.Exec(
		`INSERT INTO members (pubkey_hash, pubkey, role, display_name, joined_at)
         VALUES (?, ?, ?, ?, ?)`,
		m.PubkeyHash, m.Pubkey, m.Role, m.DisplayName, m.JoinedAt,
	)
	if err != nil {
		return fmt.Errorf("add member: %w", err)
	}
	return nil
}

// GetMember retrieves a member by pubkey_hash. Returns sql.ErrNoRows if absent.
func (s *SQLiteStore) GetMember(hash string) (*Member, error) {
	m := &Member{}
	err := s.db.QueryRow(
		`SELECT pubkey_hash, pubkey, role, display_name, joined_at FROM members WHERE pubkey_hash = ?`, hash,
	).Scan(&m.PubkeyHash, &m.Pubkey, &m.Role, &m.DisplayName, &m.JoinedAt)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// ListMembers returns all members ordered by joined_at ASC.
func (s *SQLiteStore) ListMembers() ([]*Member, error) {
	rows, err := s.db.Query(
		`SELECT pubkey_hash, pubkey, role, display_name, joined_at FROM members ORDER BY joined_at ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []*Member
	for rows.Next() {
		m := &Member{}
		if err := rows.Scan(&m.PubkeyHash, &m.Pubkey, &m.Role, &m.DisplayName, &m.JoinedAt); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// RemoveMember deletes a member. Returns error if hash is owner or not found.
func (s *SQLiteStore) RemoveMember(hash string) error {
	m, err := s.GetMember(hash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("member not found")
		}
		return err
	}
	if m.Role == "owner" {
		return fmt.Errorf("cannot remove the owner")
	}
	_, err = s.db.Exec(`DELETE FROM members WHERE pubkey_hash = ?`, hash)
	return err
}

// IsOwner returns true if hash belongs to the mailbox owner.
func (s *SQLiteStore) IsOwner(hash string) (bool, error) {
	var role string
	err := s.db.QueryRow(`SELECT role FROM members WHERE pubkey_hash = ?`, hash).Scan(&role)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return role == "owner", nil
}

// IsMember returns true if hash is a registered member (any role).
func (s *SQLiteStore) IsMember(hash string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM members WHERE pubkey_hash = ?`, hash).Scan(&n)
	return n > 0, err
}

// --- Invites ---

// CreateInvite inserts a new invite record.
func (s *SQLiteStore) CreateInvite(inv *Invite) error {
	if inv.CreatedAt == 0 {
		inv.CreatedAt = time.Now().Unix()
	}
	_, err := s.db.Exec(
		`INSERT INTO invites (token, role, created_by, created_at, expires_at, max_uses, use_count)
         VALUES (?, ?, ?, ?, ?, ?, 0)`,
		inv.Token, inv.Role, inv.CreatedBy, inv.CreatedAt, inv.ExpiresAt, inv.MaxUses,
	)
	if err != nil {
		return fmt.Errorf("create invite: %w", err)
	}
	return nil
}

// GetInvite retrieves an invite by token.
func (s *SQLiteStore) GetInvite(token string) (*Invite, error) {
	inv := &Invite{}
	err := s.db.QueryRow(
		`SELECT token, role, created_by, created_at, expires_at, max_uses, use_count
         FROM invites WHERE token = ?`, token,
	).Scan(&inv.Token, &inv.Role, &inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.MaxUses, &inv.UseCount)
	if err != nil {
		return nil, err
	}
	return inv, nil
}

// ListInvites returns all invites ordered by created_at DESC.
func (s *SQLiteStore) ListInvites() ([]*Invite, error) {
	rows, err := s.db.Query(
		`SELECT token, role, created_by, created_at, expires_at, max_uses, use_count
         FROM invites ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invites []*Invite
	for rows.Next() {
		inv := &Invite{}
		if err := rows.Scan(&inv.Token, &inv.Role, &inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.MaxUses, &inv.UseCount); err != nil {
			return nil, err
		}
		invites = append(invites, inv)
	}
	return invites, rows.Err()
}

// ConsumeInvite atomically validates the token and increments use_count.
// Returns the invite on success, or an error if expired/maxed/not found.
func (s *SQLiteStore) ConsumeInvite(token string) (*Invite, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	inv := &Invite{}
	err = tx.QueryRow(
		`SELECT token, role, created_by, created_at, expires_at, max_uses, use_count
         FROM invites WHERE token = ?`, token,
	).Scan(&inv.Token, &inv.Role, &inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.MaxUses, &inv.UseCount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("invite not found or already consumed")
		}
		return nil, err
	}

	now := time.Now().Unix()
	if inv.ExpiresAt > 0 && now > inv.ExpiresAt {
		return nil, fmt.Errorf("invite expired")
	}
	if inv.UseCount >= inv.MaxUses {
		return nil, fmt.Errorf("invite already used")
	}

	if _, err = tx.Exec(`UPDATE invites SET use_count = use_count + 1 WHERE token = ?`, token); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	inv.UseCount++
	return inv, nil
}

// RevokeInvite deletes an invite by token.
func (s *SQLiteStore) RevokeInvite(token string) error {
	res, err := s.db.Exec(`DELETE FROM invites WHERE token = ?`, token)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("invite not found")
	}
	return nil
}

// --- Server metadata ---

// SetMeta stores a key-value pair (upsert).
func (s *SQLiteStore) SetMeta(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO server_meta (key, value) VALUES (?, ?)
         ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

// GetMeta retrieves a value by key. Returns ("", nil) if not found.
func (s *SQLiteStore) GetMeta(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM server_meta WHERE key = ?`, key).Scan(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return value, nil
}
