package cmd

import (
	"fmt"
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
	tuiTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e2e8f0"))
	tuiAccStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#10b981"))
	tuiOnionStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#a78bfa"))
	tuiDimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6b7280"))
	tuiWarnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#fbbf24"))
	tuiErrStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#f87171"))
	tuiPromptStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#10b981"))
)

// Layout constants (number of terminal rows used by fixed UI elements).
const (
	tuiHeaderH  = 3 // title row + onion row + separator
	tuiFooterH  = 5 // separator + stats + separator + cmdResult + prompt
	tuiMaxLines = 1000
)

// ── Model ─────────────────────────────────────────────────────────────────────

type tuiModel struct {
	// server handles
	store     *storage.SQLiteStore
	cancel    func()
	logCh     <-chan string
	onionAddr string
	startTime time.Time

	// stats
	memberCount int
	pendingMsgs int64
	sizeBytes   int64

	// UI components
	viewport viewport.Model
	input    textinput.Model
	ready    bool
	width    int
	height   int

	// log buffer
	logLines []string

	// command feedback (one-line, shown above prompt)
	cmdResult string
	cmdIsErr  bool
}

func newTUIModel(onionAddr string, store *storage.SQLiteStore, cancel func(), logCh <-chan string) tuiModel {
	ti := textinput.New()
	ti.Placeholder = "help · members · invite · status · quit"
	ti.Prompt = "❯ "
	ti.PromptStyle = tuiPromptStyle
	ti.CharLimit = 200
	ti.Focus()

	return tuiModel{
		onionAddr: onionAddr,
		startTime: time.Now(),
		store:     store,
		cancel:    cancel,
		logCh:     logCh,
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
		vpH := msg.Height - tuiHeaderH - tuiFooterH
		if vpH < 3 {
			vpH = 3
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
		m.logLines = append(m.logLines, string(msg))
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
				m.cmdResult = result
				m.cmdIsErr = isErr
				if quit {
					m.cancel()
					return m, tea.Quit
				}
				// Refresh stats after any command that might change state
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
	uptime := time.Since(m.startTime).Round(time.Second)

	// ── Header (3 lines) ──────────────────────────────────────────────────────
	titleLine := tuiTitleStyle.Render("  🔐 FIALKA MAILBOX ") +
		tuiAccStyle.Render("v0.2.0") +
		tuiDimStyle.Render("  ●  UP ") +
		tuiAccStyle.Render(tuiFormatUptime(uptime))

	var onionLine string
	if m.onionAddr != "" {
		onionLine = "  " + tuiOnionStyle.Render(m.onionAddr)
	} else {
		onionLine = "  " + tuiDimStyle.Render("(Tor unavailable — server running without .onion)")
	}

	sep := tuiDimStyle.Render(strings.Repeat("─", w))

	// ── Stats line ────────────────────────────────────────────────────────────
	statsLine := fmt.Sprintf("  %s members  ·  %s pending msgs  ·  %s",
		tuiAccStyle.Render(strconv.Itoa(m.memberCount)),
		tuiAccStyle.Render(strconv.FormatInt(m.pendingMsgs, 10)),
		tuiDimStyle.Render(tuiFormatBytes(m.sizeBytes)),
	)

	// ── Command result (one line, ALWAYS 1 row — never collapse to 0) ────────
	var resultLine string
	if m.cmdResult != "" {
		style := tuiWarnStyle
		if m.cmdIsErr {
			style = tuiErrStyle
		}
		resultLine = style.Render("  " + m.cmdResult)
	} else {
		resultLine = " " // reserve the row so layout never shifts
	}

	// ── Assemble ──────────────────────────────────────────────────────────────
	return lipgloss.JoinVertical(lipgloss.Left,
		titleLine,           // 1
		onionLine,           // 2
		sep,                 // 3
		m.viewport.View(),   // vpH lines
		sep,                 // +1
		statsLine,           // +1
		sep,                 // +1
		resultLine,          // +1
		"  "+m.input.View(), // +1
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

	case "help", "h", "?":
		return "Commands: help · members · invite [days=7] · status · clear · quit", false, false

	case "members", "list", "ls":
		members, err := m.store.ListMembers()
		if err != nil {
			return "error: " + err.Error(), true, false
		}
		if len(members) == 0 {
			return "No members yet — run: fialka mailbox init", false, false
		}
		rows := make([]string, 0, len(members))
		for i, mem := range members {
			hash := mem.PubkeyHash
			if len(hash) > 16 {
				hash = hash[:8] + "…" + hash[len(hash)-8:]
			}
			role := ""
			if mem.Role == "owner" {
				role = " [owner]"
			}
			rows = append(rows, fmt.Sprintf("[%d] %s%s", i+1, hash, role))
		}
		return strings.Join(rows, "  "), false, false

	case "invite":
		days := 7
		if len(args) > 0 {
			if n, err := strconv.Atoi(args[0]); err == nil && n >= 0 {
				days = n
			}
		}
		if hasOwner, _ := m.store.HasOwner(); !hasOwner {
			return "no owner yet — run: fialka mailbox init", true, false
		}
		token, err := storage.GenerateToken()
		if err != nil {
			return "error generating token: " + err.Error(), true, false
		}
		var expiresAt int64
		if days > 0 {
			expiresAt = time.Now().Add(time.Duration(days) * 24 * time.Hour).Unix()
		}
		inv := &storage.Invite{Token: token, Role: "member", MaxUses: 1, ExpiresAt: expiresAt}
		if err := m.store.CreateInvite(inv); err != nil {
			return "error: " + err.Error(), true, false
		}
		onion, _ := m.store.GetMeta("onion_address")
		return fmt.Sprintf("✓ invite (1-use, %dd): %s", days, mbBuildLink(onion, token)), false, false

	case "status":
		stats, err := m.store.Stats()
		if err != nil {
			return "error: " + err.Error(), true, false
		}
		return fmt.Sprintf("pending=%d  recipients=%d  size=%s",
			stats.PendingMessages, stats.Recipients, tuiFormatBytes(stats.TotalSizeBytes)), false, false

	case "clear":
		m.logLines = nil
		m.cmdResult = ""
		if m.ready {
			m.viewport.SetContent("")
		}
		return "", false, false

	case "quit", "q", "exit", "stop":
		return "", false, true

	default:
		return fmt.Sprintf("unknown command %q — type 'help'", cmd), true, false
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
