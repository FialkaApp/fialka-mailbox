package tor

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/argon2"
)

// keyEncMagic is the file signature for an encrypted onion key file.
var keyEncMagic = [4]byte{'F', 'K', 'E', 'Y'}

const (
	keyEncVersion = byte(0x01)

	// Argon2id parameters — calibrated for ~1-3s on a low-end server.
	// Memory intentionally high to resist offline brute-force from attackers
	// with stolen disk images.
	argon2Time    = 3
	argon2Memory  = 64 * 1024 // 64 MiB
	argon2Threads = 4
	argon2KeyLen  = 32

	// EncryptedKeyFile is the filename used for passphrase-protected keys.
	EncryptedKeyFile = "onion.key.enc"
)

// IsKeyEncrypted returns true if a passphrase-encrypted key file exists in dataDir.
func IsKeyEncrypted(dataDir string) bool {
	_, err := os.Stat(filepath.Join(dataDir, EncryptedKeyFile))
	return err == nil
}

// EncryptAndSaveKey encrypts privKey (raw base64 string from Tor) with the
// given passphrase using argon2id + AES-256-GCM, then writes it to
// dataDir/onion.key.enc (mode 0600).
//
// Wire format:
//
//	[4]  magic "FKEY"
//	[1]  version 0x01
//	[16] argon2id salt (random)
//	[12] AES-GCM nonce (random)
//	[N]  AES-256-GCM ciphertext + 16-byte auth tag
func EncryptAndSaveKey(dataDir string, privKey string, passphrase []byte) error {
	if len(passphrase) == 0 {
		return errors.New("passphrase must not be empty")
	}
	if strings.TrimSpace(privKey) == "" {
		return errors.New("private key must not be empty")
	}

	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return fmt.Errorf("generating salt: %w", err)
	}

	dk := argon2.IDKey(passphrase, salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	defer zeroBytes(dk)

	block, err := aes.NewCipher(dk)
	if err != nil {
		return fmt.Errorf("creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("creating GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize()) // 12 bytes
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("generating nonce: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, []byte(privKey), nil)

	out := make([]byte, 0, 4+1+16+len(nonce)+len(ciphertext))
	out = append(out, keyEncMagic[:]...)
	out = append(out, keyEncVersion)
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)

	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return fmt.Errorf("creating key dir: %w", err)
	}
	return os.WriteFile(filepath.Join(dataDir, EncryptedKeyFile), out, 0600)
}

// DecryptKey reads and decrypts dataDir/onion.key.enc using the given passphrase.
// Returns the plaintext Ed25519 key (base64 string as expected by Tor ADD_ONION).
// The returned string should be zeroed by the caller after use.
func DecryptKey(dataDir string, passphrase []byte) (string, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, EncryptedKeyFile))
	if err != nil {
		return "", fmt.Errorf("reading encrypted key: %w", err)
	}

	// Minimum size: 4 (magic) + 1 (version) + 16 (salt) + 12 (nonce) + 16 (GCM tag) + 1 (at least 1 byte plaintext)
	if len(data) < 4+1+16+12+16+1 {
		return "", errors.New("key file too short or corrupted")
	}

	var magic [4]byte
	copy(magic[:], data[:4])
	if magic != keyEncMagic {
		return "", errors.New("not a Fialka encrypted key file (bad magic bytes)")
	}
	if data[4] != keyEncVersion {
		return "", fmt.Errorf("unsupported key file version: 0x%02x", data[4])
	}

	offset := 5
	salt := data[offset : offset+16]
	offset += 16

	dk := argon2.IDKey(passphrase, salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	defer zeroBytes(dk)

	block, err := aes.NewCipher(dk)
	if err != nil {
		return "", fmt.Errorf("cipher init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("GCM init: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < offset+nonceSize {
		return "", errors.New("key file truncated (missing nonce)")
	}
	nonce := data[offset : offset+nonceSize]
	offset += nonceSize

	pt, err := gcm.Open(nil, nonce, data[offset:], nil)
	if err != nil {
		// Intentionally opaque — don't distinguish wrong passphrase from corrupted file.
		return "", errors.New("decryption failed: wrong passphrase or corrupted key file")
	}

	result := string(pt)
	zeroBytes(pt)
	return result, nil
}

// LoadPlaintextKey loads the legacy plaintext key from dataDir/onion.key.
// Returns ("", nil) if the file does not exist.
func LoadPlaintextKey(dataDir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, "onion.key"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading onion.key: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// SavePlaintextKey writes privKey to dataDir/onion.key (mode 0600).
func SavePlaintextKey(dataDir string, privKey string) error {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, "onion.key"), []byte(privKey), 0600)
}

// zeroBytes overwrites a byte slice with zeroes to limit key material in memory.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
