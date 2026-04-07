# fialka-mailbox

> **[English version below](#english)**

---

## Français

Serveur relay auto-hébergé, privacy-first, pour l'écosystème [Fialka](https://fialka.app).

### Ce que c'est

**fialka-mailbox** est un relais store-and-forward **aveugle** : il stocke et transfère des blobs chiffrés sans jamais en voir le contenu. Les messages sont chiffrés par l'application Fialka (PQXDH + Double Ratchet) avant dépôt — le serveur ne voit que du bruit.

- Exposé via **Tor hidden service v3 (.onion)** — aucune IP exposée
- Protocole TCP binaire au lieu de HTTP — surface d'attaque minimale
- Membership contrôlé (liste blanche Ed25519 — pas de relay public ouvert)
- Clé .onion protégée : **passphrase** (argon2id + AES-256-GCM) ou **TPM 2.0** (systemd-creds)
- Binaire statique unique — déployable sur RPi, VPS, Docker

### Installation rapide (Linux / RPi)

```bash
curl -fsSL https://raw.githubusercontent.com/FialkaApp/fialka-mailbox/main/deploy/install.sh | sudo bash
```

L'installeur détecte automatiquement si un **TPM 2.0** est disponible et propose :
1. **TPM 2.0** — clé liée au hardware, redémarrage automatique
2. **Passphrase** — argon2id + AES-256-GCM, saisie à chaque démarrage *(recommandé sans TPM)*
3. **Plaintext** — legacy, non recommandé

### Installation manuelle

```bash
# Cloner
git clone https://github.com/FialkaApp/fialka-mailbox.git
cd fialka-mailbox

# Compiler (Go 1.23+)
go build -o fialka .

# Setup interactif
sudo ./fialka setup

# Démarrer
sudo ./fialka start
```

### Docker

```bash
docker compose up -d
```

La clé .onion est persistée dans le volume `mailbox-data`.

### Commandes

| Commande | Description |
|---|---|
| `fialka setup` | Wizard de configuration interactif (TUI) |
| `fialka start` | Démarrer le daemon relay |
| `fialka stop` | Arrêter le daemon |
| `fialka restart` | Redémarrer le daemon |
| `fialka status` | Statut + adresse .onion |
| `fialka config` | Édition interactive de la configuration |
| `fialka logs` | Logs en temps réel |
| `fialka mailbox` | Gestion membres et invitations |
| `fialka uninstall` | Désinstallation propre |

### Configuration

Fichier : `/etc/fialka-mailbox/config.toml`

```toml
[server]
listen = "127.0.0.1:7333"   # Port TCP interne (Tor gère l'exposition)

[tor]
enabled = true
tor_binary = ""              # Auto-détecté
data_dir = ""                # Défaut: ~/.config/fialka-mailbox/tor
key_protection = "passphrase" # "plaintext" | "passphrase" | "tpm"
cred_name = "onion-key"      # Nom du credential systemd (mode tpm)

[storage]
db_path = "mailbox.db"

[limits]
max_message_size = 65536     # 64 Ko par message
max_messages_per_recipient = 100
message_ttl_hours = 168      # 7 jours
max_storage_mb = 500

[security]
rate_limit_deposits = 60     # Dépôts max par IP par minute
require_auth_fetch = true    # Auth Ed25519 obligatoire pour la récupération
```

### Protocole

Le serveur utilise un protocole **TCP binaire** (pas HTTP), format :

```
[4 octets : type] [4 octets : longueur payload] [payload]
```

| Type | Code | Description |
|---|---|---|
| DEPOSIT | 0x01 | Dépôt d'un blob chiffré |
| FETCH | 0x02 | Récupération des messages (auth Ed25519) |
| DELETE | 0x03 | Acquittement / suppression |
| FETCH_RESP | 0x04 | Réponse avec les blobs |
| ACK | 0x05 | Accusé de réception |
| ERROR | 0x06 | Erreur protocolaire |
| INVITE_USE | 0x07 | Utilisation d'un token d'invitation |
| MEMBER_LIST | 0x08 | Liste des membres |

**Auth FETCH** : challenge-response Ed25519.
Le serveur ne connaît que `base64(raw 32B pubkey)` — jamais la clé privée, jamais le contenu.

### Membership

```bash
# Générer une invitation (usage unique, 24h)
fialka mailbox invite --max-uses 1 --expires 24h

# Lister les membres
fialka mailbox list

# Révoquer un membre
fialka mailbox revoke <pubkey_hash>
```

### Sécurité — Modèle de menace

**Ce que fialka-mailbox protège :**
- Contenu des messages (chiffrement E2E par Fialka — le serveur voit des blobs opaques)
- Identité des correspondants (seul le hash SHA-256 de la pubkey est stocké)
- Clé .onion au repos en modes passphrase et TPM

**Ce que fialka-mailbox ne protège PAS :**
- Métadonnées de timing (moment du dépôt/récupération)
- Analyse de trafic réseau (Tor atténue mais n'élimine pas)
- Compromission du serveur en cours d'exécution
- Saisie légale du serveur (les blobs chiffrés restent inutilisables pour l'attaquant)

### Stack technique

| Composant | Librairie |
|---|---|
| CLI | `cobra` |
| Wizard setup | `bubbletea` + `huh` |
| Protocole TCP | stdlib `net` |
| Base de données | `modernc/sqlite` (pure Go) |
| Tor | control port (`ADD_ONION ED25519-V3`) |
| Config | `viper` (TOML) |
| Logs | `zerolog` |
| Crypto clé | `golang.org/x/crypto` (argon2id + AES-256-GCM) |
| TTY passphrase | `golang.org/x/term` |

### Licence

GPL-3.0 — voir [LICENSE](LICENSE)

---

## English

<a name="english"></a>

Self-hosted, privacy-first message relay for the [Fialka](https://fialka.app) ecosystem.

### What it is

**fialka-mailbox** is a **blind** store-and-forward relay: it stores and forwards encrypted blobs without ever seeing their content. Messages are encrypted by the Fialka app (PQXDH + Double Ratchet) before deposit — the server only sees opaque ciphertext.

- Exposed via **Tor hidden service v3 (.onion)** — no IP address exposed
- Binary TCP protocol instead of HTTP — minimal attack surface
- Controlled membership (Ed25519 allowlist — no open relay)
- .onion key protection: **passphrase** (argon2id + AES-256-GCM) or **TPM 2.0** (systemd-creds)
- Single static binary — deployable on RPi, VPS, Docker

### Quick install (Linux / RPi)

```bash
curl -fsSL https://raw.githubusercontent.com/FialkaApp/fialka-mailbox/main/deploy/install.sh | sudo bash
```

The installer automatically detects if a **TPM 2.0** is available and offers:
1. **TPM 2.0** — hardware-bound key, automatic restart
2. **Passphrase** — argon2id + AES-256-GCM, prompted on every start *(recommended without TPM)*
3. **Plaintext** — legacy, not recommended

### Manual install

```bash
git clone https://github.com/FialkaApp/fialka-mailbox.git
cd fialka-mailbox

# Build (requires Go 1.23+)
go build -o fialka .

# Interactive setup
sudo ./fialka setup

# Start
sudo ./fialka start
```

### Docker

```bash
docker compose up -d
```

The .onion key is persisted in the `mailbox-data` volume.

### Commands

| Command | Description |
|---|---|
| `fialka setup` | Interactive configuration wizard (TUI) |
| `fialka start` | Start the relay daemon |
| `fialka stop` | Stop the daemon |
| `fialka restart` | Restart the daemon |
| `fialka status` | Status + .onion address |
| `fialka config` | Interactive configuration editor |
| `fialka logs` | Tail live logs |
| `fialka mailbox` | Manage members and invitations |
| `fialka uninstall` | Clean uninstall |

### Configuration

File: `/etc/fialka-mailbox/config.toml`

```toml
[server]
listen = "127.0.0.1:7333"   # Internal TCP port (Tor handles exposure)

[tor]
enabled = true
tor_binary = ""              # Auto-detected
data_dir = ""                # Default: ~/.config/fialka-mailbox/tor
key_protection = "passphrase" # "plaintext" | "passphrase" | "tpm"
cred_name = "onion-key"      # systemd credential name (tpm mode)

[storage]
db_path = "mailbox.db"

[limits]
max_message_size = 65536     # 64 KB per message
max_messages_per_recipient = 100
message_ttl_hours = 168      # 7 days
max_storage_mb = 500

[security]
rate_limit_deposits = 60     # Max deposits per IP per minute
require_auth_fetch = true    # Ed25519 auth required to fetch messages
```

### Protocol

The server uses a **binary TCP protocol** (not HTTP), with the frame format:

```
[4 bytes: type] [4 bytes: payload length] [payload]
```

| Type | Code | Description |
|---|---|---|
| DEPOSIT | 0x01 | Deposit an encrypted blob |
| FETCH | 0x02 | Retrieve messages (Ed25519 auth) |
| DELETE | 0x03 | Acknowledge receipt / delete |
| FETCH_RESP | 0x04 | Response with blobs |
| ACK | 0x05 | Acknowledgement |
| ERROR | 0x06 | Protocol error |
| INVITE_USE | 0x07 | Use an invitation token |
| MEMBER_LIST | 0x08 | List members |

**FETCH auth**: Ed25519 challenge-response.
The server only stores `base64(raw 32B pubkey)` — never the private key, never the content.

### Membership

```bash
# Generate an invitation (single-use, 24h)
fialka mailbox invite --max-uses 1 --expires 24h

# List members
fialka mailbox list

# Revoke a member
fialka mailbox revoke <pubkey_hash>
```

### Security — Threat model

**What fialka-mailbox protects:**
- Message content (E2E encrypted by Fialka — the server sees opaque blobs)
- Correspondent identities (only SHA-256 hash of pubkey is stored)
- .onion key at rest in passphrase and TPM modes

**What fialka-mailbox does NOT protect:**
- Timing metadata (when messages are deposited/retrieved)
- Network traffic analysis (Tor mitigates but does not eliminate)
- A compromised running server
- Legal seizure of the server (encrypted blobs remain useless to an attacker)

### Tech stack

| Component | Library |
|---|---|
| CLI | `cobra` |
| Setup wizard | `bubbletea` + `huh` |
| TCP protocol | stdlib `net` |
| Database | `modernc/sqlite` (pure Go) |
| Tor | control port (`ADD_ONION ED25519-V3`) |
| Config | `viper` (TOML) |
| Logging | `zerolog` |
| Key crypto | `golang.org/x/crypto` (argon2id + AES-256-GCM) |
| TTY passphrase | `golang.org/x/term` |

### License

GPL-3.0 — see [LICENSE](LICENSE)
