package api

import (
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/fialkaapp/fialka-mailbox/internal/storage"
)

// --- GET /mailbox/info ---

// handleMailboxInfo returns public server information.
// No auth required — any client can discover the server's capabilities.
func (s *Server) handleMailboxInfo(w http.ResponseWriter, r *http.Request) {
	hasOwner, err := s.store.HasOwner()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	onion, _ := s.store.GetMeta("onion_address")

	var memberCount int64
	if stats, err := s.store.Stats(); err == nil {
		_ = stats
	}
	members, _ := s.store.ListMembers()
	memberCount = int64(len(members))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"version":      "0.1.0",
		"has_owner":    hasOwner,
		"member_count": memberCount,
		"onion":        onion,
	})
}

// --- POST /mailbox/join ---

// joinRequest is the body expected by handleJoin.
type joinRequest struct {
	Token       string `json:"token"`
	PubkeyB64   string `json:"pubkey_b64"`
	DisplayName string `json:"display_name"`
	// SigB64 is ed25519.Sign(privkey, token_bytes)
	// Proves the caller owns the private key matching PubkeyB64.
	SigB64 string `json:"sig_b64"`
}

// handleJoin handles a client joining the mailbox using an invite token.
//
// The invite token is the credential — no prior membership needed.
// The client proves ownership of their Ed25519 key by signing the raw token bytes.
// The server verifies:
//  1. Token is valid, not expired, not exhausted.
//  2. sha256(pubkey) is not already a member (idempotent join blocked).
//  3. For owner-role invites: no owner exists yet.
//  4. ed25519.Verify(pubkey, token_bytes, sig) — proves key ownership.
func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	var req joinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	if req.Token == "" || req.PubkeyB64 == "" || req.SigB64 == "" {
		http.Error(w, "token, pubkey_b64 and sig_b64 are required", http.StatusBadRequest)
		return
	}

	// Decode pubkey
	pubkeyRaw, err := base64.StdEncoding.DecodeString(req.PubkeyB64)
	if err != nil || len(pubkeyRaw) != ed25519.PublicKeySize {
		http.Error(w, "invalid pubkey_b64 (must be base64 of 32-byte Ed25519 public key)", http.StatusBadRequest)
		return
	}

	// Derive hash
	digest := sha256.Sum256(pubkeyRaw)
	pubkeyHash := hex.EncodeToString(digest[:])

	// Reject if already a member (prevent re-join)
	if already, _ := s.store.IsMember(pubkeyHash); already {
		http.Error(w, "already a member", http.StatusConflict)
		return
	}

	// Verify key ownership: sig must be over the raw token bytes
	sigRaw, err := base64.StdEncoding.DecodeString(req.SigB64)
	if err != nil {
		http.Error(w, "invalid sig_b64 encoding", http.StatusBadRequest)
		return
	}
	tokenBytes := []byte(req.Token)
	if !ed25519.Verify(ed25519.PublicKey(pubkeyRaw), tokenBytes, sigRaw) {
		http.Error(w, "signature verification failed", http.StatusForbidden)
		return
	}

	// Consume invite (atomic — validates expiry + use count)
	invite, err := s.store.ConsumeInvite(req.Token)
	if err != nil {
		http.Error(w, fmt.Sprintf("invite invalid: %s", err.Error()), http.StatusForbidden)
		return
	}

	// Owner invite: ensure no owner exists yet
	if invite.Role == "owner" {
		if hasOwner, _ := s.store.HasOwner(); hasOwner {
			http.Error(w, "owner already exists — this invite is no longer valid", http.StatusConflict)
			return
		}
	}

	member := &storage.Member{
		PubkeyHash:  pubkeyHash,
		Pubkey:      pubkeyRaw,
		Role:        invite.Role,
		DisplayName: req.DisplayName,
		JoinedAt:    time.Now().Unix(),
	}

	if err := s.store.AddMember(member); err != nil {
		s.logger.Error().Err(err).Str("hash", pubkeyHash[:8]+"…").Msg("add member failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.logger.Info().Str("hash", pubkeyHash[:8]+"…").Str("role", invite.Role).Msg("member joined")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"pubkey_hash": pubkeyHash,
		"role":        invite.Role,
	})
}

// --- POST /mailbox/invite ---

// inviteRequest is the body expected by handleCreateInvite.
type inviteRequest struct {
	Role        string `json:"role"`         // "member" (default) — only owner can create "owner" invites
	MaxUses     int    `json:"max_uses"`     // default 1
	ExpiresHours int  `json:"expires_hours"` // 0 = no expiry
}

// handleCreateInvite creates a new invite token. Requires owner auth.
func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	callerHash, _, err := verifyMgmtAuth(r)
	if err != nil {
		http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}

	isOwner, err := s.store.IsOwner(callerHash)
	if err != nil || !isOwner {
		http.Error(w, "forbidden: only the owner can create invites", http.StatusForbidden)
		return
	}

	var req inviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	role := req.Role
	if role == "" {
		role = "member"
	}
	if role != "owner" && role != "member" {
		http.Error(w, "role must be 'owner' or 'member'", http.StatusBadRequest)
		return
	}
	// Prevent creating a second owner invite if an owner already exists
	if role == "owner" {
		if hasOwner, _ := s.store.HasOwner(); hasOwner {
			http.Error(w, "owner already exists — cannot create another owner invite", http.StatusConflict)
			return
		}
	}

	maxUses := req.MaxUses
	if maxUses <= 0 {
		maxUses = 1
	}

	token, err := storage.GenerateToken()
	if err != nil {
		http.Error(w, "internal error generating token", http.StatusInternalServerError)
		return
	}

	now := time.Now()
	var expiresAt int64
	if req.ExpiresHours > 0 {
		expiresAt = now.Add(time.Duration(req.ExpiresHours) * time.Hour).Unix()
	}

	invite := &storage.Invite{
		Token:     token,
		Role:      role,
		CreatedBy: callerHash,
		CreatedAt: now.Unix(),
		ExpiresAt: expiresAt,
		MaxUses:   maxUses,
	}

	if err := s.store.CreateInvite(invite); err != nil {
		s.logger.Error().Err(err).Msg("create invite failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	onion, _ := s.store.GetMeta("onion_address")
	inviteLink := buildInviteLink(onion, token)

	s.logger.Info().Str("role", role).Int("max_uses", maxUses).Msg("invite created")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"token":       token,
		"role":        role,
		"max_uses":    maxUses,
		"expires_at":  expiresAt,
		"invite_link": inviteLink,
	})
}

// --- GET /mailbox/members ---

// handleListMembers returns all members. Requires owner auth.
func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	callerHash, _, err := verifyMgmtAuth(r)
	if err != nil {
		http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}
	if isOwner, _ := s.store.IsOwner(callerHash); !isOwner {
		http.Error(w, "forbidden: only the owner can list members", http.StatusForbidden)
		return
	}

	members, err := s.store.ListMembers()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	type memberDTO struct {
		PubkeyHash  string `json:"pubkey_hash"`
		Role        string `json:"role"`
		DisplayName string `json:"display_name"`
		JoinedAt    int64  `json:"joined_at"`
	}
	dtos := make([]memberDTO, 0, len(members))
	for _, m := range members {
		dtos = append(dtos, memberDTO{
			PubkeyHash:  m.PubkeyHash,
			Role:        m.Role,
			DisplayName: m.DisplayName,
			JoinedAt:    m.JoinedAt,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"members": dtos,
		"count":   len(dtos),
	})
}

// --- DELETE /mailbox/members/{hash} ---

// handleKickMember removes a member. Requires owner auth.
// The owner cannot kick themselves.
func (s *Server) handleKickMember(w http.ResponseWriter, r *http.Request) {
	callerHash, _, err := verifyMgmtAuth(r)
	if err != nil {
		http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}
	if isOwner, _ := s.store.IsOwner(callerHash); !isOwner {
		http.Error(w, "forbidden: only the owner can kick members", http.StatusForbidden)
		return
	}

	targetHash := r.PathValue("hash")
	if !isValidHash(targetHash) {
		http.Error(w, "invalid member hash", http.StatusBadRequest)
		return
	}
	if targetHash == callerHash {
		http.Error(w, "cannot kick yourself", http.StatusBadRequest)
		return
	}

	if err := s.store.RemoveMember(targetHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "member not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.logger.Info().Str("kicked", targetHash[:8]+"…").Str("by", callerHash[:8]+"…").Msg("member kicked")
	w.WriteHeader(http.StatusNoContent)
}

// --- GET /mailbox/invites ---

// handleListInvites returns all invite tokens. Requires owner auth.
func (s *Server) handleListInvites(w http.ResponseWriter, r *http.Request) {
	callerHash, _, err := verifyMgmtAuth(r)
	if err != nil {
		http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}
	if isOwner, _ := s.store.IsOwner(callerHash); !isOwner {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	invites, err := s.store.ListInvites()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	onion, _ := s.store.GetMeta("onion_address")

	type invDTO struct {
		Token      string `json:"token"`
		Role       string `json:"role"`
		CreatedBy  string `json:"created_by"`
		CreatedAt  int64  `json:"created_at"`
		ExpiresAt  int64  `json:"expires_at"`
		MaxUses    int    `json:"max_uses"`
		UseCount   int    `json:"use_count"`
		InviteLink string `json:"invite_link"`
	}
	dtos := make([]invDTO, 0, len(invites))
	for _, inv := range invites {
		dtos = append(dtos, invDTO{
			Token:      inv.Token,
			Role:       inv.Role,
			CreatedBy:  inv.CreatedBy,
			CreatedAt:  inv.CreatedAt,
			ExpiresAt:  inv.ExpiresAt,
			MaxUses:    inv.MaxUses,
			UseCount:   inv.UseCount,
			InviteLink: buildInviteLink(onion, inv.Token),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"invites": dtos, "count": len(dtos)})
}

// --- DELETE /mailbox/invites/{token} ---

// handleRevokeInvite removes an invite. Requires owner auth.
func (s *Server) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	callerHash, _, err := verifyMgmtAuth(r)
	if err != nil {
		http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}
	if isOwner, _ := s.store.IsOwner(callerHash); !isOwner {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	token := r.PathValue("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	if err := s.store.RevokeInvite(token); err != nil {
		http.Error(w, "invite not found", http.StatusNotFound)
		return
	}

	s.logger.Info().Str("token", token[:8]+"…").Msg("invite revoked")
	w.WriteHeader(http.StatusNoContent)
}

// buildInviteLink constructs the deep-link URI for the Fialka Android app.
// Format: fialka-mailbox://{onion_address}/join/{token}
func buildInviteLink(onion, token string) string {
	if onion == "" {
		onion = "<onion-address>"
	}
	return fmt.Sprintf("fialka-mailbox://%s/join/%s", onion, token)
}
