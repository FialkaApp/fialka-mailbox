package transport

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/fialkaapp/fialka-mailbox/internal/config"
	"github.com/fialkaapp/fialka-mailbox/internal/storage"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// Server handles incoming TorTransport binary connections on port 7333.
type Server struct {
	store *storage.SQLiteStore
	cfg   *config.Config
	log   zerolog.Logger
}

// New creates a new Server instance.
func New(store *storage.SQLiteStore, cfg *config.Config, log zerolog.Logger) *Server {
	return &Server{store: store, cfg: cfg, log: log}
}

// ListenAndServe binds to addr and serves connections until ctx is cancelled.
// addr should be "127.0.0.1:7333" — accessible only locally, Tor handles the .onion mapping.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	s.log.Info().Str("addr", addr).Msg("TorTransport TCP server listening")

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				s.log.Error().Err(err).Msg("accept error")
				continue
			}
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	// Per-connection read deadline (matches Android READ_TIMEOUT_MS = 60s)
	conn.SetDeadline(time.Now().Add(60 * time.Second)) //nolint:errcheck

	frame, err := ReadFrame(conn)
	if err != nil {
		s.log.Debug().Err(err).Str("remote", conn.RemoteAddr().String()).Msg("read frame error")
		return
	}

	var resp *Frame
	switch frame.Type {
	case TypePing:
		resp = AckOk()
	case TypeJoin:
		resp = s.handleJoin(frame.Payload)
	case TypeDeposit:
		resp = s.handleDeposit(frame.Payload)
	case TypeFetch:
		resp = s.handleFetch(frame.Payload)
	case TypeLeave:
		resp = s.handleLeave(frame.Payload)
	case TypeInviteReq:
		resp = s.handleInviteReq(frame.Payload)
	case TypeRevoke:
		resp = s.handleRevoke(frame.Payload)
	case TypeListMembers:
		resp = s.handleListMembers(frame.Payload)
	default:
		s.log.Warn().Uint8("type", frame.Type).Msg("unknown frame type")
		resp = AckError(fmt.Sprintf("unknown frame type: 0x%02X", frame.Type))
	}

	if resp != nil {
		conn.SetDeadline(time.Now().Add(30 * time.Second)) //nolint:errcheck
		if err := WriteFrame(conn, resp); err != nil {
			s.log.Debug().Err(err).Msg("write frame error")
		}
	}
}

// ── JOIN ──────────────────────────────────────────────────────────────────────
//
// Auth payload: commandTag="JOIN", extra=inviteCode_bytes (may be empty)
// Response:     TYPE_JOIN_RESP [JoinAccepted/JoinRejected][role/reason...]

func (s *Server) handleJoin(payload []byte) *Frame {
	pubkey, extra, err := VerifyAuthPayload("JOIN", payload)
	if err != nil {
		s.log.Warn().Err(err).Msg("JOIN auth failed")
		return rejectJoin(err.Error())
	}

	hash := pubkeyHash(pubkey)

	// Already a member? Return current role (idempotent).
	existing, lookupErr := s.store.GetMember(hash)
	if lookupErr == nil && existing != nil {
		roleB := RoleMember
		if existing.Role == "owner" {
			roleB = RoleOwner
		}
		s.log.Info().Str("hash", hash[:12]).Str("role", existing.Role).Msg("JOIN: already member")
		return acceptJoin(roleB)
	}

	hasOwner, err := s.store.HasOwner()
	if err != nil {
		return rejectJoin("internal error")
	}

	var role string
	if !hasOwner {
		// First joiner = OWNER, auto-accepted (no invite required)
		role = "owner"
	} else {
		// Subsequent joiners: require invite code
		inviteCode := ""
		if len(extra) > 0 {
			inviteCode = string(extra)
		}
		if inviteCode == "" {
			return rejectJoin("invite code required")
		}
		inv, err := s.store.ConsumeInvite(inviteCode)
		if err != nil {
			s.log.Warn().Err(err).Msg("JOIN: invalid invite")
			return rejectJoin(err.Error())
		}
		role = inv.Role
	}

	pubKeyB64 := base64.StdEncoding.EncodeToString(pubkey)
	displayName := pubKeyB64
	if len(displayName) > 16 {
		displayName = displayName[:16]
	}

	if err := s.store.AddMember(&storage.Member{
		PubkeyHash:  hash,
		Pubkey:      pubkey,
		Role:        role,
		DisplayName: displayName,
		JoinedAt:    time.Now().Unix(),
	}); err != nil {
		s.log.Error().Err(err).Msg("JOIN: add member failed")
		return rejectJoin("registration failed: " + err.Error())
	}

	roleB := RoleMember
	if role == "owner" {
		roleB = RoleOwner
	}
	s.log.Info().Str("hash", hash[:12]).Str("role", role).Msg("JOIN: accepted")
	return acceptJoin(roleB)
}

func acceptJoin(role byte) *Frame {
	return &Frame{Type: TypeJoinResp, Payload: []byte{JoinAccepted, role}}
}

func rejectJoin(reason string) *Frame {
	r := []byte(reason)
	payload := make([]byte, 1+len(r))
	payload[0] = JoinRejected
	copy(payload[1:], r)
	return &Frame{Type: TypeJoinResp, Payload: payload}
}

// ── DEPOSIT ───────────────────────────────────────────────────────────────────
//
// Auth payload: commandTag="DEPOSIT", extra=[recipientPub:32B][blob...]
// Sender must be a registered member.
// Recipient must be a registered member.
// Response: TYPE_ACK [AckOk]

func (s *Server) handleDeposit(payload []byte) *Frame {
	senderPub, extra, err := VerifyAuthPayload("DEPOSIT", payload)
	if err != nil {
		return AckError("auth failed: " + err.Error())
	}

	if len(extra) < 32 {
		return AckError("missing recipient pubkey")
	}

	recipientPub := extra[:32]
	blob := extra[32:]

	// Sender must be a member
	senderHash := pubkeyHash(senderPub)
	if ok, _ := s.store.IsMember(senderHash); !ok {
		return AckError("sender not a member")
	}

	// Recipient must be a member
	recipientHash := pubkeyHash(recipientPub)
	if ok, _ := s.store.IsMember(recipientHash); !ok {
		return AckError("recipient not a member")
	}

	// Enforce per-message size limit
	maxSize := s.cfg.Limits.MaxMessageSize
	if maxSize > 0 && int64(len(blob)) > maxSize {
		return AckError(fmt.Sprintf("blob too large (%d > %d bytes)", len(blob), maxSize))
	}

	// Enforce per-recipient message count
	if s.cfg.Limits.MaxMessagesPerRecipient > 0 {
		recipientB64 := base64.StdEncoding.EncodeToString(recipientPub)
		msgs, _ := s.store.Fetch(recipientB64)
		if len(msgs) >= s.cfg.Limits.MaxMessagesPerRecipient {
			return AckError("recipient mailbox full")
		}
	}

	ttlSecs := int64(s.cfg.Limits.MessageTTLHours) * 3600
	if ttlSecs <= 0 {
		ttlSecs = 7 * 24 * 3600 // 7 days default (matches Android BLOB_TTL_MS)
	}

	now := time.Now().Unix()
	if err := s.store.Deposit(&storage.Message{
		ID:         uuid.New().String(),
		Recipient:  base64.StdEncoding.EncodeToString(recipientPub),
		Sender:     base64.StdEncoding.EncodeToString(senderPub),
		Payload:    blob,
		SizeBytes:  int64(len(blob)),
		ReceivedAt: now,
		ExpiresAt:  now + ttlSecs,
	}); err != nil {
		s.log.Error().Err(err).Msg("DEPOSIT: store failed")
		return AckError("deposit failed")
	}

	s.log.Debug().
		Str("sender", senderHash[:12]).
		Str("recipient", recipientHash[:12]).
		Int("size", len(blob)).
		Msg("DEPOSIT: ok")
	return AckOk()
}

// ── FETCH ─────────────────────────────────────────────────────────────────────
//
// Auth payload: commandTag="FETCH", extra=empty
// Response: TYPE_FETCH_RESP
//   [count:2B] for each: [idLen:2B][id_bytes][senderPub:32B][depositedAt_ms:8B][blobLen:4B][blob]
//
// Blobs are deleted server-side immediately after fetch (delivered = gone).

func (s *Server) handleFetch(payload []byte) *Frame {
	requesterPub, _, err := VerifyAuthPayload("FETCH", payload)
	if err != nil {
		return AckError("auth failed: " + err.Error())
	}

	requesterHash := pubkeyHash(requesterPub)
	if ok, _ := s.store.IsMember(requesterHash); !ok {
		return AckError("not a member")
	}

	requesterB64 := base64.StdEncoding.EncodeToString(requesterPub)
	msgs, err := s.store.Fetch(requesterB64)
	if err != nil {
		s.log.Error().Err(err).Msg("FETCH: query failed")
		return AckError("fetch failed")
	}

	// Build FETCH_RESP:
	// [count:2B] for each blob: [idLen:2B][id][senderPub:32B][depositedAt_ms:8B][blobLen:4B][blob]
	totalSize := 2
	for _, m := range msgs {
		totalSize += 2 + len(m.ID) + 32 + 8 + 4 + len(m.Payload)
	}

	buf := make([]byte, totalSize)
	offset := 0
	binary.BigEndian.PutUint16(buf[offset:], uint16(len(msgs)))
	offset += 2

	for _, m := range msgs {
		idBytes := []byte(m.ID)
		binary.BigEndian.PutUint16(buf[offset:], uint16(len(idBytes)))
		offset += 2
		copy(buf[offset:], idBytes)
		offset += len(idBytes)

		// Sender pubkey: raw 32 bytes
		senderPub, _ := base64.StdEncoding.DecodeString(m.Sender)
		if len(senderPub) < 32 {
			senderPub = make([]byte, 32) // zero-padded if unknown
		}
		copy(buf[offset:], senderPub[:32])
		offset += 32

		// depositedAt in milliseconds (Android uses System.currentTimeMillis())
		binary.BigEndian.PutUint64(buf[offset:], uint64(m.ReceivedAt*1000))
		offset += 8

		binary.BigEndian.PutUint32(buf[offset:], uint32(len(m.Payload)))
		offset += 4
		copy(buf[offset:], m.Payload)
		offset += len(m.Payload)

		// Delete immediately after including in response (firebase delivered = gone)
		_ = s.store.Delete(m.ID)
	}

	s.log.Debug().Str("requester", requesterHash[:12]).Int("count", len(msgs)).Msg("FETCH: ok")
	return &Frame{Type: TypeFetchResp, Payload: buf[:offset]}
}

// ── LEAVE ─────────────────────────────────────────────────────────────────────
//
// Auth payload: commandTag="LEAVE", extra=empty
// Owner cannot leave remotely (must reset from device).
// Pending blobs are purged on leave.

func (s *Server) handleLeave(payload []byte) *Frame {
	requesterPub, _, err := VerifyAuthPayload("LEAVE", payload)
	if err != nil {
		return AckError("auth failed: " + err.Error())
	}

	hash := pubkeyHash(requesterPub)
	m, err := s.store.GetMember(hash)
	if err != nil {
		return AckError("not a member")
	}
	if m.Role == "owner" {
		return AckError("owner cannot leave remotely")
	}

	// Purge pending blobs
	requesterB64 := base64.StdEncoding.EncodeToString(requesterPub)
	if pending, err := s.store.Fetch(requesterB64); err == nil {
		for _, msg := range pending {
			_ = s.store.Delete(msg.ID)
		}
	}

	if err := s.store.RemoveMember(hash); err != nil {
		return AckError("leave failed: " + err.Error())
	}

	s.log.Info().Str("hash", hash[:12]).Msg("LEAVE: member removed")
	return AckOk()
}

// ── INVITE_REQUEST ────────────────────────────────────────────────────────────
//
// Auth payload: commandTag="INVITE_REQUEST", extra=empty (OWNER only)
// Response: TYPE_INVITE_RESP [codeLen:2B][code_bytes]
//
// Creates a single-use, 24-hour invite (role=member).

func (s *Server) handleInviteReq(payload []byte) *Frame {
	requesterPub, _, err := VerifyAuthPayload("INVITE_REQUEST", payload)
	if err != nil {
		return AckError("auth failed: " + err.Error())
	}

	hash := pubkeyHash(requesterPub)
	if ok, _ := s.store.IsOwner(hash); !ok {
		return AckError("not the owner")
	}

	token, err := storage.GenerateToken()
	if err != nil {
		return AckError("token generation failed")
	}

	// 24-hour invite (matches Android INVITE_TTL_MS = 24h)
	expiresAt := time.Now().Unix() + 24*3600
	inv := &storage.Invite{
		Token:     token,
		Role:      "member",
		CreatedBy: hash,
		CreatedAt: time.Now().Unix(),
		ExpiresAt: expiresAt,
		MaxUses:   1,
	}
	if err := s.store.CreateInvite(inv); err != nil {
		return AckError("create invite failed")
	}

	codeBytes := []byte(token)
	respPayload := make([]byte, 2+len(codeBytes))
	binary.BigEndian.PutUint16(respPayload, uint16(len(codeBytes)))
	copy(respPayload[2:], codeBytes)

	s.log.Info().Str("owner", hash[:12]).Msg("INVITE_REQUEST: invite created")
	return &Frame{Type: TypeInviteResp, Payload: respPayload}
}

// ── REVOKE ────────────────────────────────────────────────────────────────────
//
// Auth payload: commandTag="REVOKE_MEMBER", extra=targetPubKey:32B (OWNER only)
// Response: TYPE_ACK [AckOk]

func (s *Server) handleRevoke(payload []byte) *Frame {
	requesterPub, extra, err := VerifyAuthPayload("REVOKE_MEMBER", payload)
	if err != nil {
		return AckError("auth failed: " + err.Error())
	}

	requesterHash := pubkeyHash(requesterPub)
	if ok, _ := s.store.IsOwner(requesterHash); !ok {
		return AckError("not the owner")
	}

	if len(extra) < 32 {
		return AckError("missing target pubkey")
	}

	targetHash := pubkeyHash(extra[:32])
	if err := s.store.RemoveMember(targetHash); err != nil {
		return AckError("revoke failed: " + err.Error())
	}

	s.log.Info().Str("target", targetHash[:12]).Msg("REVOKE: member removed")
	return AckOk()
}

// ── LIST_MEMBERS ──────────────────────────────────────────────────────────────
//
// Auth payload: commandTag="LIST_MEMBERS", extra=empty (OWNER only)
// Response: TYPE_MEMBER_LIST
//   [count:2B] for each: [pub:32B][role:1B][joinedAt_ms:8B]

func (s *Server) handleListMembers(payload []byte) *Frame {
	requesterPub, _, err := VerifyAuthPayload("LIST_MEMBERS", payload)
	if err != nil {
		return AckError("auth failed: " + err.Error())
	}

	hash := pubkeyHash(requesterPub)
	if ok, _ := s.store.IsOwner(hash); !ok {
		return AckError("not the owner")
	}

	members, err := s.store.ListMembers()
	if err != nil {
		return AckError("list failed")
	}

	// [count:2B] for each: [pub:32B][role:1B][joinedAt_ms:8B]
	totalSize := 2 + len(members)*(32+1+8)
	buf := make([]byte, totalSize)
	offset := 0

	binary.BigEndian.PutUint16(buf[offset:], uint16(len(members)))
	offset += 2

	for _, m := range members {
		pub := m.Pubkey
		if len(pub) < 32 {
			pub = make([]byte, 32)
		}
		copy(buf[offset:], pub[:32])
		offset += 32

		roleByte := RoleMember
		if m.Role == "owner" {
			roleByte = RoleOwner
		}
		buf[offset] = roleByte
		offset++

		// joinedAt in milliseconds (matches Android format)
		binary.BigEndian.PutUint64(buf[offset:], uint64(m.JoinedAt*1000))
		offset += 8
	}

	return &Frame{Type: TypeMemberList, Payload: buf[:offset]}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// pubkeyHash returns a hex-encoded SHA-256 of the raw Ed25519 pubkey bytes.
func pubkeyHash(pubkey []byte) string {
	h := sha256.Sum256(pubkey)
	return fmt.Sprintf("%x", h)
}
