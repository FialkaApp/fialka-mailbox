package api

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/fialkaapp/fialka-mailbox/internal/config"
	"github.com/fialkaapp/fialka-mailbox/internal/storage"
	"github.com/rs/zerolog"
)

// Server holds shared dependencies for all HTTP handlers.
type Server struct {
	store  *storage.SQLiteStore
	cfg    *config.Config
	logger zerolog.Logger
}

// NewHandler wires all HTTP routes and returns an http.Handler.
func NewHandler(store *storage.SQLiteStore, cfg *config.Config, logger zerolog.Logger) http.Handler {
	s := &Server{store: store, cfg: cfg, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /inbox/{pubkeyHash}", s.handleDeposit)
	mux.HandleFunc("GET /inbox/{pubkeyHash}", s.handleFetch)
	mux.HandleFunc("DELETE /inbox/{pubkeyHash}/{msgID}", s.handleAck)
	return mux
}

// handleHealth returns server and storage statistics.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.Stats()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":           "ok",
		"pending_messages": stats.PendingMessages,
		"recipients":       stats.Recipients,
		"total_size_bytes": stats.TotalSizeBytes,
	})
}

// handleDeposit accepts an encrypted blob from a sender.
//
// The server is a blind relay — it never decrypts the payload.
// Limits: max payload size, max messages per recipient, max total quota.
func (s *Server) handleDeposit(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("pubkeyHash")
	if !isValidHash(hash) {
		http.Error(w, "invalid recipient hash", http.StatusBadRequest)
		return
	}

	maxSize := s.cfg.Limits.MaxMessageSize
	r.Body = http.MaxBytesReader(w, r.Body, maxSize)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "payload too large or read error", http.StatusRequestEntityTooLarge)
		return
	}
	if len(payload) == 0 {
		http.Error(w, "empty payload", http.StatusBadRequest)
		return
	}

	// Quota: message count per recipient
	count, err := s.store.CountByRecipient(hash)
	if err != nil {
		s.logger.Error().Err(err).Msg("quota check failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if count >= s.cfg.Limits.MaxMessagesPerRecipient {
		http.Error(w, "recipient mailbox full", http.StatusTooManyRequests)
		return
	}

	ttlSecs := int64(s.cfg.Limits.MessageTTLHours) * 3600
	now := time.Now().Unix()
	msg := &storage.Message{
		Recipient:  hash,
		Payload:    payload,
		SizeBytes:  int64(len(payload)),
		ReceivedAt: now,
		ExpiresAt:  now + ttlSecs,
	}

	if err := s.store.Deposit(msg); err != nil {
		s.logger.Error().Err(err).Str("recipient", hash).Msg("deposit failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	s.logger.Info().Str("recipient", hash[:8]+"…").Int("size", len(payload)).Msg("deposited")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"id": msg.ID})
}

// handleFetch returns pending blobs for a recipient.
//
// Authentication: the recipient proves ownership of their Ed25519 key.
//   - Header X-Fialka-Pubkey:    base64(raw 32-byte Ed25519 pubkey)
//   - Header X-Fialka-Timestamp: unix seconds (rejected if >30s old)
//   - Header X-Fialka-Signature: base64(sign(pubkeyHash + "|" + timestamp))
//
// The server verifies sha256(pubkey) == pubkeyHash (URL param), then
// verifies the signature, then returns the blobs.
func (s *Server) handleFetch(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("pubkeyHash")
	if !isValidHash(hash) {
		http.Error(w, "invalid recipient hash", http.StatusBadRequest)
		return
	}

	pubkeyB64 := r.Header.Get("X-Fialka-Pubkey")
	tsStr := r.Header.Get("X-Fialka-Timestamp")
	sigB64 := r.Header.Get("X-Fialka-Signature")

	if pubkeyB64 == "" || tsStr == "" || sigB64 == "" {
		http.Error(w, "missing auth headers", http.StatusUnauthorized)
		return
	}

	pubkeyRaw, err := base64.StdEncoding.DecodeString(pubkeyB64)
	if err != nil || len(pubkeyRaw) != ed25519.PublicKeySize {
		http.Error(w, "invalid pubkey", http.StatusBadRequest)
		return
	}

	// Verify pubkey matches URL hash
	digest := sha256.Sum256(pubkeyRaw)
	expectedHash := hex.EncodeToString(digest[:])
	if expectedHash != hash {
		http.Error(w, "pubkey does not match hash", http.StatusForbidden)
		return
	}

	// Verify timestamp freshness (±30s window)
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid timestamp", http.StatusBadRequest)
		return
	}
	now := time.Now().Unix()
	if ts < now-30 || ts > now+30 {
		http.Error(w, "timestamp out of window", http.StatusUnauthorized)
		return
	}

	// Verify Ed25519 signature over "hash|timestamp"
	sigRaw, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		http.Error(w, "invalid signature encoding", http.StatusBadRequest)
		return
	}
	message := []byte(fmt.Sprintf("%s|%s", hash, tsStr))
	if !ed25519.Verify(ed25519.PublicKey(pubkeyRaw), message, sigRaw) {
		http.Error(w, "invalid signature", http.StatusForbidden)
		return
	}

	msgs, err := s.store.Fetch(hash)
	if err != nil {
		s.logger.Error().Err(err).Str("recipient", hash).Msg("fetch failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	type msgDTO struct {
		ID         string `json:"id"`
		Payload    string `json:"payload"` // base64-encoded ciphertext
		ReceivedAt int64  `json:"received_at"`
		ExpiresAt  int64  `json:"expires_at"`
	}
	dtos := make([]msgDTO, 0, len(msgs))
	for _, m := range msgs {
		dtos = append(dtos, msgDTO{
			ID:         m.ID,
			Payload:    base64.StdEncoding.EncodeToString(m.Payload),
			ReceivedAt: m.ReceivedAt,
			ExpiresAt:  m.ExpiresAt,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"messages": dtos,
		"count":    len(dtos),
	})
}

// handleAck deletes a delivered message.
//
// Same auth as handleFetch (X-Fialka-Pubkey, X-Fialka-Timestamp, X-Fialka-Signature).
// The signed message includes the msgID to prevent replay: "hash|timestamp|msgID".
func (s *Server) handleAck(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("pubkeyHash")
	msgID := r.PathValue("msgID")
	if !isValidHash(hash) || msgID == "" {
		http.Error(w, "invalid parameters", http.StatusBadRequest)
		return
	}

	pubkeyB64 := r.Header.Get("X-Fialka-Pubkey")
	tsStr := r.Header.Get("X-Fialka-Timestamp")
	sigB64 := r.Header.Get("X-Fialka-Signature")

	if pubkeyB64 == "" || tsStr == "" || sigB64 == "" {
		http.Error(w, "missing auth headers", http.StatusUnauthorized)
		return
	}

	pubkeyRaw, err := base64.StdEncoding.DecodeString(pubkeyB64)
	if err != nil || len(pubkeyRaw) != ed25519.PublicKeySize {
		http.Error(w, "invalid pubkey", http.StatusBadRequest)
		return
	}

	digest := sha256.Sum256(pubkeyRaw)
	if hex.EncodeToString(digest[:]) != hash {
		http.Error(w, "pubkey does not match hash", http.StatusForbidden)
		return
	}

	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid timestamp", http.StatusBadRequest)
		return
	}
	now := time.Now().Unix()
	if ts < now-30 || ts > now+30 {
		http.Error(w, "timestamp out of window", http.StatusUnauthorized)
		return
	}

	sigRaw, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		http.Error(w, "invalid signature encoding", http.StatusBadRequest)
		return
	}
	// Sign over hash|timestamp|msgID to bind the ack to a specific message
	message := []byte(fmt.Sprintf("%s|%s|%s", hash, tsStr, msgID))
	if !ed25519.Verify(ed25519.PublicKey(pubkeyRaw), message, sigRaw) {
		http.Error(w, "invalid signature", http.StatusForbidden)
		return
	}

	if err := s.store.Delete(msgID); err != nil {
		s.logger.Warn().Err(err).Str("msgID", msgID).Msg("ack delete failed")
		http.Error(w, "message not found", http.StatusNotFound)
		return
	}

	s.logger.Info().Str("msgID", msgID[:8]+"…").Msg("acked")
	w.WriteHeader(http.StatusNoContent)
}

// isValidHash checks that s looks like a hex SHA-256 hash (64 chars).
func isValidHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
