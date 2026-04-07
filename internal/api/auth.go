package api

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// verifyMgmtAuth extracts and validates Ed25519 auth headers for management endpoints.
// The signed message is: "{METHOD}|{URL.Path}|{timestamp}"
// Returns (pubkeyHash, pubkeyRaw) on success.
func verifyMgmtAuth(r *http.Request) (string, []byte, error) {
	pubkeyB64 := r.Header.Get("X-Fialka-Pubkey")
	tsStr := r.Header.Get("X-Fialka-Timestamp")
	sigB64 := r.Header.Get("X-Fialka-Signature")

	if pubkeyB64 == "" || tsStr == "" || sigB64 == "" {
		return "", nil, fmt.Errorf("missing auth headers (X-Fialka-Pubkey, X-Fialka-Timestamp, X-Fialka-Signature)")
	}

	pubkeyRaw, err := base64.StdEncoding.DecodeString(pubkeyB64)
	if err != nil || len(pubkeyRaw) != ed25519.PublicKeySize {
		return "", nil, fmt.Errorf("invalid pubkey")
	}

	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return "", nil, fmt.Errorf("invalid timestamp")
	}
	now := time.Now().Unix()
	if ts < now-30 || ts > now+30 {
		return "", nil, fmt.Errorf("timestamp out of ±30s window")
	}

	sigRaw, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return "", nil, fmt.Errorf("invalid signature encoding")
	}

	// Signed message binds method + path + timestamp to prevent cross-endpoint replays.
	msg := []byte(r.Method + "|" + r.URL.Path + "|" + tsStr)
	if !ed25519.Verify(ed25519.PublicKey(pubkeyRaw), msg, sigRaw) {
		return "", nil, fmt.Errorf("invalid signature")
	}

	digest := sha256.Sum256(pubkeyRaw)
	hash := hex.EncodeToString(digest[:])
	return hash, pubkeyRaw, nil
}
