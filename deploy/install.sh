#!/usr/bin/env bash
# ============================================================
#  Fialka Mailbox — Self-hosted installer
#  https://github.com/FialkaApp/fialka-mailbox
#
#  Usage:
#    sudo bash install.sh          # first install
#    sudo bash install.sh --reconfigure   # reconfigure existing install
#
#  To uninstall after installation:
#    fialka uninstall
# ============================================================
set -euo pipefail

RECONFIGURE=false
for arg in "$@"; do
  [[ "$arg" == "--reconfigure" ]] && RECONFIGURE=true
done

# ── Constants ────────────────────────────────────────────────
REPO_API="https://api.github.com/repos/FialkaApp/fialka-mailbox"
REPO_RAW="https://raw.githubusercontent.com/FialkaApp/fialka-mailbox"
REPO_DL="https://github.com/FialkaApp/fialka-mailbox/releases/download"
INSTALL_DIR="/usr/local/bin"
DATA_DIR="/var/lib/fialka-mailbox"
CONFIG_DIR="/etc/fialka-mailbox"
CONFIG_FILE="$CONFIG_DIR/config.toml"
SERVICE_FILE="/etc/systemd/system/fialka-mailbox.service"
LOG_FILE="/var/log/fialka-mailbox/install.log"

# Official Tor Project signing key fingerprint
# Source: https://support.torproject.org/little-t-tor/verify-little-t-tor/
TOR_GPG_FINGERPRINT="A3C4F0F979CAA22CDBA8F512EE8CBC9E886DDD89"
TOR_GPG_KEY_URL="https://deb.torproject.org/torproject.org/${TOR_GPG_FINGERPRINT}.asc"
TOR_KEYRING="/usr/share/keyrings/tor-archive-keyring.gpg"

# ── Colors ───────────────────────────────────────────────────
if [ -t 1 ]; then
  RED='\033[0;31m'   ; GREEN='\033[0;32m' ; YELLOW='\033[1;33m'
  CYAN='\033[0;36m'  ; BLUE='\033[0;34m'  ; BOLD='\033[1m'
  DIM='\033[2m'      ; RESET='\033[0m'
else
  RED='' ; GREEN='' ; YELLOW='' ; CYAN='' ; BLUE='' ; BOLD='' ; DIM='' ; RESET=''
fi

# ── Helpers ──────────────────────────────────────────────────
step()     { echo -e "\n${CYAN}${BOLD}━━ $* ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"; }
substep()  { echo -e "\n  ${BLUE}${BOLD}[•]${RESET} $*"; }
ok()       { echo -e "      ${GREEN}✓${RESET}  $*"; }
warn()     { echo -e "      ${YELLOW}⚠${RESET}   $*"; }
info()     { echo -e "      ${DIM}→${RESET}  $*"; }
die()      { echo -e "\n  ${RED}${BOLD}[✗] FATAL:${RESET} $*\n" >&2; exit 1; }
hr()       { echo -e "  ${DIM}────────────────────────────────────────────────${RESET}"; }

ask_yn() {
  # ask_yn "Question?" default_y_or_n
  local question="$1"
  local default="${2:-n}"
  local prompt
  if [[ "$default" == "y" ]]; then prompt="[Y/n]"; else prompt="[y/N]"; fi
  echo -en "\n  ${BOLD}▶${RESET} $question $prompt "
  read -r answer
  answer="${answer:-$default}"
  [[ "$answer" =~ ^[Yy]$ ]]
}

ask_value() {
  # ask_value "Question?" "default_value" → sets REPLY
  local question="$1"
  local default="$2"
  echo -en "\n  ${BOLD}▶${RESET} $question ${DIM}[${default}]${RESET}: "
  read -r REPLY
  REPLY="${REPLY:-$default}"
}

pause() {
  echo -en "\n  ${DIM}Press Enter to continue...${RESET}"
  read -r
}

# ── Root check ───────────────────────────────────────────────
if [ "$EUID" -ne 0 ]; then
  die "Ce script doit être exécuté en tant que root.\n\n    sudo bash install.sh"
fi

# ── Detect OS / Arch ─────────────────────────────────────────
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH_RAW=$(uname -m)
case $ARCH_RAW in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  armv7l)  ARCH="armv7" ;;
  *) die "Architecture non supportée: $ARCH_RAW" ;;
esac

if command -v lsb_release &>/dev/null; then
  DISTRO_CODENAME=$(lsb_release -cs 2>/dev/null || true)
  DISTRO_ID=$(lsb_release -si 2>/dev/null | tr '[:upper:]' '[:lower:]' || true)
else
  DISTRO_CODENAME=""
  DISTRO_ID=""
fi

IS_DEBIAN_BASED=false
[[ "$DISTRO_ID" =~ ^(ubuntu|debian|raspbian)$ ]] && [ -n "$DISTRO_CODENAME" ] && IS_DEBIAN_BASED=true

HAS_SYSTEMD=false
command -v systemctl &>/dev/null && IS_SYSTEMD_RUNNING=$(systemctl is-system-running 2>/dev/null || true)
[[ "$IS_SYSTEMD_RUNNING" =~ ^(running|degraded)$ ]] && HAS_SYSTEMD=true

# Ensure log dir
mkdir -p "$(dirname "$LOG_FILE")"

# ════════════════════════════════════════════════════════════
#  BANNER
# ════════════════════════════════════════════════════════════
clear
echo ""
echo -e "${CYAN}${BOLD}"
cat << 'BANNER'
  ███████╗██╗ █████╗ ██╗     ██╗  ██╗ █████╗
  ██╔════╝██║██╔══██╗██║     ██║ ██╔╝██╔══██╗
  █████╗  ██║███████║██║     █████╔╝ ███████║
  ██╔══╝  ██║██╔══██║██║     ██╔═██╗ ██╔══██║
  ██║     ██║██║  ██║███████╗██║  ██╗██║  ██║
  ╚═╝     ╚═╝╚═╝  ╚═╝╚══════╝╚═╝  ╚═╝╚═╝  ╚═╝

          Mailbox — Self-hosted installer
BANNER
echo -e "${RESET}"
hr
echo -e "  Système   : ${BOLD}$OS${RESET} (${ARCH_RAW} → $ARCH)"
echo -e "  Distrib.  : ${BOLD}${DISTRO_ID:-inconnu} ${DISTRO_CODENAME:-}${RESET}"
echo -e "  Systemd   : ${BOLD}${HAS_SYSTEMD}${RESET}"
$RECONFIGURE && echo -e "\n  ${YELLOW}${BOLD}Mode : RECONFIGURATION${RESET}"
hr

echo ""
echo -e "  Ce script va installer et configurer :"
echo -e ""
echo -e "    ${CYAN}1.${RESET}  ${BOLD}Tor${RESET}  — depuis le dépôt officiel The Tor Project"
echo -e "          ${DIM}(clé GPG vérifiée avant toute installation)${RESET}"
echo -e "    ${CYAN}2.${RESET}  ${BOLD}fialka-mailbox${RESET}  — le serveur relay"
echo -e "    ${CYAN}3.${RESET}  ${BOLD}Configuration complète${RESET}  — interactivement, étape par étape"
echo -e "    ${CYAN}4.${RESET}  ${BOLD}Service systemd${RESET}  — démarrage automatique (optionnel)"
echo ""
echo -e "  ${DIM}Pour désinstaller : ${BOLD}fialka uninstall${RESET}"
echo ""

ask_yn "Commencer l'installation ?" "y" || { echo -e "\n  Annulé."; exit 0; }

# ════════════════════════════════════════════════════════════
#  STEP 1 — TOR
# ════════════════════════════════════════════════════════════
step "ÉTAPE 1 / 5  —  Installation de Tor"

echo ""
echo -e "  Fialka Mailbox utilise ${BOLD}Tor${RESET} pour exposer votre serveur"
echo -e "  via une adresse ${CYAN}.onion${RESET} sans révéler votre IP."
echo ""
echo -e "  ${BOLD}Tor Project${RESET} est une organisation à but non lucratif."
echo -e "  Le code source est public : ${DIM}https://gitlab.torproject.org${RESET}"
echo -e "  Utilisé par : Tor Browser, Signal, SecureDrop, Tails..."

if command -v tor &>/dev/null; then
  EXISTING_TOR=$(tor --version 2>&1 | head -1 || true)
  echo ""
  ok "Tor déjà installé : ${EXISTING_TOR}"
  if ! ask_yn "Réinstaller/mettre à jour Tor depuis le dépôt officiel ?" "n"; then
    info "Tor existant conservé."
    TOR_SKIP=true
  else
    TOR_SKIP=false
  fi
else
  TOR_SKIP=false
fi

if [ "$TOR_SKIP" = false ]; then
  if [ "$IS_DEBIAN_BASED" = true ]; then
    substep "Ajout du dépôt officiel The Tor Project (Debian/Ubuntu)"
    echo ""
    echo -e "  ${BOLD}Vérification de la clé GPG officielle${RESET}"
    hr
    echo -e "  Source  : ${CYAN}deb.torproject.org${RESET}"
    echo -e "  Empreinte attendue :"
    echo -e "    ${BOLD}${GREEN}${TOR_GPG_FINGERPRINT}${RESET}"
    echo -e ""
    echo -e "  ${DIM}Vous pouvez vérifier cette empreinte sur :${RESET}"
    echo -e "  ${DIM}https://support.torproject.org/little-t-tor/verify-little-t-tor/${RESET}"
    hr
    echo ""
    info "Téléchargement de la clé GPG..."

    if ! curl -fsSL "$TOR_GPG_KEY_URL" \
         | gpg --dearmor \
         | tee "$TOR_KEYRING" > /dev/null 2>&1; then
      die "Impossible de télécharger la clé GPG Tor Project depuis $TOR_GPG_KEY_URL"
    fi

    ACTUAL_FP=$(gpg --no-default-keyring --keyring "$TOR_KEYRING" \
                    --with-fingerprint --with-colons 2>/dev/null \
                | grep '^fpr' | head -1 | cut -d: -f10 | tr -d ' ')

    if [ "$ACTUAL_FP" != "$TOR_GPG_FINGERPRINT" ]; then
      rm -f "$TOR_KEYRING"
      die "EMPREINTE GPG NON CONCORDANTE !\n\n    Attendu : $TOR_GPG_FINGERPRINT\n    Reçu    : $ACTUAL_FP\n\n  Installation annulée. Vérifiez votre connexion réseau ou signalez\n  ce problème sur https://github.com/FialkaApp/fialka-mailbox/issues"
    fi

    echo ""
    echo -e "  ${GREEN}${BOLD}✓ Empreinte GPG vérifiée et confirmée${RESET}"
    echo -e "    ${GREEN}${TOR_GPG_FINGERPRINT}${RESET}"
    echo -e "    ${DIM}Correspond à la clé officielle The Tor Project${RESET}"
    echo ""

    echo "deb [signed-by=$TOR_KEYRING] https://deb.torproject.org/torproject.org $DISTRO_CODENAME main" \
      > /etc/apt/sources.list.d/tor.list
    ok "Dépôt Tor Project ajouté → /etc/apt/sources.list.d/tor.list"

    info "Mise à jour des paquets (apt update)..."
    apt-get update -qq 2>>"$LOG_FILE"
    info "Installation de tor + deb.torproject.org-keyring..."
    DEBIAN_FRONTEND=noninteractive apt-get install -y tor deb.torproject.org-keyring 2>>"$LOG_FILE"
    ok "Tor installé : $(tor --version 2>&1 | head -1)"

  else
    warn "Système non-Debian détecté — dépôt officiel Tor Project non disponible."
    warn "Installation depuis les dépôts système (fiabilité moindre)."
    echo ""
    if ask_yn "Continuer avec le paquet Tor système (moins sécurisé) ?" "n"; then
      if command -v dnf &>/dev/null; then
        dnf install -y tor 2>>"$LOG_FILE"
      elif command -v pacman &>/dev/null; then
        pacman -Sy --noconfirm tor 2>>"$LOG_FILE"
      elif command -v apt-get &>/dev/null; then
        apt-get install -y tor 2>>"$LOG_FILE"
      else
        die "Aucun gestionnaire de paquets trouvé.\nInstallez Tor manuellement depuis https://torproject.org/download/tor/"
      fi
      ok "Tor installé : $(tor --version 2>&1 | head -1)"
    else
      die "Installation annulée. Installez Tor manuellement."
    fi
  fi
fi

# ── Configure Tor control port ───────────────────────────────
substep "Configuration du control port Tor"

TOR_CONF="/etc/tor/torrc"
TOR_CHANGED=false

if ! grep -q "ControlPort 9051" "$TOR_CONF" 2>/dev/null; then
  echo ""
  echo -e "  Le control port permet à fialka-mailbox de créer le"
  echo -e "  service .onion automatiquement au démarrage."
  echo ""
  cat >> "$TOR_CONF" << 'EOF'

# --- Fialka Mailbox ---
ControlPort 9051
CookieAuthentication 1
CookieAuthFileGroupReadable 1
EOF
  TOR_CHANGED=true
  ok "Control port 9051 activé avec authentification par cookie"
else
  ok "Control port déjà configuré"
fi

# Ensure fialka user in debian-tor group (for cookie)
if getent group debian-tor &>/dev/null; then
  usermod -aG debian-tor fialka 2>/dev/null || true
fi

systemctl enable tor 2>>"$LOG_FILE" || true
if [ "$TOR_CHANGED" = true ]; then
  systemctl restart tor 2>>"$LOG_FILE" || true
else
  systemctl start tor 2>>"$LOG_FILE" || true
fi
ok "Service Tor démarré et activé au boot"

# ════════════════════════════════════════════════════════════
#  STEP 2 — fialka-mailbox binary
# ════════════════════════════════════════════════════════════
step "ÉTAPE 2 / 5  —  Installation de fialka-mailbox"

info "Récupération de la dernière version depuis GitHub..."
VERSION=$(curl -fsSL "$REPO_API/releases/latest" \
  | grep '"tag_name"' | cut -d'"' -f4 || true)

if [ -z "$VERSION" ]; then
  warn "Impossible de contacter l'API GitHub."
  ask_value "Version à installer (ex: v1.0.0)" "v1.0.0"
  VERSION="$REPLY"
fi

ok "Version cible : $VERSION"

BINARY_URL="$REPO_DL/$VERSION/fialka-mailbox_${OS}_${ARCH}"
CHECKSUM_URL="$REPO_DL/$VERSION/checksums.sha256"

info "Téléchargement du binaire ($OS/$ARCH)..."
if ! curl -fsSL "$BINARY_URL" -o /tmp/fialka-mailbox-new 2>>"$LOG_FILE"; then
  die "Impossible de télécharger : $BINARY_URL\n  Vérifiez votre connexion internet."
fi
chmod +x /tmp/fialka-mailbox-new

# SHA-256 checksum verification
if curl -fsSL "$CHECKSUM_URL" -o /tmp/fialka-mailbox.sha256 2>/dev/null; then
  EXPECTED=$(grep "fialka-mailbox_${OS}_${ARCH}$" /tmp/fialka-mailbox.sha256 | awk '{print $1}' || true)
  if [ -n "$EXPECTED" ]; then
    ACTUAL=$(sha256sum /tmp/fialka-mailbox-new | awk '{print $1}')
    if [ "$ACTUAL" != "$EXPECTED" ]; then
      rm -f /tmp/fialka-mailbox-new /tmp/fialka-mailbox.sha256
      die "HASH SHA-256 NON CONCORDANT !\n  Attendu : $EXPECTED\n  Reçu    : $ACTUAL\n\n  Le fichier pourrait être corrompu. Réessayez."
    fi
    ok "Hash SHA-256 vérifié"
    echo -e "    ${DIM}$ACTUAL${RESET}"
  fi
  rm -f /tmp/fialka-mailbox.sha256
else
  warn "Fichier checksums.sha256 non trouvé pour cette release — vérification ignorée"
fi

if [ -f "$INSTALL_DIR/fialka" ] && ! $RECONFIGURE; then
  OLD_VER=$("$INSTALL_DIR/fialka" --version 2>/dev/null || echo "inconnu")
  warn "fialka déjà installé : $OLD_VER"
  if ! ask_yn "Remplacer par la version $VERSION ?" "y"; then
    info "Binaire existant conservé."
    rm -f /tmp/fialka-mailbox-new
    BINARY_SKIP=true
  else
    BINARY_SKIP=false
  fi
else
  BINARY_SKIP=false
fi

if [ "$BINARY_SKIP" = false ]; then
  mv /tmp/fialka-mailbox-new "$INSTALL_DIR/fialka"
  ok "Binaire installé → $INSTALL_DIR/fialka ($VERSION)"
fi

# ════════════════════════════════════════════════════════════
#  STEP 3 — System user + directories
# ════════════════════════════════════════════════════════════
step "ÉTAPE 3 / 5  —  Utilisateur système & répertoires"

echo ""
echo -e "  fialka-mailbox s'exécute sous un utilisateur dédié ${BOLD}fialka${RESET}"
echo -e "  (pas de shell, pas de droits root) pour isoler le processus."
echo ""

if ! id fialka &>/dev/null; then
  useradd -r -s /bin/false -d "$DATA_DIR" -m fialka 2>>"$LOG_FILE"
  ok "Utilisateur système 'fialka' créé"
else
  ok "Utilisateur 'fialka' déjà existant"
fi

if getent group debian-tor &>/dev/null; then
  usermod -aG debian-tor fialka 2>/dev/null || true
  ok "Utilisateur fialka ajouté au groupe debian-tor (accès cookie Tor)"
fi

mkdir -p "$DATA_DIR" "$DATA_DIR/tor" "$CONFIG_DIR" "$(dirname "$LOG_FILE")"
chown -R fialka:fialka "$DATA_DIR"
chown fialka:fialka "$(dirname "$LOG_FILE")" 2>/dev/null || true
chmod 750 "$DATA_DIR" "$CONFIG_DIR"

ok "Répertoires créés :"
info "Données   → $DATA_DIR"
info "Config    → $CONFIG_DIR"
info "Logs      → $(dirname "$LOG_FILE")"

# ════════════════════════════════════════════════════════════
#  KEY PROTECTION — Detect TPM2 + choose mode
# ════════════════════════════════════════════════════════════
step "Protection de la clé .onion"

echo ""
echo -e "  La clé Ed25519 de votre adresse ${CYAN}.onion${RESET} est stockée sur le serveur."
echo -e "  Voler cette clé = usurper votre adresse permanente."
echo ""
echo -e "  ${BOLD}Options disponibles :${RESET}"
echo ""

# Detect TPM 2.0 + systemd-creds
HAS_TPM2=false
if { [ -e /dev/tpmrm0 ] || [ -e /dev/tpm0 ]; } && command -v systemd-creds &>/dev/null; then
  if systemd-creds --tpm2-device=auto encrypt /dev/null /dev/null 2>/dev/null; then
    HAS_TPM2=true
  fi
fi

echo -e "  ${BOLD}1. TPM 2.0 (systemd-creds)${RESET}"
echo -e "     Cle chiffree par le TPM de la machine. Demarrage auto possible."
echo -e "     Vol du disque seul = inutilisable."
if $HAS_TPM2; then
  echo -e "     ${GREEN}[detecte sur cette machine]${RESET}"
else
  echo -e "     ${YELLOW}[non detecte sur cette machine]${RESET}"
fi
echo ""
echo -e "  ${BOLD}2. Passphrase (argon2id + AES-256-GCM)${RESET}"
echo -e "     Cle chiffree par votre passphrase. ${BOLD}Saisie requise a chaque demarrage.${RESET}"
echo -e "     ${RED}Le service ne redemarrera PAS automatiquement.${RESET}"
echo ""
echo -e "  ${BOLD}3. Plaintext${RESET}  ${RED}[non recommande]${RESET}"
echo -e "     Cle stockee en clair (chmod 0600). Root peut la lire."
echo ""

if $HAS_TPM2; then
  DEFAULT_PROT="1"
else
  DEFAULT_PROT="2"
fi

echo -en "\n  ${BOLD}Choix [${DEFAULT_PROT}]:${RESET} "
read -r PROT_CHOICE
PROT_CHOICE="${PROT_CHOICE:-$DEFAULT_PROT}"

KEY_PROTECTION="plaintext"
ONION_PASSPHRASE=""

case "$PROT_CHOICE" in
  1)
    if $HAS_TPM2; then
      KEY_PROTECTION="tpm"
      ok "Mode TPM 2.0 selectionne"
    else
      warn "TPM 2.0 non disponible -- retour au mode passphrase"
      PROT_CHOICE=2
    fi
    ;;
esac

if [ "$PROT_CHOICE" = "2" ]; then
  KEY_PROTECTION="passphrase"
  echo ""
  echo -e "  ${BOLD}Choisissez une passphrase pour la cle .onion${RESET}"
  echo -e "  ${DIM}Requise a chaque demarrage du daemon. Min 12 caracteres.${RESET}"
  echo -e "  ${YELLOW}Ne la perdez pas -- perte passphrase = perte adresse .onion.${RESET}"
  echo ""
  while true; do
    echo -en "  ${BOLD}Passphrase :${RESET} "
    read -rs ONION_PASSPHRASE
    echo ""
    if [ ${#ONION_PASSPHRASE} -lt 12 ]; then
      warn "Trop courte (min 12 caracteres). Recommencez."
      continue
    fi
    echo -en "  ${BOLD}Confirmez :${RESET} "
    read -rs ONION_PASSPHRASE_CONFIRM
    echo ""
    if [ "$ONION_PASSPHRASE" = "$ONION_PASSPHRASE_CONFIRM" ]; then
      unset ONION_PASSPHRASE_CONFIRM
      ok "Passphrase definie (${#ONION_PASSPHRASE} caracteres)"
      echo ""
      warn "Le daemon ne peut PAS demarrer sans cette passphrase."
      warn "Le service systemd ne redemarrera PAS automatiquement."
      break
    else
      warn "Les passphrases ne correspondent pas. Recommencez."
    fi
  done
fi

if [ "$PROT_CHOICE" = "3" ] || { [ "$PROT_CHOICE" != "1" ] && [ "$PROT_CHOICE" != "2" ]; }; then
  KEY_PROTECTION="plaintext"
  warn "Mode PLAINTEXT - la cle onion.key est stockee en clair."
fi

# ════════════════════════════════════════════════════════════
#  STEP 4 — Interactive configuration
# ════════════════════════════════════════════════════════════
step "ÉTAPE 4 / 5  —  Configuration"

echo ""
echo -e "  Nous allons configurer votre mailbox étape par étape."
echo -e "  ${DIM}Appuyez sur Entrée pour accepter la valeur par défaut [entre crochets].${RESET}"

# ── Listen address ───────────────────────────────────────────────────────
echo ""
echo -e "  ${BOLD}Adresse d'écoute TCP${RESET}"
echo -e "  ${DIM}Le serveur écoute en local uniquement — Tor gère la partie .onion.${RESET}"
echo -e "  ${DIM}Laissez 127.0.0.1:7333 sauf si vous savez ce que vous faites.${RESET}"
ask_value "Adresse:port d'écoute" "127.0.0.1:7333"
LISTEN="$REPLY"

# ── Message TTL ──────────────────────────────────────────────────────────
echo ""
echo -e "  ${BOLD}Durée de rétention des messages (TTL)${RESET}"
echo -e "  ${DIM}Combien de jours conserver les messages non récupérés ?${RESET}"
echo -e "  ${DIM}L'app Android retente la récupération toutes les 10-60 secondes.${RESET}"
ask_value "TTL en jours" "7"
TTL_DAYS="$REPLY"
TTL_HOURS=$(( TTL_DAYS * 24 ))

# ── Storage limit ────────────────────────────────────────────────────────
echo ""
echo -e "  ${BOLD}Limite de stockage total${RESET}"
echo -e "  ${DIM}Taille maximale de la base de données (mégaoctets).${RESET}"
ask_value "Limite de stockage (MB)" "500"
MAX_STORAGE="$REPLY"

# ── Max messages per recipient ───────────────────────────────────────────
echo ""
echo -e "  ${BOLD}Messages max par destinataire${RESET}"
echo -e "  ${DIM}Nombre de messages en attente acceptés par membre.${RESET}"
ask_value "Messages max par destinataire" "200"
MAX_MSGS="$REPLY"

# ── Log level ────────────────────────────────────────────────────────────
echo ""
echo -e "  ${BOLD}Niveau de log${RESET}"
echo -e "  ${DIM}Options : debug, info, warn, error${RESET}"
ask_value "Niveau de log" "info"
LOG_LEVEL="$REPLY"

# ── Write config ─────────────────────────────────────────────────────────
echo ""
info "Écriture de la configuration..."

if [ -f "$CONFIG_FILE" ] && ! $RECONFIGURE; then
  BACKUP="$CONFIG_FILE.bak.$(date +%Y%m%d_%H%M%S)"
  cp "$CONFIG_FILE" "$BACKUP"
  ok "Ancienne configuration sauvegardée → $BACKUP"
fi

cat > "$CONFIG_FILE" << EOF
# Fialka Mailbox — Configuration
# Installer le : $(date)
# Documentation : https://github.com/FialkaApp/fialka-mailbox

[server]
# Adresse TCP locale (Tor mappe l'externe :7333 → ici)
listen = "${LISTEN}"

[tor]
enabled      = true
control_net  = "tcp"
control_addr = "127.0.0.1:9051"
cookie_auth  = true                    # authentification par cookie (recommandé)
data_dir     = "${DATA_DIR}/tor"       # clé privée onion stockée ici

# Protection de la clé .onion à l'arrêt :
#   "plaintext"  - onion.key en clair (chmod 0600)
#   "passphrase" - onion.key.enc chiffré argon2id+AES-256-GCM (prompt au démarrage)
#   "tpm"        - chiffré par systemd-creds + TPM2 (démarrage automatique)
key_protection = "${KEY_PROTECTION}"
cred_name      = "onion-key"

[storage]
db_path = "${DATA_DIR}/mailbox.db"

[limits]
# Taille max d'un message déposé (octets) — 65536 = 64 KB
max_message_size = 65536
# Messages en attente max par destinataire
max_messages_per_recipient = ${MAX_MSGS}
# Durée de rétention en heures (${TTL_DAYS} jours = ${TTL_HOURS}h)
message_ttl_hours = ${TTL_HOURS}
# Stockage total max (mégaoctets)
max_storage_mb = ${MAX_STORAGE}

[log]
level  = "${LOG_LEVEL}"
pretty = false    # true = couleurs pour terminal interactif
EOF

chown fialka:fialka "$CONFIG_FILE"
chmod 640 "$CONFIG_FILE"
ok "Configuration écrite → $CONFIG_FILE"

# ════════════════════════════════════════════════════════════
#  STEP 5 — Service systemd
# ════════════════════════════════════════════════════════════
step "ÉTAPE 5 / 5  —  Service systemd"

SETUP_SERVICE=false
if $HAS_SYSTEMD; then
  echo ""
  echo -e "  Un service systemd permet de gérer fialka-mailbox avec :"
  echo -e "  ${DIM}systemctl start|stop|restart|status fialka-mailbox${RESET}"
  echo ""
  if [ "$KEY_PROTECTION" = "passphrase" ]; then
    warn "Mode passphrase : le service sera installe SANS redemarrage automatique."
    warn "Demarrage manuel requis a chaque reboot (systemctl start fialka-mailbox)."
    echo ""
  fi
  if ask_yn "Installer le service systemd ?" "y"; then
    SETUP_SERVICE=true
  fi
else
  warn "Systemd non disponible — service ignoré"
fi

if $SETUP_SERVICE; then
  # Adjust Restart policy based on key protection mode
  if [ "$KEY_PROTECTION" = "passphrase" ]; then
    SVC_RESTART="no"
  else
    SVC_RESTART="on-failure"
  fi

  # Build service file inline (no network download needed)
  cat > "$SERVICE_FILE" << EOF
[Unit]
Description=Fialka Mailbox - self-hosted Tor message relay
After=network.target tor.service
Requires=tor.service

[Service]
Type=simple
User=fialka
Group=fialka
ExecStart=$INSTALL_DIR/fialka start --config $CONFIG_FILE
Restart=$SVC_RESTART
RestartSec=10
StandardOutput=journal
StandardError=journal
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=$DATA_DIR /var/log/fialka-mailbox
EOF

  # TPM mode: add LoadCredentialEncrypted directive
  if [ "$KEY_PROTECTION" = "tpm" ]; then
    echo "LoadCredentialEncrypted=onion-key:${CONFIG_DIR}/onion-key.cred" >> "$SERVICE_FILE"
  fi

  cat >> "$SERVICE_FILE" << 'EOF'

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable fialka-mailbox 2>>"$LOG_FILE"
  ok "Service installe et active → $SERVICE_FILE"
  [ "$KEY_PROTECTION" = "passphrase" ] && warn "Restart=no — demarrage manuel requis"
  [ "$KEY_PROTECTION" = "tpm" ] && ok "Restart=on-failure — redemarrage auto (TPM)"
fi

# ════════════════════════════════════════════════════════════
#  INIT — owner bootstrap invite
# ════════════════════════════════════════════════════════════
step "Initialisation de la mailbox"

echo ""
echo -e "  Démarrage temporaire du daemon pour :"
echo -e "    ${CYAN}1.${RESET} Générer la clé Ed25519 et l'adresse .onion"
echo -e "    ${CYAN}2.${RESET} Créer le lien d'invitation propriétaire"
echo ""

info "Démarrage du daemon (15 secondes)..."
set +e

if [ "$KEY_PROTECTION" = "passphrase" ]; then
  # Pass the passphrase via stdin — it does NOT appear in process arguments or /proc
  echo "$ONION_PASSPHRASE" | sudo -u fialka "$INSTALL_DIR/fialka" start \
    --config "$CONFIG_FILE" \
    --passphrase-stdin \
    >> "$LOG_FILE" 2>&1 &
else
  sudo -u fialka "$INSTALL_DIR/fialka" start --config "$CONFIG_FILE" >> "$LOG_FILE" 2>&1 &
fi
FIALKA_PID=$!
sleep 15

info "Génération de l'invitation propriétaire..."
INVITE_OUTPUT=$(sudo -u fialka "$INSTALL_DIR/fialka" mailbox init --config "$CONFIG_FILE" 2>&1 || true)

kill "$FIALKA_PID" 2>/dev/null || true
wait "$FIALKA_PID" 2>/dev/null || true
set -e

ONION=$(grep -oP '[a-z2-7]{56}\.onion' <<< "$INVITE_OUTPUT" 2>/dev/null || true)
INVITE_LINK=$(grep -oP 'fialka://\S+' <<< "$INVITE_OUTPUT" 2>/dev/null || true)

# TPM post-processing: encrypt the freshly generated onion.key with the TPM
if [ "$KEY_PROTECTION" = "tpm" ]; then
  substep "Chiffrement de la clé onion avec TPM 2.0 (systemd-creds)..."
  ONION_KEY_PLAIN="$DATA_DIR/tor/onion.key"
  ONION_KEY_CRED="$CONFIG_DIR/onion-key.cred"
  if [ -f "$ONION_KEY_PLAIN" ]; then
    if systemd-creds encrypt --tpm2-device=auto --name=onion-key \
        "$ONION_KEY_PLAIN" "$ONION_KEY_CRED" 2>>"$LOG_FILE"; then
      shred -u "$ONION_KEY_PLAIN" 2>/dev/null || rm -f "$ONION_KEY_PLAIN"
      chown root:root "$ONION_KEY_CRED"
      chmod 600 "$ONION_KEY_CRED"
      ok "Cle chiffree par TPM → $ONION_KEY_CRED"
      ok "Cle plaintext detruite de maniere securisee"
    else
      warn "Chiffrement TPM echoue — cle plaintext conservee (verifie /dev/tpmrm*)"
    fi
  else
    warn "onion.key introuvable — le daemon n'a peut-etre pas demarre correctement"
    warn "Verifie les logs : $LOG_FILE"
  fi
fi

# Zero passphrase from bash memory
ONION_PASSPHRASE=""
unset ONION_PASSPHRASE

# ════════════════════════════════════════════════════════════
#  START now ?
# ════════════════════════════════════════════════════════════
START_NOW=false
if $SETUP_SERVICE; then
  echo ""
  if [ "$KEY_PROTECTION" = "passphrase" ]; then
    info "Mode passphrase — demarrage manuel requis depuis un terminal :"
    echo -e "    ${CYAN}sudo systemctl start fialka-mailbox${RESET}"
    echo -e "  ${DIM}(systemd-ask-password vous demandera la passphrase)${RESET}"
  elif ask_yn "Démarrer fialka-mailbox maintenant (via systemd) ?" "y"; then
    START_NOW=true
    systemctl start fialka-mailbox 2>>"$LOG_FILE"
    sleep 3
    SVC_STATUS=$(systemctl is-active fialka-mailbox 2>/dev/null || true)
    if [ "$SVC_STATUS" = "active" ]; then
      ok "Service fialka-mailbox démarré et actif"
    else
      warn "Statut : $SVC_STATUS — vérifiez : journalctl -u fialka-mailbox -n 50"
    fi
  fi
else
  echo ""
  if ask_yn "Démarrer fialka-mailbox en arrière-plan maintenant ?" "y"; then
    START_NOW=true
    sudo -u fialka nohup "$INSTALL_DIR/fialka" start --config "$CONFIG_FILE" \
      >> /var/log/fialka-mailbox/service.log 2>&1 &
    info "PID : $!"
    ok "fialka-mailbox démarré (nohup)"
    warn "Sans systemd, il ne redémarrera pas automatiquement au boot."
  fi
fi

# ════════════════════════════════════════════════════════════
#  FINAL SUMMARY
# ════════════════════════════════════════════════════════════
clear
echo ""
echo -e "${GREEN}${BOLD}"
cat << 'DONE'
  ╔══════════════════════════════════════════════════════════╗
  ║                                                          ║
  ║          ✓  Installation terminée avec succès            ║
  ║                                                          ║
  ╚══════════════════════════════════════════════════════════╝
DONE
echo -e "${RESET}"

hr
echo -e "  ${BOLD}Ce qui a été installé${RESET}"
hr
echo -e "    ✓  Tor ${CYAN}(The Tor Project — GPG vérifié)${RESET}"
echo -e "    ✓  fialka-mailbox ${BOLD}${VERSION}${RESET}  →  $INSTALL_DIR/fialka"
echo -e "    ✓  Configuration  →  $CONFIG_FILE"
echo -e "    ✓  Données        →  $DATA_DIR"
$SETUP_SERVICE && echo -e "    ✓  Service        →  fialka-mailbox.service"
echo ""

hr
echo -e "  ${BOLD}Protection de la clé .onion${RESET}"
hr
case "$KEY_PROTECTION" in
  tpm)
    echo -e "    ${GREEN}✓${RESET}  ${BOLD}TPM 2.0 (systemd-creds)${RESET}"
    echo -e "    ${DIM}  Redémarrage automatique : OUI${RESET}"
    [ -f "$CONFIG_DIR/onion-key.cred" ] && echo -e "    ${DIM}  Credential : $CONFIG_DIR/onion-key.cred${RESET}"
    ;;
  passphrase)
    echo -e "    ${YELLOW}✓${RESET}  ${BOLD}Passphrase (argon2id + AES-256-GCM)${RESET}"
    echo -e "    ${YELLOW}  Saisie requise à chaque démarrage${RESET}"
    echo -e "    ${RED}  Redémarrage automatique : NON${RESET}"
    echo -e "    ${DIM}  Clé : $DATA_DIR/tor/onion.key.enc${RESET}"
    echo ""
    echo -e "  ${YELLOW}${BOLD}Pour démarrer (mode passphrase) :${RESET}"
    echo -e "    ${CYAN}sudo systemctl start fialka-mailbox${RESET}"
    echo -e "  ${DIM}  (systemd-ask-password vous demandera la passphrase)${RESET}"
    ;;
  *)
    echo -e "    ${RED}!${RESET}  ${BOLD}Plaintext${RESET} ${RED}(non recommandé)${RESET}"
    echo -e "    ${DIM}  Clé : $DATA_DIR/tor/onion.key (chmod 0600)${RESET}"
    ;;
esac
echo ""

if [ -n "$ONION" ]; then
  hr
  echo -e "  ${BOLD}Votre adresse .onion${RESET}  ${DIM}(permanente tant que onion.key est conservé)${RESET}"
  hr
  echo -e ""
  echo -e "    ${CYAN}${BOLD}${ONION}${RESET}"
  echo ""
fi

if [ -n "$INVITE_LINK" ]; then
  hr
  echo -e "  ${BOLD}Lien d'invitation propriétaire${RESET}  ${DIM}(usage unique — partagez-le via Fialka)${RESET}"
  hr
  echo ""
  echo -e "    ${YELLOW}${INVITE_LINK}${RESET}"
  echo ""
  echo -e "  ${RED}${BOLD}⚠  Ce lien est OBLIGATOIRE pour rejoindre la mailbox.${RESET}"
  echo -e "  ${DIM}  Sans lui, aucune connexion ne sera acceptée par le serveur.${RESET}"
  echo -e "  ${DIM}  La première personne à l'utiliser devient PROPRIÉTAIRE.${RESET}"
  echo -e "  ${DIM}  Après ça, utilisez l'app Fialka pour inviter des membres.${RESET}"
  echo ""
  echo -e "  ${DIM}  Pour régénérer ce lien si perdu :${RESET}"
  echo -e "    ${CYAN}fialka mailbox init --config $CONFIG_FILE${RESET}"
  echo ""
fi

hr
echo -e "  ${BOLD}Commandes utiles${RESET}"
hr
echo ""
echo -e "  ${BOLD}Démarrer${RESET}"
$SETUP_SERVICE \
  && echo -e "    ${CYAN}systemctl start fialka-mailbox${RESET}" \
  || echo -e "    ${CYAN}sudo -u fialka fialka start --config $CONFIG_FILE${RESET}"

echo ""
echo -e "  ${BOLD}Arrêter${RESET}"
$SETUP_SERVICE \
  && echo -e "    ${CYAN}systemctl stop fialka-mailbox${RESET}" \
  || echo -e "    ${CYAN}pkill fialka${RESET}"

echo ""
echo -e "  ${BOLD}Voir les logs${RESET}"
$SETUP_SERVICE \
  && echo -e "    ${CYAN}journalctl -u fialka-mailbox -f${RESET}" \
  || echo -e "    ${CYAN}tail -f /var/log/fialka-mailbox/service.log${RESET}"

echo ""
echo -e "  ${BOLD}Statut de la mailbox${RESET}"
echo -e "    ${CYAN}fialka mailbox info --config $CONFIG_FILE${RESET}"

echo ""
echo -e "  ${BOLD}Gérer les membres (TUI)${RESET}"
echo -e "    ${CYAN}fialka mailbox members --config $CONFIG_FILE${RESET}"

echo ""
echo -e "  ${BOLD}Créer une invitation${RESET}"
echo -e "    ${CYAN}fialka mailbox invite --config $CONFIG_FILE${RESET}"

echo ""
echo -e "  ${BOLD}Reconfigurer${RESET}"
echo -e "    ${CYAN}sudo bash install.sh --reconfigure${RESET}"

echo ""
echo -e "  ${BOLD}Désinstaller complètement${RESET}"
echo -e "    ${CYAN}fialka uninstall${RESET}"

echo ""
hr
echo ""
echo -e "  ${DIM}Journal d'installation → $LOG_FILE${RESET}"
echo ""
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
  INVITE_LINK_EN=$(grep -oP 'fialka://\S+' <<< "$INVITE_OUTPUT" 2>/dev/null || true)
  echo -e "  ${BOLD}Owner invite link:${RESET}"
  echo ""
  if [ -n "$INVITE_LINK_EN" ]; then
    echo -e "    ${YELLOW}${INVITE_LINK_EN}${RESET}"
  else
    echo "$INVITE_OUTPUT" | grep -E "fialka://|Invite|invite" | sed 's/^/    /'
  fi
  echo ""
  echo -e "  ${RED}${BOLD}⚠  This link is MANDATORY to join the mailbox.${RESET}"
  echo -e "  ${DIM}  Without it, no connection will be accepted by the server.${RESET}"
  echo -e "  ${DIM}  The first person to use it becomes the OWNER.${RESET}"
  echo -e "  ${DIM}  After that, use the Fialka app to invite members.${RESET}"
  echo ""
  echo -e "  ${DIM}  To regenerate this link if lost:${RESET}"
  echo -e "    ${CYAN}fialka mailbox init --config $CONFIG_FILE${RESET}"
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
