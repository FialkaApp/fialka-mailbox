package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fialkaapp/fialka-mailbox/internal/storage"
)

// ── Log colorizer ─────────────────────────────────────────────────────────────

func tuiColorizeLog(line string) string {
	switch {
	case strings.Contains(line, " ERR "), strings.Contains(line, " FTL "):
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#f87171")).Render(line)
	case strings.Contains(line, " WRN "):
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#fbbf24")).Render(line)
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#94a3b8")).Render(line)
	}
}

// ── External log writer (zerolog → channel) ────────────────────────────────────

// tuiLogWriter implements io.Writer; it forwards every log line to the TUI
// viewport channel. Writes never block — lines are dropped if the buffer is full
// so server goroutines are never stalled by a slow terminal.
type tuiLogWriter struct {
	mu sync.Mutex
	ch chan<- string
}

func (w *tuiLogWriter) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n\r")
	if line == "" {
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case w.ch <- line:
	default: // buffer full — drop rather than block the server
	}
	return len(p), nil
}

// ── Tea message types ─────────────────────────────────────────────────────────

type (
	tuiLogLineMsg string
	tuiStatsMsg   struct {
		members   int
		pending   int64
		sizeBytes int64
	}
	tuiSecondTickMsg time.Time
)

// ── Tea commands ──────────────────────────────────────────────────────────────

func tuiWaitForLog(ch <-chan string) tea.Cmd {
	return func() tea.Msg { return tuiLogLineMsg(<-ch) }
}

func tuiSecondTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tuiSecondTickMsg(t) })
}

func tuiQueryStats(store *storage.SQLiteStore) tuiStatsMsg {
	members, _ := store.ListMembers()
	stats, _ := store.Stats()
	msg := tuiStatsMsg{members: len(members)}
	if stats != nil {
		msg.pending = stats.PendingMessages
		msg.sizeBytes = stats.TotalSizeBytes
	}
	return msg
}

// tuiFetchStats returns an immediate stats read (no delay).
func tuiFetchStats(store *storage.SQLiteStore) tea.Cmd {
	return func() tea.Msg { return tuiQueryStats(store) }
}

// tuiScheduleStats returns a stats read after a 5-second delay.
func tuiScheduleStats(store *storage.SQLiteStore) tea.Cmd {
	return tea.Tick(5*time.Second, func(_ time.Time) tea.Msg {
		return tuiQueryStats(store)
	})
}

// ── Styles ────────────────────────────────────────────────────────────────────

var (
	tuiBrandStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f8fafc"))
	tuiVerStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#10b981"))
	tuiDotStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#10b981"))
	tuiUptimeStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#10b981"))
	tuiStatNumStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e2e8f0"))
	tuiStatLblStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#475569"))
	tuiOnionStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#a78bfa"))
	tuiThickSep     = lipgloss.NewStyle().Foreground(lipgloss.Color("#10b981"))
	tuiThinSep      = lipgloss.NewStyle().Foreground(lipgloss.Color("#1e293b"))
	tuiCmdOkStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#34d399"))
	tuiCmdErrStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#f87171"))
	tuiPromptStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#10b981"))
	tuiErrStyle     = tuiCmdErrStyle
)

// Fixed rows: title + ━sep + onion + ─sep + ─sep + prompt = 6
const (
	tuiFixedRows = 6
	tuiMaxLines  = 1000
)

// ── Model ─────────────────────────────────────────────────────────────────────

type tuiModel struct {
	store     *storage.SQLiteStore
	cancel    func()
	logCh     <-chan string
	onionAddr string
	startTime time.Time
	cfgPath   string // for config/logs commands

	memberCount int
	pendingMsgs int64
	sizeBytes   int64

	viewport viewport.Model
	input    textinput.Model
	ready    bool
	width    int
	height   int

	logLines []string // pre-colored rendered lines
}

func newTUIModel(onionAddr string, store *storage.SQLiteStore, cancel func(), logCh <-chan string, cfgPath string) tuiModel {
	ti := textinput.New()
	ti.Placeholder = "help  members  invite  status  quit"
	ti.Prompt = "  ❯ "
	ti.PromptStyle = tuiPromptStyle
	ti.CharLimit = 200
	ti.Focus()

	return tuiModel{
		onionAddr: onionAddr,
		startTime: time.Now(),
		store:     store,
		cancel:    cancel,
		logCh:     logCh,
		cfgPath:   cfgPath,
		input:     ti,
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(
		tuiWaitForLog(m.logCh),
		tuiSecondTick(),
		tuiFetchStats(m.store),
	)
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		vpH := msg.Height - tuiFixedRows
		if vpH < 1 {
			vpH = 1
		}
		if !m.ready {
			m.viewport = viewport.New(msg.Width, vpH)
			m.viewport.SetContent(strings.Join(m.logLines, "\n"))
			m.viewport.GotoBottom()
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = vpH
		}

	case tuiLogLineMsg:
		m.logLines = append(m.logLines, tuiColorizeLog(string(msg)))
		if len(m.logLines) > tuiMaxLines {
			m.logLines = m.logLines[len(m.logLines)-tuiMaxLines:]
		}
		if m.ready {
			atBottom := m.viewport.AtBottom()
			m.viewport.SetContent(strings.Join(m.logLines, "\n"))
			if atBottom {
				m.viewport.GotoBottom()
			}
		}
		cmds = append(cmds, tuiWaitForLog(m.logCh))

	case tuiStatsMsg:
		m.memberCount = msg.members
		m.pendingMsgs = msg.pending
		m.sizeBytes = msg.sizeBytes
		cmds = append(cmds, tuiScheduleStats(m.store))

	case tuiSecondTickMsg:
		cmds = append(cmds, tuiSecondTick())

	case tea.KeyMsg:
		switch msg.Type {

		case tea.KeyCtrlC:
			m.cancel()
			return m, tea.Quit

		// Viewport navigation — handled manually (viewport never gets KeyMsg directly)
		case tea.KeyPgUp:
			if m.ready {
				var vpCmd tea.Cmd
				m.viewport, vpCmd = m.viewport.Update(msg)
				cmds = append(cmds, vpCmd)
			}
		case tea.KeyPgDown:
			if m.ready {
				var vpCmd tea.Cmd
				m.viewport, vpCmd = m.viewport.Update(msg)
				cmds = append(cmds, vpCmd)
			}
		case tea.KeyEnd:
			if m.ready {
				m.viewport.GotoBottom()
			}
		case tea.KeyHome:
			if m.ready {
				m.viewport.GotoTop()
			}

		case tea.KeyEnter:
			raw := strings.TrimSpace(m.input.Value())
			m.input.SetValue("")
			if raw != "" {
				result, isErr, quit := m.execCommand(raw)
				if quit {
					m.cancel()
					return m, tea.Quit
				}
				if result != "" {
					var rendered string
					if isErr {
						rendered = tuiCmdErrStyle.Render("  ✗ " + result)
					} else {
						rendered = tuiCmdOkStyle.Render("  ◀ " + result)
					}
					m.logLines = append(m.logLines, rendered)
					if m.ready {
						m.viewport.SetContent(strings.Join(m.logLines, "\n"))
						m.viewport.GotoBottom()
					}
				}
				cmds = append(cmds, tuiFetchStats(m.store))
			}

		default:
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			cmds = append(cmds, inputCmd)
		}
	}

	// Forward non-key messages to viewport (log updates, window resize, mouse).
	// Key messages are handled above — never pass them to the viewport or it
	// will intercept PgUp/PgDown/Enter before the input does.
	if m.ready {
		if _, isKey := msg.(tea.KeyMsg); !isKey {
			var vpCmd tea.Cmd
			m.viewport, vpCmd = m.viewport.Update(msg)
			cmds = append(cmds, vpCmd)
		}
	}

	return m, tea.Batch(cmds...)
}

func (m tuiModel) View() string {
	if !m.ready {
		return "\n  Starting Fialka Mailbox…\n"
	}

	w := m.width
	uptime := tuiFormatUptime(time.Since(m.startTime).Round(time.Second))

	// ── Title line: brand left | stats right ──────────────────────────────────
	left := tuiBrandStyle.Render("  🔐 FIALKA MAILBOX ") +
		tuiVerStyle.Render("v0.2.0") +
		tuiDotStyle.Render("  ●  UP ") +
		tuiUptimeStyle.Render(uptime)

	right := tuiStatNumStyle.Render(strconv.Itoa(m.memberCount)) +
		tuiStatLblStyle.Render(" members  ·  ") +
		tuiStatNumStyle.Render(strconv.FormatInt(m.pendingMsgs, 10)) +
		tuiStatLblStyle.Render(" msgs  ·  ") +
		tuiStatLblStyle.Render(tuiFormatBytes(m.sizeBytes)) + "  "

	pad := w - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	titleLine := left + strings.Repeat(" ", pad) + right

	// ── Separators ────────────────────────────────────────────────────────────
	thickSep := tuiThickSep.Render(strings.Repeat("━", w))
	thinSep := tuiThinSep.Render(strings.Repeat("─", w))

	// ── Onion line ────────────────────────────────────────────────────────────
	var onionLine string
	if m.onionAddr != "" {
		onionLine = tuiOnionStyle.Render("  " + m.onionAddr)
	} else {
		onionLine = tuiStatLblStyle.Render("  Tor unavailable — no .onion address")
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		titleLine,
		thickSep,
		onionLine,
		thinSep,
		m.viewport.View(),
		thinSep,
		m.input.View(),
	)
}

// ── Command executor ──────────────────────────────────────────────────────────

func (m *tuiModel) execCommand(raw string) (result string, isErr bool, quit bool) {
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return "", false, false
	}
	cmd := strings.ToLower(parts[0])
	args := parts[1:]

	switch cmd {

	// ── help ─────────────────────────────────────────────────────────────────
	case "help", "h", "?":
		lines := []string{
			"  Available commands:",
			"    status               — server stats",
			"    members              — list members",
			"    kick <hash>          — remove a member (prefix ok)",
			"    init                 — create owner bootstrap invite",
			"    invite [days=7]      — create a member invite link",
			"    invites              — list all active invites",
			"    revoke <token>       — revoke an invite (prefix ok)",
			"    config               — show config file path",
			"    logs                 — show log file path",
			"    clear                — clear viewport",
			"    quit                 — stop server and exit",
		}
		for _, l := range lines {
			m.logLines = append(m.logLines, tuiCmdOkStyle.Render(l))
		}
		if m.ready {
			m.viewport.SetContent(strings.Join(m.logLines, "\n"))
			m.viewport.GotoBottom()
		}
		return "", false, false

	// ── status ───────────────────────────────────────────────────────────────
	case "status":
		stats, err := m.store.Stats()
		if err != nil {
			return "error: " + err.Error(), true, false
		}
		onion, _ := m.store.GetMeta("onion_address")
		if onion == "" {
			onion = "(not set)"
		}
		lines := []string{
			fmt.Sprintf("  onion    : %s", onion),
			fmt.Sprintf("  pending  : %d messages", stats.PendingMessages),
			fmt.Sprintf("  inboxes  : %d recipients", stats.Recipients),
			fmt.Sprintf("  storage  : %s", tuiFormatBytes(stats.TotalSizeBytes)),
			fmt.Sprintf("  uptime   : %s", tuiFormatUptime(time.Since(m.startTime).Round(time.Second))),
		}
		for _, l := range lines {
			m.logLines = append(m.logLines, tuiCmdOkStyle.Render(l))
		}
		if m.ready {
			m.viewport.SetContent(strings.Join(m.logLines, "\n"))
			m.viewport.GotoBottom()
		}
		return "", false, false

	// ── members ───────────────────────────────────────────────────────────────
	case "members", "list", "ls":
		members, err := m.store.ListMembers()
		if err != nil {
			return "error: " + err.Error(), true, false
		}
		if len(members) == 0 {
			return "no members yet — run: init", false, false
		}
		for i, mem := range members {
			hash := mem.PubkeyHash
			if len(hash) > 32 {
				hash = hash[:16] + "…" + hash[len(hash)-8:]
			}
			name := mem.DisplayName
			if name == "" {
				name = "(unnamed)"
			}
			role := ""
			if mem.Role == "owner" {
				role = " [owner]"
			}
			joined := time.Unix(mem.JoinedAt, 0).Format("2006-01-02")
			line := fmt.Sprintf("  [%d] %-18s  %-14s  %s%s", i+1, hash, name, joined, role)
			m.logLines = append(m.logLines, tuiCmdOkStyle.Render(line))
		}
		if m.ready {
			m.viewport.SetContent(strings.Join(m.logLines, "\n"))
			m.viewport.GotoBottom()
		}
		return "", false, false

	// ── kick ──────────────────────────────────────────────────────────────────
	case "kick", "remove", "rm":
		if len(args) == 0 {
			return "usage: kick <hash_prefix>", true, false
		}
		prefix := args[0]
		members, err := m.store.ListMembers()
		if err != nil {
			return "error: " + err.Error(), true, false
		}
		var target *storage.Member
		for _, mem := range members {
			if strings.HasPrefix(mem.PubkeyHash, prefix) {
				target = mem
				break
			}
		}
		if target == nil {
			return fmt.Sprintf("no member matching %q", prefix), true, false
		}
		if target.Role == "owner" {
			return "cannot kick the owner", true, false
		}
		if err := m.store.RemoveMember(target.PubkeyHash); err != nil {
			return "error: " + err.Error(), true, false
		}
		return fmt.Sprintf("✓ kicked %s", target.PubkeyHash[:16]+"…"), false, false

	// ── init ──────────────────────────────────────────────────────────────────
	case "init":
		if hasOwner, _ := m.store.HasOwner(); hasOwner {
			return "owner already exists — use: invite", true, false
		}
		// Reuse existing unused owner invite if any
		invites, _ := m.store.ListInvites()
		for _, inv := range invites {
			if inv.Role == "owner" && inv.UseCount < inv.MaxUses {
				onion, _ := m.store.GetMeta("onion_address")
				link := mbBuildLink(onion, inv.Token)
				lines := []string{
					"  existing owner invite:",
					"    token : " + inv.Token,
					"    link  : " + link,
				}
				for _, l := range lines {
					m.logLines = append(m.logLines, tuiCmdOkStyle.Render(l))
				}
				if m.ready {
					m.viewport.SetContent(strings.Join(m.logLines, "\n"))
					m.viewport.GotoBottom()
				}
				return "", false, false
			}
		}
		token, err := storage.GenerateToken()
		if err != nil {
			return "error: " + err.Error(), true, false
		}
		inv := &storage.Invite{Token: token, Role: "owner", MaxUses: 1}
		if err := m.store.CreateInvite(inv); err != nil {
			return "error: " + err.Error(), true, false
		}
		onion, _ := m.store.GetMeta("onion_address")
		link := mbBuildLink(onion, token)
		lines := []string{
			"  ✓ owner bootstrap invite created:",
			"    token : " + token,
			"    link  : " + link,
			"  single-use. share this link with the owner to join.",
		}
		for _, l := range lines {
			m.logLines = append(m.logLines, tuiCmdOkStyle.Render(l))
		}
		if m.ready {
			m.viewport.SetContent(strings.Join(m.logLines, "\n"))
			m.viewport.GotoBottom()
		}
		return "", false, false

	// ── invite ────────────────────────────────────────────────────────────────
	case "invite":
		days := 7
		if len(args) > 0 {
			if n, err := strconv.Atoi(args[0]); err == nil && n >= 0 {
				days = n
			}
		}
		if hasOwner, _ := m.store.HasOwner(); !hasOwner {
			return "no owner yet — run: init", true, false
		}
		token, err := storage.GenerateToken()
		if err != nil {
			return "error: " + err.Error(), true, false
		}
		var expiresAt int64
		expDesc := "never"
		if days > 0 {
			expiresAt = time.Now().Add(time.Duration(days) * 24 * time.Hour).Unix()
			expDesc = fmt.Sprintf("%d days", days)
		}
		inv := &storage.Invite{Token: token, Role: "member", MaxUses: 1, ExpiresAt: expiresAt}
		if err := m.store.CreateInvite(inv); err != nil {
			return "error: " + err.Error(), true, false
		}
		onion, _ := m.store.GetMeta("onion_address")
		link := mbBuildLink(onion, token)
		lines := []string{
			fmt.Sprintf("  ✓ member invite (1-use, expires: %s):", expDesc),
			"    " + link,
		}
		for _, l := range lines {
			m.logLines = append(m.logLines, tuiCmdOkStyle.Render(l))
		}
		if m.ready {
			m.viewport.SetContent(strings.Join(m.logLines, "\n"))
			m.viewport.GotoBottom()
		}
		return "", false, false

	// ── invites ───────────────────────────────────────────────────────────────
	case "invites":
		invites, err := m.store.ListInvites()
		if err != nil {
			return "error: " + err.Error(), true, false
		}
		if len(invites) == 0 {
			return "no invites — run: init or invite", false, false
		}
		for _, inv := range invites {
			tok := inv.Token
			if len(tok) > 16 {
				tok = tok[:16] + "…"
			}
			exp := "never"
			if inv.ExpiresAt > 0 {
				if time.Now().Unix() > inv.ExpiresAt {
					exp = "EXPIRED"
				} else {
					exp = time.Unix(inv.ExpiresAt, 0).Format("2006-01-02")
				}
			}
			line := fmt.Sprintf("  %-10s  uses:%d/%d  exp:%-12s  %s",
				inv.Role, inv.UseCount, inv.MaxUses, exp, tok)
			m.logLines = append(m.logLines, tuiCmdOkStyle.Render(line))
		}
		if m.ready {
			m.viewport.SetContent(strings.Join(m.logLines, "\n"))
			m.viewport.GotoBottom()
		}
		return "", false, false

	// ── revoke ────────────────────────────────────────────────────────────────
	case "revoke":
		if len(args) == 0 {
			return "usage: revoke <token_prefix>", true, false
		}
		prefix := args[0]
		invites, err := m.store.ListInvites()
		if err != nil {
			return "error: " + err.Error(), true, false
		}
		var target *storage.Invite
		for _, inv := range invites {
			if strings.HasPrefix(inv.Token, prefix) {
				target = inv
				break
			}
		}
		if target == nil {
			return fmt.Sprintf("no invite matching %q", prefix), true, false
		}
		if err := m.store.RevokeInvite(target.Token); err != nil {
			return "error: " + err.Error(), true, false
		}
		short := target.Token
		if len(short) > 16 {
			short = short[:16] + "…"
		}
		return fmt.Sprintf("✓ revoked invite %s", short), false, false

	// ── config ────────────────────────────────────────────────────────────────
	case "config":
		p := m.cfgPath
		if p == "" {
			home, _ := os.UserHomeDir()
			p = home + "/.config/fialka-mailbox/config.toml"
		}
		return "config: " + p, false, false

	// ── logs ──────────────────────────────────────────────────────────────────
	case "logs":
		p := m.cfgPath
		if p == "" {
			home, _ := os.UserHomeDir()
			p = home + "/.config/fialka-mailbox"
		} else {
			p = strings.TrimSuffix(p, "/config.toml")
		}
		return "log file: " + p + "/fialka-mailbox.log", false, false

	// ── clear ─────────────────────────────────────────────────────────────────
	case "clear":
		m.logLines = nil
		if m.ready {
			m.viewport.SetContent("")
		}
		return "", false, false

	// ── quit ──────────────────────────────────────────────────────────────────
	case "quit", "q", "exit", "stop":
		return "", false, true

	default:
		return fmt.Sprintf("unknown: %q — type 'help'", cmd), true, false
	}
}

// ── Formatting helpers ────────────────────────────────────────────────────────

func tuiFormatUptime(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

func tuiFormatBytes(b int64) string {
	switch {
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
