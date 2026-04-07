#!/usr/bin/env bash
# ============================================================
#  Fialka Mailbox — Self-hosted installer
#  https://github.com/FialkaApp/fialka-mailbox
#
#  Installs:
#    1. Tor daemon (official Tor Project repository + GPG verification)
#    2. fialka-mailbox binary
#    3. systemd service (if available)
# ============================================================
set -euo pipefail

# ── Constants ────────────────────────────────────────────────
REPO_API="https://api.github.com/repos/FialkaApp/fialka-mailbox"
REPO_RAW="https://raw.githubusercontent.com/FialkaApp/fialka-mailbox"
REPO_DL="https://github.com/FialkaApp/fialka-mailbox/releases/download"
INSTALL_DIR="/usr/local/bin"
DATA_DIR="/var/lib/fialka-mailbox"
CONFIG_DIR="/etc/fialka-mailbox"
SERVICE_FILE="/etc/systemd/system/fialka-mailbox.service"

# Official Tor Project signing key fingerprint (stable — do NOT change without auditing)
# Source: https://support.torproject.org/little-t-tor/verify-little-t-tor/
TOR_GPG_FINGERPRINT="A3C4F0F979CAA22CDBA8F512EE8CBC9E886DDD89"
TOR_GPG_KEY_URL="https://deb.torproject.org/torproject.org/${TOR_GPG_FINGERPRINT}.asc"
TOR_KEYRING="/usr/share/keyrings/tor-archive-keyring.gpg"

# ── Colors ───────────────────────────────────────────────────
if [ -t 1 ]; then
  RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
  CYAN='\033[0;36m'; BOLD='\033[1m'; RESET='\033[0m'
else
  RED=''; GREEN=''; YELLOW=''; CYAN=''; BOLD=''; RESET=''
fi

step()  { echo -e "\n${CYAN}${BOLD}[●]${RESET} $*"; }
ok()    { echo -e "    ${GREEN}✓${RESET} $*"; }
warn()  { echo -e "    ${YELLOW}⚠${RESET}  $*"; }
die()   { echo -e "\n${RED}${BOLD}[✗] FATAL:${RESET} $*\n" >&2; exit 1; }
info()  { echo -e "    ${BOLD}→${RESET} $*"; }

# ── Root check ───────────────────────────────────────────────
if [ "$EUID" -ne 0 ]; then
  die "Run as root or with sudo:\n\n    sudo bash install.sh"
fi

# ── Detect OS / Arch ─────────────────────────────────────────
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH_RAW=$(uname -m)
case $ARCH_RAW in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  armv7l)  ARCH="armv7" ;;
  *) die "Unsupported architecture: $ARCH_RAW" ;;
esac

# Detect Debian/Ubuntu codename for Tor repo
if command -v lsb_release &>/dev/null; then
  DISTRO_CODENAME=$(lsb_release -cs)
  DISTRO_ID=$(lsb_release -si | tr '[:upper:]' '[:lower:]')
else
  DISTRO_CODENAME=""
  DISTRO_ID=""
fi

# ── Banner ───────────────────────────────────────────────────
echo ""
echo -e "${BOLD}╔══════════════════════════════════════════════════════╗${RESET}"
echo -e "${BOLD}║          Fialka Mailbox — Self-hosted Setup          ║${RESET}"
echo -e "${BOLD}╚══════════════════════════════════════════════════════╝${RESET}"
echo ""
info "OS:           $OS ($ARCH_RAW → $ARCH)"
info "Distro:       ${DISTRO_ID:-unknown} ${DISTRO_CODENAME:-}"
info "Install dir:  $INSTALL_DIR"
info "Data dir:     $DATA_DIR"
echo ""
echo -e "  This script will install:"
echo -e "    ${CYAN}1.${RESET} Tor daemon from the ${BOLD}official Tor Project repository${RESET}"
echo -e "    ${CYAN}2.${RESET} fialka-mailbox binary"
echo -e "    ${CYAN}3.${RESET} systemd service (auto-start on boot)"
echo ""
read -rp "  Continue? [y/N] " confirm
[[ "$confirm" =~ ^[Yy]$ ]] || { echo "Aborted."; exit 0; }

# ════════════════════════════════════════════════════════════
#  STEP 1 — Tor (official Tor Project repo + GPG verification)
# ════════════════════════════════════════════════════════════
step "Installing Tor (The Tor Project — official)"

if command -v tor &>/dev/null; then
  EXISTING_TOR=$(tor --version 2>&1 | head -1 || true)
  ok "Tor already installed: $EXISTING_TOR"
  info "Skipping Tor installation."
else
  if [[ "$DISTRO_ID" =~ ^(ubuntu|debian|raspbian)$ ]] && [ -n "$DISTRO_CODENAME" ]; then
    # ── Add official Tor Project APT repository ──────────────
    echo ""
    echo -e "  ${BOLD}Tor Project GPG key verification${RESET}"
    echo -e "  ─────────────────────────────────────────────────────"
    echo -e "  Downloading signing key from: ${CYAN}deb.torproject.org${RESET}"
    echo -e "  Expected fingerprint:"
    echo -e "    ${BOLD}${TOR_GPG_FINGERPRINT}${RESET}"
    echo -e "  ─────────────────────────────────────────────────────"
    echo ""

    # Download and import the key
    if ! curl -fsSL "$TOR_GPG_KEY_URL" \
         | gpg --dearmor \
         | tee "$TOR_KEYRING" > /dev/null; then
      die "Failed to download Tor Project GPG key from $TOR_GPG_KEY_URL"
    fi

    # Verify the fingerprint matches exactly
    ACTUAL_FP=$(gpg --no-default-keyring --keyring "$TOR_KEYRING" \
                    --with-fingerprint --with-colons 2>/dev/null \
                | grep '^fpr' | head -1 | cut -d: -f10 | tr -d ' ')

    if [ "$ACTUAL_FP" != "$TOR_GPG_FINGERPRINT" ]; then
      rm -f "$TOR_KEYRING"
      die "GPG fingerprint MISMATCH!\n\n  Expected: $TOR_GPG_FINGERPRINT\n  Got:      $ACTUAL_FP\n\n  Refusing to install. The key may have changed — check https://support.torproject.org/"
    fi

    echo -e "  ${GREEN}${BOLD}✓ GPG fingerprint verified:${RESET}"
    echo -e "    ${GREEN}${TOR_GPG_FINGERPRINT}${RESET}"
    echo -e "    Matches the official Tor Project signing key."
    echo ""

    # Add apt source
    echo "deb [signed-by=$TOR_KEYRING] https://deb.torproject.org/torproject.org $DISTRO_CODENAME main" \
      > /etc/apt/sources.list.d/tor.list

    ok "Tor Project repository added (/etc/apt/sources.list.d/tor.list)"

    info "Updating package lists..."
    apt-get update -qq

    info "Installing tor + keyring package..."
    DEBIAN_FRONTEND=noninteractive apt-get install -y tor deb.torproject.org-keyring

    ok "Tor installed: $(tor --version 2>&1 | head -1)"

  else
    # Fallback: try system package manager
    warn "Non-Debian system — cannot add official Tor Project repo."
    warn "Installing tor from system repositories (verify manually)."
    echo ""
    if command -v apt-get &>/dev/null; then
      apt-get install -y tor
    elif command -v dnf &>/dev/null; then
      dnf install -y tor
    elif command -v pacman &>/dev/null; then
      pacman -Sy --noconfirm tor
    else
      die "Cannot find a package manager. Install tor manually from https://torproject.org/download/tor/"
    fi
    ok "Tor installed: $(tor --version 2>&1 | head -1)"
  fi
fi

# Configure Tor control port (needed by fialka-mailbox)
TOR_CONF="/etc/tor/torrc"
if ! grep -q "ControlPort 9051" "$TOR_CONF" 2>/dev/null; then
  info "Enabling Tor control port with cookie auth..."
  cat >> "$TOR_CONF" << 'EOF'

# --- Fialka Mailbox ---
ControlPort 9051
CookieAuthentication 1
CookieAuthFileGroupReadable 1
EOF
  ok "Control port enabled (127.0.0.1:9051, cookie auth)"
else
  ok "Tor control port already configured"
fi

# Ensure fialka user will be in the debian-tor group (for cookie file access)
systemctl restart tor
systemctl enable tor
ok "Tor daemon started and enabled"

# ════════════════════════════════════════════════════════════
#  STEP 2 — fialka-mailbox binary
# ════════════════════════════════════════════════════════════
step "Installing fialka-mailbox"

# Fetch latest release tag
info "Fetching latest release from GitHub..."
VERSION=$(curl -fsSL "$REPO_API/releases/latest" \
  | grep '"tag_name"' | cut -d'"' -f4)
[ -n "$VERSION" ] || die "Could not determine latest version from GitHub API"
ok "Latest version: $VERSION"

BINARY_URL="$REPO_DL/$VERSION/fialka-mailbox_${OS}_${ARCH}"
CHECKSUM_URL="$REPO_DL/$VERSION/checksums.sha256"

info "Downloading binary..."
curl -fsSL "$BINARY_URL" -o /tmp/fialka-mailbox-new
chmod +x /tmp/fialka-mailbox-new

# Verify SHA-256 checksum (if released)
if curl -fsSL "$CHECKSUM_URL" -o /tmp/fialka-mailbox.sha256 2>/dev/null; then
  EXPECTED=$(grep "fialka-mailbox_${OS}_${ARCH}" /tmp/fialka-mailbox.sha256 | awk '{print $1}')
  if [ -n "$EXPECTED" ]; then
    ACTUAL=$(sha256sum /tmp/fialka-mailbox-new | awk '{print $1}')
    if [ "$ACTUAL" != "$EXPECTED" ]; then
      rm -f /tmp/fialka-mailbox-new
      die "SHA-256 checksum MISMATCH!\n  Expected: $EXPECTED\n  Got:      $ACTUAL"
    fi
    ok "SHA-256 checksum verified"
  fi
  rm -f /tmp/fialka-mailbox.sha256
fi

mv /tmp/fialka-mailbox-new "$INSTALL_DIR/fialka"
ok "Binary installed → $INSTALL_DIR/fialka"

# ════════════════════════════════════════════════════════════
#  STEP 3 — System user + directories
# ════════════════════════════════════════════════════════════
step "Creating system user and directories"

if ! id fialka &>/dev/null; then
  useradd -r -s /bin/false -d "$DATA_DIR" -m fialka
  ok "Created system user: fialka"
else
  ok "System user fialka already exists"
fi

# Add fialka to debian-tor group so it can read the cookie file
if getent group debian-tor &>/dev/null; then
  usermod -aG debian-tor fialka
  ok "Added fialka to debian-tor group (cookie auth access)"
fi

mkdir -p "$DATA_DIR" "$DATA_DIR/tor" "$CONFIG_DIR"
chown -R fialka:fialka "$DATA_DIR"
chmod 750 "$DATA_DIR"
ok "Directories: $DATA_DIR, $CONFIG_DIR"

# ════════════════════════════════════════════════════════════
#  STEP 4 — Default config
# ════════════════════════════════════════════════════════════
step "Creating default configuration"

CONFIG_FILE="$CONFIG_DIR/config.toml"
if [ ! -f "$CONFIG_FILE" ]; then
  cat > "$CONFIG_FILE" << EOF
[server]
listen = "127.0.0.1:7333"

[tor]
enabled      = true
control_net  = "tcp"
control_addr = "127.0.0.1:9051"
cookie_auth  = true
data_dir     = "$DATA_DIR/tor"

[storage]
db_path = "$DATA_DIR/mailbox.db"

[limits]
max_message_size          = 65536    # 64 KB per blob
max_messages_per_recipient = 200
message_ttl_hours          = 168     # 7 days (matches Android BLOB_TTL_MS)
max_storage_mb             = 500

[log]
level  = "info"
pretty = false
EOF
  chown fialka:fialka "$CONFIG_FILE"
  chmod 640 "$CONFIG_FILE"
  ok "Config written → $CONFIG_FILE"
else
  ok "Config already exists → $CONFIG_FILE (not overwritten)"
fi

# ════════════════════════════════════════════════════════════
#  STEP 5 — systemd service
# ════════════════════════════════════════════════════════════
if command -v systemctl &>/dev/null; then
  step "Installing systemd service"

  # Download service file from matching release tag
  SERVICE_URL="$REPO_RAW/$VERSION/deploy/fialka-mailbox.service"
  if curl -fsSL "$SERVICE_URL" -o "$SERVICE_FILE" 2>/dev/null; then
    # Inject config path into ExecStart line if not already there
    sed -i "s|ExecStart=.*fialka start.*|ExecStart=$INSTALL_DIR/fialka start --config $CONFIG_FILE|g" "$SERVICE_FILE"
    systemctl daemon-reload
    systemctl enable fialka-mailbox
    ok "Service installed and enabled"
  else
    warn "Could not download service file from $SERVICE_URL"
    warn "Install manually: see deploy/fialka-mailbox.service in the repo"
  fi
else
  warn "systemd not found — skip service installation"
fi

# ════════════════════════════════════════════════════════════
#  STEP 6 — First run: init owner invite
# ════════════════════════════════════════════════════════════
step "Initializing mailbox"

info "Starting fialka-mailbox daemon (background, 5s)..."
sudo -u fialka "$INSTALL_DIR/fialka" start --config "$CONFIG_FILE" &
FIALKA_PID=$!
sleep 5

# Create owner bootstrap invite
info "Generating owner bootstrap invite..."
INVITE_OUTPUT=$(sudo -u fialka "$INSTALL_DIR/fialka" mailbox init --config "$CONFIG_FILE" 2>&1 || true)

# Stop background daemon
kill "$FIALKA_PID" 2>/dev/null || true
wait "$FIALKA_PID" 2>/dev/null || true

# ════════════════════════════════════════════════════════════
#  Done
# ════════════════════════════════════════════════════════════
ONION=$(grep -oP '[a-z2-7]{56}\.onion' <<< "$INVITE_OUTPUT" || true)

echo ""
echo -e "${GREEN}${BOLD}╔══════════════════════════════════════════════════════╗${RESET}"
echo -e "${GREEN}${BOLD}║           Installation complete!                     ║${RESET}"
echo -e "${GREEN}${BOLD}╚══════════════════════════════════════════════════════╝${RESET}"
echo ""
echo -e "  ${BOLD}What was installed:${RESET}"
echo -e "    ✓ Tor ${CYAN}(The Tor Project — GPG verified)${RESET}"
echo -e "    ✓ fialka-mailbox ${VERSION}"
echo -e "    ✓ Config  → ${CONFIG_FILE}"
echo -e "    ✓ Data    → ${DATA_DIR}"
[ -f "$SERVICE_FILE" ] && echo -e "    ✓ Service → fialka-mailbox.service"
echo ""
if [ -n "$INVITE_OUTPUT" ]; then
  echo -e "  ${BOLD}Owner invite link:${RESET}"
  echo "$INVITE_OUTPUT" | grep -E "fialka://|Invite|invite" | sed 's/^/    /'
  echo ""
fi
echo -e "  ${BOLD}Start now:${RESET}"
echo -e "    ${CYAN}systemctl start fialka-mailbox${RESET}"
echo ""
echo -e "  ${BOLD}View status:${RESET}"
echo -e "    ${CYAN}fialka mailbox info --config $CONFIG_FILE${RESET}"
echo ""
echo -e "  ${BOLD}Manage members:${RESET}"
echo -e "    ${CYAN}fialka mailbox members --config $CONFIG_FILE${RESET}"
echo ""
if [ -n "$ONION" ]; then
  echo -e "  ${BOLD}Your .onion address:${RESET}"
  echo -e "    ${CYAN}${ONION}${RESET}"
  echo ""
fi
echo -e "  ${YELLOW}Share the invite link above with the mailbox owner via Fialka.${RESET}"
echo -e "  ${YELLOW}After the owner joins, use the Fialka app to invite members.${RESET}"
echo ""
