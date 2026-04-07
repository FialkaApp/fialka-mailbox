package cmd

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/fialkaapp/fialka-mailbox/internal/config"
	"github.com/fialkaapp/fialka-mailbox/internal/storage"
	"github.com/spf13/cobra"
)

// ── Styles ─────────────────────────────────────────────────────────────────────

var (
	mbBaseStyle    = lipgloss.NewStyle().BorderStyle(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240"))
	mbOwnerStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	mbErrorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	mbSuccessStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	mbDimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	mbTitleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
)

// ── Config helper ───────────────────────────────────────────────────────────────

func openStoreFromConfig(cfgFile string) (*storage.SQLiteStore, *config.Config, error) {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return nil, nil, fmt.Errorf("loading config: %w", err)
	}
	store, err := storage.NewSQLiteStore(cfg.Storage.DBPath)
	if err != nil {
		return nil, nil, fmt.Errorf("opening database %s: %w", cfg.Storage.DBPath, err)
	}
	return store, cfg, nil
}

var mbCfgPath string

// ── mailboxCmd ──────────────────────────────────────────────────────────────────

var mailboxCmd = &cobra.Command{
	Use:   "mailbox",
	Short: "Manage the Fialka Mailbox (members, invites, info)",
}

func init() {
	mailboxCmd.PersistentFlags().StringVarP(&mbCfgPath, "config", "c", "", "config file (default: ~/.config/fialka-mailbox/config.toml)")
	mailboxCmd.AddCommand(mailboxInfoCmd)
	mailboxCmd.AddCommand(mailboxInitCmd)
	mailboxCmd.AddCommand(mailboxMembersCmd)
	mailboxCmd.AddCommand(mailboxInviteCmd)
	mailboxCmd.AddCommand(mailboxInvitesCmd)
}

// ── fialka mailbox info ─────────────────────────────────────────────────────────

var mailboxInfoCmd = &cobra.Command{
	Use:   "info",
	Short: "Show mailbox status",
	RunE: func(cmd *cobra.Command, args []string) error {
		store, _, err := openStoreFromConfig(mbCfgPath)
		if err != nil {
			return err
		}
		defer store.Close()

		hasOwner, _ := store.HasOwner()
		onion, _ := store.GetMeta("onion_address")
		members, _ := store.ListMembers()
		stats, _ := store.Stats()

		if onion == "" {
			onion = mbDimStyle.Render("(not set — start daemon first)")
		}

		fmt.Println()
		fmt.Println(mbTitleStyle.Render("  Fialka Mailbox — Info"))
		fmt.Println()
		fmt.Printf("  Onion address  : %s\n", onion)
		fmt.Printf("  Owner set      : %v\n", hasOwner)
		fmt.Printf("  Members        : %d\n", len(members))
		if stats != nil {
			fmt.Printf("  Pending msgs   : %d\n", stats.PendingMessages)
			fmt.Printf("  Storage used   : %.1f KB\n", float64(stats.TotalSizeBytes)/1024)
		}
		fmt.Println()
		return nil
	},
}

// ── fialka mailbox init ─────────────────────────────────────────────────────────

var mailboxInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Create the owner bootstrap invite (first join = owner)",
	Long: `Creates a one-time invite with role=owner.
The first person to use this link becomes the mailbox owner.
After that, the owner creates regular member invites from the Fialka app.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		store, _, err := openStoreFromConfig(mbCfgPath)
		if err != nil {
			return err
		}
		defer store.Close()

		if hasOwner, _ := store.HasOwner(); hasOwner {
			return fmt.Errorf("an owner already exists — use 'fialka mailbox invite' for new members")
		}

		// Reuse existing unused owner invite
		invites, _ := store.ListInvites()
		for _, inv := range invites {
			if inv.Role == "owner" && inv.UseCount < inv.MaxUses {
				onion, _ := store.GetMeta("onion_address")
				fmt.Println()
				fmt.Println(mbSuccessStyle.Render("  Existing owner invite:"))
				mbPrintInvite(inv, mbBuildLink(onion, inv.Token))
				return nil
			}
		}

		token, err := storage.GenerateToken()
		if err != nil {
			return err
		}
		invite := &storage.Invite{Token: token, Role: "owner", MaxUses: 1}
		if err := store.CreateInvite(invite); err != nil {
			return err
		}

		onion, _ := store.GetMeta("onion_address")
		fmt.Println()
		fmt.Println(mbSuccessStyle.Render("  ✓ Owner bootstrap invite created:"))
		mbPrintInvite(invite, mbBuildLink(onion, token))
		fmt.Println(mbDimStyle.Render("  Single-use. After the owner joins, use the app to invite members."))
		fmt.Println()
		return nil
	},
}

// ── fialka mailbox invite ───────────────────────────────────────────────────────

var mailboxInviteCmd = &cobra.Command{
	Use:   "invite",
	Short: "Create a member invite (server-admin shortcut)",
	Long: `Creates an invite directly in the DB — no Ed25519 owner auth required.
For owner-signed invites (from the Android app), use POST /mailbox/invite.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		store, _, err := openStoreFromConfig(mbCfgPath)
		if err != nil {
			return err
		}
		defer store.Close()

		if hasOwner, _ := store.HasOwner(); !hasOwner {
			return fmt.Errorf("no owner yet — run 'fialka mailbox init' first")
		}

		var role, maxUsesStr, expiresDaysStr string
		role = "member"
		maxUsesStr = "1"
		expiresDaysStr = "7"

		form := huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title("Role").
					Options(
						huh.NewOption("Member", "member"),
						huh.NewOption("Owner (transfer ownership)", "owner"),
					).
					Value(&role),

				huh.NewInput().
					Title("Max uses").
					Placeholder("1").
					Value(&maxUsesStr).
					Validate(func(s string) error {
						var n int
						if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n < 1 {
							return fmt.Errorf("must be ≥ 1")
						}
						return nil
					}),

				huh.NewInput().
					Title("Expires in days (0 = never)").
					Placeholder("7").
					Value(&expiresDaysStr).
					Validate(func(s string) error {
						var n int
						if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n < 0 {
							return fmt.Errorf("must be ≥ 0")
						}
						return nil
					}),
			),
		)

		if err := form.Run(); err != nil {
			return fmt.Errorf("aborted")
		}

		if role == "owner" {
			if hasOwner, _ := store.HasOwner(); hasOwner {
				return fmt.Errorf("owner already exists")
			}
		}

		var maxUses, expiresDays int
		fmt.Sscanf(maxUsesStr, "%d", &maxUses)
		fmt.Sscanf(expiresDaysStr, "%d", &expiresDays)
		if maxUses < 1 {
			maxUses = 1
		}
		var expiresAt int64
		if expiresDays > 0 {
			expiresAt = time.Now().Add(time.Duration(expiresDays) * 24 * time.Hour).Unix()
		}

		token, err := storage.GenerateToken()
		if err != nil {
			return err
		}
		invite := &storage.Invite{Token: token, Role: role, MaxUses: maxUses, ExpiresAt: expiresAt}
		if err := store.CreateInvite(invite); err != nil {
			return err
		}

		onion, _ := store.GetMeta("onion_address")
		fmt.Println()
		fmt.Println(mbSuccessStyle.Render("  ✓ Invite created:"))
		mbPrintInvite(invite, mbBuildLink(onion, token))
		return nil
	},
}

// ── fialka mailbox members (TUI) ───────────────────────────────────────────────

var mailboxMembersCmd = &cobra.Command{
	Use:   "members",
	Short: "View and manage members (TUI)",
	RunE: func(cmd *cobra.Command, args []string) error {
		store, _, err := openStoreFromConfig(mbCfgPath)
		if err != nil {
			return err
		}
		defer store.Close()

		members, err := store.ListMembers()
		if err != nil {
			return err
		}
		p := tea.NewProgram(newMembersModel(store, members), tea.WithAltScreen())
		_, err = p.Run()
		return err
	},
}

type membersModel struct {
	store      *storage.SQLiteStore
	table      table.Model
	members    []*storage.Member
	confirming string
	statusMsg  string
	statusErr  bool
	quitting   bool
}

func newMembersModel(store *storage.SQLiteStore, members []*storage.Member) membersModel {
	cols := []table.Column{
		{Title: "ROLE", Width: 8},
		{Title: "DISPLAY NAME", Width: 22},
		{Title: "HASH (16 chars)", Width: 18},
		{Title: "JOINED", Width: 20},
	}
	t := table.New(
		table.WithColumns(cols),
		table.WithRows(membersToRows(members)),
		table.WithFocused(true),
		table.WithHeight(12),
	)
	mbApplyTableStyles(&t)
	return membersModel{store: store, table: t, members: members}
}

func membersToRows(members []*storage.Member) []table.Row {
	rows := make([]table.Row, 0, len(members))
	for _, m := range members {
		name := m.DisplayName
		if name == "" {
			name = "(unnamed)"
		}
		hash := m.PubkeyHash
		if len(hash) > 16 {
			hash = hash[:16] + "…"
		}
		rows = append(rows, table.Row{m.Role, name, hash, time.Unix(m.JoinedAt, 0).Format("2006-01-02 15:04")})
	}
	return rows
}

func (m membersModel) Init() tea.Cmd { return nil }

func (m membersModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, isKey := msg.(tea.KeyMsg)

	if m.confirming != "" && isKey {
		switch keyMsg.String() {
		case "y", "Y":
			if err := m.store.RemoveMember(m.confirming); err != nil {
				m.statusMsg = "Error: " + err.Error()
				m.statusErr = true
			} else {
				members, _ := m.store.ListMembers()
				m.members = members
				m.table.SetRows(membersToRows(members))
				m.statusMsg = "Member kicked."
				m.statusErr = false
			}
			m.confirming = ""
		case "n", "N", "esc":
			m.confirming = ""
			m.statusMsg = ""
		}
		return m, nil
	}

	if isKey {
		switch keyMsg.String() {
		case "q", "esc", "ctrl+c":
			m.quitting = true
			return m, tea.Quit

		case "k", "K":
			row := m.table.SelectedRow()
			if row == nil {
				return m, nil
			}
			prefix := strings.TrimSuffix(row[2], "…")
			for _, mem := range m.members {
				if strings.HasPrefix(mem.PubkeyHash, prefix) {
					if mem.Role == "owner" {
						m.statusMsg = "Cannot kick the owner."
						m.statusErr = true
						return m, nil
					}
					m.confirming = mem.PubkeyHash
					name := mem.DisplayName
					if name == "" {
						name = prefix + "…"
					}
					m.statusMsg = fmt.Sprintf("Kick %q? [y/n]", name)
					m.statusErr = false
					return m, nil
				}
			}

		case "r", "R":
			members, _ := m.store.ListMembers()
			m.members = members
			m.table.SetRows(membersToRows(members))
			m.statusMsg = "Refreshed."
			m.statusErr = false
		}
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m membersModel) View() string {
	if m.quitting {
		return ""
	}
	status := ""
	if m.statusMsg != "" {
		if m.statusErr {
			status = mbErrorStyle.Render("  ✗ "+m.statusMsg) + "\n"
		} else {
			status = mbSuccessStyle.Render("  ✓ "+m.statusMsg) + "\n"
		}
	}
	return "\n" +
		mbTitleStyle.Render("  Fialka Mailbox — Members") + "\n\n" +
		mbBaseStyle.Render(m.table.View()) + "\n" +
		mbDimStyle.Render("  ↑/↓ navigate  •  k kick  •  r refresh  •  q quit") + "\n" +
		status
}

// ── fialka mailbox invites (TUI) ───────────────────────────────────────────────

var mailboxInvitesCmd = &cobra.Command{
	Use:   "invites",
	Short: "View and revoke invite tokens (TUI)",
	RunE: func(cmd *cobra.Command, args []string) error {
		store, _, err := openStoreFromConfig(mbCfgPath)
		if err != nil {
			return err
		}
		defer store.Close()

		invites, err := store.ListInvites()
		if err != nil {
			return err
		}
		onion, _ := store.GetMeta("onion_address")
		p := tea.NewProgram(newInvitesModel(store, invites, onion), tea.WithAltScreen())
		_, err = p.Run()
		return err
	},
}

type invitesModel struct {
	store      *storage.SQLiteStore
	table      table.Model
	invites    []*storage.Invite
	onion      string
	statusMsg  string
	statusErr  bool
	showDetail bool
	detailInv  *storage.Invite
	quitting   bool
}

func newInvitesModel(store *storage.SQLiteStore, invites []*storage.Invite, onion string) invitesModel {
	cols := []table.Column{
		{Title: "ROLE", Width: 8},
		{Title: "TOKEN (16)", Width: 18},
		{Title: "USES", Width: 9},
		{Title: "EXPIRES", Width: 20},
		{Title: "CREATED", Width: 20},
	}
	t := table.New(
		table.WithColumns(cols),
		table.WithRows(invitesToRows(invites)),
		table.WithFocused(true),
		table.WithHeight(12),
	)
	mbApplyTableStyles(&t)
	return invitesModel{store: store, table: t, invites: invites, onion: onion}
}

func invitesToRows(invites []*storage.Invite) []table.Row {
	rows := make([]table.Row, 0, len(invites))
	for _, inv := range invites {
		tok := inv.Token
		if len(tok) > 16 {
			tok = tok[:16] + "…"
		}
		uses := fmt.Sprintf("%d/%d", inv.UseCount, inv.MaxUses)
		exp := "(never)"
		if inv.ExpiresAt > 0 {
			if time.Now().Unix() > inv.ExpiresAt {
				exp = "EXPIRED"
			} else {
				exp = time.Unix(inv.ExpiresAt, 0).Format("2006-01-02 15:04")
			}
		}
		rows = append(rows, table.Row{inv.Role, tok, uses, exp, time.Unix(inv.CreatedAt, 0).Format("2006-01-02 15:04")})
	}
	return rows
}

func (m invitesModel) Init() tea.Cmd { return nil }

func (m invitesModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, isKey := msg.(tea.KeyMsg)

	if m.showDetail && isKey {
		m.showDetail = false
		m.detailInv = nil
		return m, nil
	}

	if isKey {
		switch keyMsg.String() {
		case "q", "esc", "ctrl+c":
			m.quitting = true
			return m, tea.Quit

		case "enter":
			row := m.table.SelectedRow()
			if row == nil {
				return m, nil
			}
			prefix := strings.TrimSuffix(row[1], "…")
			for _, inv := range m.invites {
				if strings.HasPrefix(inv.Token, prefix) {
					m.detailInv = inv
					m.showDetail = true
					break
				}
			}

		case "d", "D", "delete":
			row := m.table.SelectedRow()
			if row == nil {
				return m, nil
			}
			prefix := strings.TrimSuffix(row[1], "…")
			for _, inv := range m.invites {
				if strings.HasPrefix(inv.Token, prefix) {
					if err := m.store.RevokeInvite(inv.Token); err != nil {
						m.statusMsg = "Error: " + err.Error()
						m.statusErr = true
					} else {
						invites, _ := m.store.ListInvites()
						m.invites = invites
						m.table.SetRows(invitesToRows(invites))
						m.statusMsg = "Invite revoked."
						m.statusErr = false
					}
					break
				}
			}

		case "r", "R":
			invites, _ := m.store.ListInvites()
			m.invites = invites
			m.table.SetRows(invitesToRows(invites))
			m.statusMsg = "Refreshed."
			m.statusErr = false
		}
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m invitesModel) View() string {
	if m.quitting {
		return ""
	}
	if m.showDetail && m.detailInv != nil {
		inv := m.detailInv
		link := mbBuildLink(m.onion, inv.Token)
		exp := "(never)"
		if inv.ExpiresAt > 0 {
			exp = time.Unix(inv.ExpiresAt, 0).Format("2006-01-02 15:04 UTC")
		}
		return fmt.Sprintf("\n  %s\n\n  Token   : %s\n  Role    : %s\n  Uses    : %d / %d\n  Expires : %s\n  Created : %s\n\n  Link    :\n    %s\n\n  (any key to go back)\n",
			mbTitleStyle.Render("Invite Detail"),
			inv.Token,
			mbOwnerStyle.Render(inv.Role),
			inv.UseCount, inv.MaxUses,
			exp,
			time.Unix(inv.CreatedAt, 0).Format("2006-01-02 15:04"),
			link,
		)
	}

	status := ""
	if m.statusMsg != "" {
		if m.statusErr {
			status = mbErrorStyle.Render("  ✗ "+m.statusMsg) + "\n"
		} else {
			status = mbSuccessStyle.Render("  ✓ "+m.statusMsg) + "\n"
		}
	}
	return "\n" +
		mbTitleStyle.Render("  Fialka Mailbox — Invites") + "\n\n" +
		mbBaseStyle.Render(m.table.View()) + "\n" +
		mbDimStyle.Render("  ↑/↓ navigate  •  enter detail  •  d revoke  •  r refresh  •  q quit") + "\n" +
		status
}

// ── Shared helpers ──────────────────────────────────────────────────────────────

func mbApplyTableStyles(t *table.Model) {
	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("240")).
		Bold(true)
	s.Selected = s.Selected.
		Foreground(lipgloss.Color("229")).
		Background(lipgloss.Color("57")).
		Bold(true)
	t.SetStyles(s)
}

func mbBuildLink(onion, token string) string {
	if onion == "" {
		onion = "<start-daemon-first>"
	}
	return fmt.Sprintf("fialka-mailbox://%s/join/%s", onion, token)
}

func mbPrintInvite(inv *storage.Invite, link string) {
	exp := mbDimStyle.Render("(never)")
	if inv.ExpiresAt > 0 {
		exp = time.Unix(inv.ExpiresAt, 0).Format("2006-01-02 15:04")
	}
	fmt.Println()
	fmt.Printf("  Token : %s\n", inv.Token)
	fmt.Printf("  Role  : %s\n", mbOwnerStyle.Render(inv.Role))
	fmt.Printf("  Uses  : %d / %d\n", inv.UseCount, inv.MaxUses)
	fmt.Printf("  Exp   : %s\n", exp)
	fmt.Println()
	fmt.Printf("  Link  : %s\n", link)
	fmt.Println()
}

// Suppress "imported and not used" for packages imported transitively.
var (
	_ = sql.ErrNoRows
	_ = errors.New
)
