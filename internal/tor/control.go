package tor

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Controller manages a connection to the Tor daemon's control port.
type Controller struct {
	conn    net.Conn
	reader  *bufio.Reader
	dataDir string

	OnionAddress string // populated after CreateHiddenService
	ServiceID    string // the 56-char base32 part
	OnionPrivKey string // base64-encoded private key (for persistence)
}

// Connect dials the Tor control port and authenticates.
func Connect(network, addr, password, dataDir string, cookieAuth bool) (*Controller, error) {
	conn, err := net.DialTimeout(network, addr, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dialling tor control port %s: %w", addr, err)
	}

	c := &Controller{
		conn:    conn,
		reader:  bufio.NewReader(conn),
		dataDir: dataDir,
	}

	if err := c.authenticate(password, dataDir, cookieAuth); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tor auth: %w", err)
	}

	return c, nil
}

func (c *Controller) authenticate(password, dataDir string, cookieAuth bool) error {
	if cookieAuth {
		// Read cookie file path from GETINFO then read the cookie
		cookiePath, err := c.getCookiePath()
		if err != nil {
			// Fall back to password auth if cookie path can't be determined
			if password != "" {
				return c.sendCommand(fmt.Sprintf("AUTHENTICATE \"%s\"", password), "250")
			}
			return c.sendCommand("AUTHENTICATE", "250")
		}
		cookie, err := os.ReadFile(cookiePath)
		if err != nil {
			return fmt.Errorf("reading cookie file %s: %w", cookiePath, err)
		}
		hexCookie := fmt.Sprintf("%x", cookie)
		return c.sendCommand("AUTHENTICATE "+hexCookie, "250")
	}

	if password != "" {
		return c.sendCommand(fmt.Sprintf("AUTHENTICATE \"%s\"", password), "250")
	}
	return c.sendCommand("AUTHENTICATE", "250")
}

func (c *Controller) getCookiePath() (string, error) {
	if err := c.write("GETINFO config-file\r\n"); err != nil {
		return "", err
	}
	// Read lines until 250 OK
	var torrcPath string
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "250-config-file=") {
			torrcPath = strings.TrimPrefix(line, "250-config-file=")
		}
		if strings.HasPrefix(line, "250 ") {
			break
		}
		if strings.HasPrefix(line, "5") {
			return "", fmt.Errorf("tor error: %s", line)
		}
	}

	if torrcPath != "" {
		cookiePath := filepath.Join(filepath.Dir(torrcPath), "control_auth_cookie")
		if _, err := os.Stat(cookiePath); err == nil {
			return cookiePath, nil
		}
	}

	// Common fallback locations
	candidates := []string{
		"/run/tor/control.authcookie",
		"/var/run/tor/control.authcookie",
		"/var/lib/tor/control_auth_cookie",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("cookie file not found")
}

// CreateHiddenService creates (or restores) a v3 Tor hidden service.
//
// privKey is the pre-resolved Ed25519 private key (base64 string as returned
// by Tor's ADD_ONION response), or "" to request a freshly generated key.
// The caller is responsible for loading/decrypting the key before calling
// this function, and for persisting c.OnionPrivKey if a new key was generated.
func (c *Controller) CreateHiddenService(targetPort int, listenAddr string, privKey string) error {
	keyArg := "NEW:ED25519-V3"
	isNew := true
	if strings.TrimSpace(privKey) != "" {
		keyArg = "ED25519-V3:" + strings.TrimSpace(privKey)
		isNew = false
	}

	cmd := fmt.Sprintf("ADD_ONION %s Port=%d,%s", keyArg, targetPort, listenAddr)
	if err := c.write(cmd + "\r\n"); err != nil {
		return err
	}

	var serviceID, privateKey string
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading ADD_ONION response: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")

		if strings.HasPrefix(line, "250-ServiceID=") {
			serviceID = strings.TrimPrefix(line, "250-ServiceID=")
		}
		if strings.HasPrefix(line, "250-PrivateKey=") {
			parts := strings.SplitN(strings.TrimPrefix(line, "250-PrivateKey="), ":", 2)
			if len(parts) == 2 {
				privateKey = parts[1]
			}
		}
		if strings.HasPrefix(line, "250 ") {
			break
		}
		if strings.HasPrefix(line, "5") || strings.HasPrefix(line, "4") {
			return fmt.Errorf("ADD_ONION error: %s", line)
		}
	}

	if serviceID == "" {
		return fmt.Errorf("no ServiceID in ADD_ONION response")
	}

	c.ServiceID = serviceID
	c.OnionAddress = serviceID + ".onion"

	// Expose newly generated key so caller can persist with the right scheme.
	if isNew && privateKey != "" {
		c.OnionPrivKey = privateKey
	}

	return nil
}

// GetInfo retrieves a Tor info key (e.g. "version").
func (c *Controller) GetInfo(key string) (string, error) {
	if err := c.write(fmt.Sprintf("GETINFO %s\r\n", key)); err != nil {
		return "", err
	}
	var value string
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		prefix := fmt.Sprintf("250-%s=", key)
		if strings.HasPrefix(line, prefix) {
			value = strings.TrimPrefix(line, prefix)
		}
		if strings.HasPrefix(line, "250 ") {
			break
		}
		if strings.HasPrefix(line, "5") || strings.HasPrefix(line, "4") {
			return "", fmt.Errorf("GETINFO error: %s", line)
		}
	}
	return value, nil
}

// Close sends QUIT and closes the control connection.
func (c *Controller) Close() error {
	_ = c.write("QUIT\r\n")
	return c.conn.Close()
}

func (c *Controller) sendCommand(cmd, expectedPrefix string) error {
	if err := c.write(cmd + "\r\n"); err != nil {
		return err
	}
	line, err := c.reader.ReadString('\n')
	if err != nil {
		return err
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, expectedPrefix) {
		return fmt.Errorf("unexpected response: %q", line)
	}
	return nil
}

func (c *Controller) write(s string) error {
	_, err := fmt.Fprint(c.conn, s)
	return err
}

// OnionAddressQR returns a simple ASCII representation of the .onion address
// suitable for terminal display (not an actual QR code — use a QR lib for that).
func (c *Controller) OnionAddressQR() string {
	return fmt.Sprintf("┌─────────────────────────────────────────────────────────┐\n│  %s  │\n└─────────────────────────────────────────────────────────┘", c.OnionAddress)
}

// EncodeKey encodes raw bytes as base64 (used for key display/debug).
func EncodeKey(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}
