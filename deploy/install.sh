#!/usr/bin/env bash
set -euo pipefail

REPO="https://github.com/FialkaApp/fialka-mailbox"
INSTALL_DIR="/usr/local/bin"
DATA_DIR="/var/lib/fialka-mailbox"
SERVICE_FILE="/etc/systemd/system/fialka-mailbox.service"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case $ARCH in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  armv7l)  ARCH="armv7" ;;
  *) echo "Unsupported architecture: $ARCH" ; exit 1 ;;
esac

echo "==> Fialka Mailbox installer"
echo "    OS: $OS / Arch: $ARCH"

# Fetch latest release tag from GitHub API
VERSION=$(curl -fsSL "https://api.github.com/repos/FialkaApp/fialka-mailbox/releases/latest" \
  | grep '"tag_name"' | cut -d'"' -f4)

echo "    Version: $VERSION"

URL="$REPO/releases/download/$VERSION/fialka-mailbox_${OS}_${ARCH}"

echo "==> Downloading binary..."
curl -fsSL "$URL" -o /tmp/fialka-mailbox
chmod +x /tmp/fialka-mailbox

echo "==> Installing to $INSTALL_DIR/fialka..."
sudo mv /tmp/fialka-mailbox "$INSTALL_DIR/fialka"

echo "==> Creating data directory..."
sudo mkdir -p "$DATA_DIR"
sudo useradd -r -s /bin/false -d "$DATA_DIR" fialka 2>/dev/null || true
sudo chown fialka:fialka "$DATA_DIR"

if command -v systemctl &>/dev/null; then
  echo "==> Installing systemd service..."
  curl -fsSL "$REPO/raw/$VERSION/deploy/fialka-mailbox.service" | sudo tee "$SERVICE_FILE" > /dev/null
  sudo systemctl daemon-reload
  sudo systemctl enable fialka-mailbox
fi

echo ""
echo "✓ Installation complete."
echo "  Run 'fialka setup' to configure your relay."
