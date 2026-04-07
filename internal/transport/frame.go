package transport

import (
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// ── Frame type constants — must match Android TorTransport exactly ────────────
const (
	TypeMessage     byte = 0x01
	TypeFileChunk   byte = 0x02
	TypeKeyBundle   byte = 0x03
	TypeAck         byte = 0x04
	TypePing        byte = 0x05
	TypeContactReq  byte = 0x06
	TypeDeposit     byte = 0x07
	TypeFetch       byte = 0x08
	TypeJoin        byte = 0x09
	TypeLeave       byte = 0x0A
	TypeInviteReq   byte = 0x0B
	TypeRevoke      byte = 0x0C
	TypeListMembers byte = 0x0D
	TypeJoinResp    byte = 0x0E
	TypeInviteResp  byte = 0x0F
	TypeMemberList  byte = 0x10
	TypeFetchResp   byte = 0x11
	TypeError       byte = 0x12
	TypePresence    byte = 0x13
)

// ── Status codes ─────────────────────────────────────────────────────────────
const (
	AckOkByte    byte = 0x00
	AckErrorByte byte = 0x01
	JoinAccepted byte = 0x00
	JoinRejected byte = 0x01
	RoleOwner    byte = 0x00
	RoleMember   byte = 0x01
)

const (
	magicHigh      byte  = 0xF1
	magicLow       byte  = 0xA1
	maxPayloadSize int   = 10 * 1024 * 1024 // 10 MB
	authHeaderSize int   = 32 + 8 + 16 + 64 // pubkey + timestamp + nonce + sig = 120 bytes
	timestampWindowMs int64 = 30 * 60 * 1000 // ±30 minutes in ms (matches Android TIMESTAMP_WINDOW_MS)
)

// Frame is a single TorTransport protocol message.
type Frame struct {
	Type    byte
	Payload []byte
}

// ReadFrame reads exactly one frame from conn.
// Wire format: [0xF1][0xA1][type:1B][length:4B BE][payload:N bytes]
func ReadFrame(conn net.Conn) (*Frame, error) {
	header := make([]byte, 7)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	if header[0] != magicHigh || header[1] != magicLow {
		return nil, fmt.Errorf("invalid magic: 0x%02X 0x%02X", header[0], header[1])
	}
	typ := header[2]
	length := int(binary.BigEndian.Uint32(header[3:7]))
	if length < 0 || length > maxPayloadSize {
		return nil, fmt.Errorf("invalid payload length: %d", length)
	}
	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(conn, payload); err != nil {
			return nil, err
		}
	}
	return &Frame{Type: typ, Payload: payload}, nil
}

// WriteFrame writes a frame to conn.
func WriteFrame(conn net.Conn, f *Frame) error {
	buf := make([]byte, 7+len(f.Payload))
	buf[0] = magicHigh
	buf[1] = magicLow
	buf[2] = f.Type
	binary.BigEndian.PutUint32(buf[3:7], uint32(len(f.Payload)))
	copy(buf[7:], f.Payload)
	_, err := conn.Write(buf)
	return err
}

// AckOk returns an ACK frame with status OK.
func AckOk() *Frame {
	return &Frame{Type: TypeAck, Payload: []byte{AckOkByte}}
}

// AckError returns an ACK frame with status error + message.
func AckError(msg string) *Frame {
	msgB := []byte(msg)
	payload := make([]byte, 1+len(msgB))
	payload[0] = AckErrorByte
	copy(payload[1:], msgB)
	return &Frame{Type: TypeAck, Payload: payload}
}

// ErrorFrame returns a TYPE_ERROR frame with a message.
func ErrorFrame(msg string) *Frame {
	return &Frame{Type: TypeError, Payload: []byte(msg)}
}

// ── Auth payload ──────────────────────────────────────────────────────────────
//
// Format: [pubkey:32B][timestamp:8B BE ms][nonce:16B][signature:64B][extra...]
//
// Signature covers (big-endian, matching Android TorTransport.buildSigningData):
//   commandTag_bytes || timestamp_8B_BE || nonce_16B || extra_bytes
//
// Timestamp is System.currentTimeMillis() on Android — milliseconds since epoch.

// VerifyAuthPayload parses and Ed25519-verifies an authenticated payload.
// Returns (pubkey_raw_32B, extra, nil) on success.
func VerifyAuthPayload(commandTag string, payload []byte) (pubkey []byte, extra []byte, err error) {
	if len(payload) < authHeaderSize {
		return nil, nil, fmt.Errorf("payload too short: %d < %d bytes", len(payload), authHeaderSize)
	}

	pubkey = payload[0:32]
	timestampBytes := payload[32:40]
	nonce := payload[40:56]
	sig := payload[56:120]
	extra = payload[120:]

	// Verify timestamp window (timestamps are in milliseconds)
	timestamp := int64(binary.BigEndian.Uint64(timestampBytes))
	nowMs := time.Now().UnixMilli()
	diff := nowMs - timestamp
	if diff < 0 {
		diff = -diff
	}
	if diff > timestampWindowMs {
		return nil, nil, fmt.Errorf("timestamp out of window (%dms off)", diff)
	}

	// Check nonce replay (prevents replayed signed requests)
	nonceKey := fmt.Sprintf("%x", nonce)
	if !checkAndStoreNonce(nonceKey, timestamp) {
		return nil, nil, fmt.Errorf("nonce replay detected")
	}

	// Build data that was signed: commandTag || timestamp(8B) || nonce(16B) || extra
	tagBytes := []byte(commandTag)
	toVerify := make([]byte, len(tagBytes)+8+16+len(extra))
	offset := 0
	copy(toVerify[offset:], tagBytes)
	offset += len(tagBytes)
	copy(toVerify[offset:], timestampBytes)
	offset += 8
	copy(toVerify[offset:], nonce)
	offset += 16
	copy(toVerify[offset:], extra)

	// Verify Ed25519 signature
	if !ed25519.Verify(ed25519.PublicKey(pubkey), toVerify, sig) {
		return nil, nil, fmt.Errorf("Ed25519 signature invalid")
	}

	return pubkey, extra, nil
}

// ── Nonce replay protection ───────────────────────────────────────────────────

type nonceEntry struct {
	expiresAtMs int64
}

var (
	nonceMu    sync.Mutex
	nonceCache = make(map[string]nonceEntry, 512)
)

// checkAndStoreNonce returns true if fresh (not seen before), false if replay.
// Automatically prunes expired entries to bound memory usage.
func checkAndStoreNonce(key string, timestampMs int64) bool {
	nonceMu.Lock()
	defer nonceMu.Unlock()

	now := time.Now().UnixMilli()

	// Prune expired nonces (keep map bounded)
	for k, v := range nonceCache {
		if v.expiresAtMs < now {
			delete(nonceCache, k)
		}
	}

	if _, found := nonceCache[key]; found {
		return false // replay
	}

	// Keep nonce for window + 1 min safety buffer
	nonceCache[key] = nonceEntry{expiresAtMs: now + timestampWindowMs + 60_000}
	return true
}
