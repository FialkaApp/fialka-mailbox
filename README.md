# fialka-mailbox

Self-hosted, privacy-first message relay for the Fialka ecosystem.

## What it does

- Stores encrypted message blobs (store-and-forward)
- Never decrypts — the server is a blind relay
- Exposes a Tor hidden service (.onion) by default
- Deployable on a Raspberry Pi, VPS, or Docker

## Quick install (Linux / RPi)

```bash
curl -fsSL https://get.fialka.app/mailbox | bash
fialka setup
fialka start
```

## Docker

```bash
docker compose up -d
```

## Commands

```
fialka setup     Interactive setup wizard
fialka start     Start the relay daemon
fialka stop      Stop the relay daemon
fialka restart   Restart the relay daemon
fialka status    Show status + .onion address
fialka config    Edit configuration
fialka logs      Tail live logs
```

## Stack

- **Go 1.23** — single static binary
- **Cobra** — CLI subcommands
- **Bubbletea + huh** — interactive TUI wizard
- **SQLite** (modernc/sqlite, pure Go) — message storage
- **Tor** — hidden service via control port
- **Zerolog** — structured logging

## License

GPL-3.0 — see [LICENSE](LICENSE)
