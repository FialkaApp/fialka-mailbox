package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Désinstaller Fialka Mailbox complètement (guidé, avec confirmations)",
	Long: `Guide interactif de désinstallation complète.

Supprime :
  • Le service systemd (s'il existe)
  • Le binaire fialka
  • La configuration (/etc/fialka-mailbox/)
  • Les données (/var/lib/fialka-mailbox/ — adresse .onion, base de données)
  • L'utilisateur système fialka
  • Le dépôt Tor Project (optionnel)
  • Tor lui-même (optionnel)

Chaque suppression est confirmée individuellement.`,
	RunE: runUninstall,
}

// ── Styles ────────────────────────────────────────────────────────────────────

var (
	unBold    = lipgloss.NewStyle().Bold(true)
	unRed     = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	unGreen   = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	unYellow  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	unCyan    = lipgloss.NewStyle().Foreground(lipgloss.Color("51"))
	unDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

func unStep(title string) {
	fmt.Printf("\n%s\n", unCyan.Render("  ━━ "+title+" ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"))
}

func unOk(msg string)   { fmt.Printf("      %s  %s\n", unGreen.Render("✓"), msg) }
func unWarn(msg string) { fmt.Printf("      %s   %s\n", unYellow.Render("⚠"), msg) }
func unInfo(msg string) { fmt.Printf("      %s  %s\n", unDim.Render("→"), msg) }
func unHr()             { fmt.Println(unDim.Render("  ────────────────────────────────────────────────")) }

// confirm asks a y/n question and returns true if the user confirms.
func unConfirm(question string) bool {
	fmt.Printf("\n  %s %s [y/N] ", unBold.Render("▶"), question)
	var answer string
	fmt.Scanln(&answer) //nolint:errcheck
	return strings.ToLower(strings.TrimSpace(answer)) == "y"
}

// ── Main ──────────────────────────────────────────────────────────────────────

func runUninstall(cmd *cobra.Command, args []string) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("désinstallation non supportée sur Windows")
	}

	// Only root on Linux can remove system files
	if os.Geteuid() != 0 {
		fmt.Println()
		fmt.Println(unRed.Render("  [✗] Ce commande doit être exécutée en tant que root."))
		fmt.Println()
		fmt.Println("  Relancez avec :")
		fmt.Printf("    %s\n\n", unCyan.Render("sudo fialka uninstall"))
		return fmt.Errorf("droits insuffisants")
	}

	// ── Banner ──────────────────────────────────────────────
	fmt.Println()
	fmt.Println(unRed.Render("  ╔══════════════════════════════════════════════════════╗"))
	fmt.Println(unRed.Render("  ║       Fialka Mailbox — Désinstallation guidée        ║"))
	fmt.Println(unRed.Render("  ╚══════════════════════════════════════════════════════╝"))
	fmt.Println()
	fmt.Println("  Cette opération va supprimer Fialka Mailbox de ce serveur.")
	fmt.Println()
	fmt.Println(unBold.Render("  Sera proposé à la suppression :"))
	fmt.Println("    • Service systemd   fialka-mailbox")
	fmt.Println("    • Binaire           /usr/local/bin/fialka")
	fmt.Println("    • Configuration     /etc/fialka-mailbox/")
	fmt.Println("    • Données           /var/lib/fialka-mailbox/  " + unRed.Render("(irreversible — inclut l'adresse .onion)"))
	fmt.Println("    • Utilisateur       fialka")
	fmt.Println("    • Tor               (optionnel)")
	fmt.Println()
	unHr()
	fmt.Println()

	if !unConfirm("Commencer la procédure de désinstallation ?") {
		fmt.Println("\n  " + unGreen.Render("Annulé. Rien n'a été supprimé."))
		fmt.Println()
		return nil
	}

	removed := []string{}
	skipped := []string{}

	// ════════════════════════════════════════════════════════
	//  SERVICE SYSTEMD
	// ════════════════════════════════════════════════════════
	unStep("1 / 6  —  Service systemd")
	serviceFile := "/etc/systemd/system/fialka-mailbox.service"

	if fileExists(serviceFile) {
		fmt.Println()
		svcStatus := systemctlIsActive("fialka-mailbox")
		fmt.Printf("  Statut actuel : %s\n", statusLabel(svcStatus))

		if !unConfirm("Arrêter et supprimer le service fialka-mailbox ?") {
			unWarn("Service conservé — les autres étapes peuvent échouer si le daemon tourne encore.")
			skipped = append(skipped, "service systemd")
		} else {
			if svcStatus == "active" {
				unInfo("Arrêt du service...")
				runSilent("systemctl", "stop", "fialka-mailbox")
			}
			runSilent("systemctl", "disable", "fialka-mailbox")
			os.Remove(serviceFile) //nolint:errcheck
			runSilent("systemctl", "daemon-reload")
			unOk("Service arrêté, désactivé et supprimé")
			removed = append(removed, serviceFile)
		}
	} else {
		unInfo("Service systemd non trouvé — ignoré")
	}

	// ════════════════════════════════════════════════════════
	//  BINARY
	// ════════════════════════════════════════════════════════
	unStep("2 / 6  —  Binaire")
	binaryPath := "/usr/local/bin/fialka"

	if fileExists(binaryPath) {
		fmt.Println()
		if !unConfirm(fmt.Sprintf("Supprimer le binaire %s ?", unCyan.Render(binaryPath))) {
			unWarn("Binaire conservé.")
			skipped = append(skipped, binaryPath)
		} else {
			if err := os.Remove(binaryPath); err != nil {
				unWarn("Erreur : " + err.Error())
			} else {
				unOk("Binaire supprimé → " + binaryPath)
				removed = append(removed, binaryPath)
			}
		}
	} else {
		unInfo("Binaire non trouvé à " + binaryPath + " — ignoré")
	}

	// ════════════════════════════════════════════════════════
	//  CONFIGURATION
	// ════════════════════════════════════════════════════════
	unStep("3 / 6  —  Configuration")
	configDir := "/etc/fialka-mailbox"

	if dirExists(configDir) {
		fmt.Println()
		fmt.Printf("  Contenu de %s :\n", unCyan.Render(configDir))
		listDir(configDir)
		fmt.Println()

		if !unConfirm(fmt.Sprintf("Supprimer %s et tout son contenu ?", unCyan.Render(configDir))) {
			unWarn("Configuration conservée.")
			skipped = append(skipped, configDir)
		} else {
			if err := os.RemoveAll(configDir); err != nil {
				unWarn("Erreur : " + err.Error())
			} else {
				unOk("Configuration supprimée → " + configDir)
				removed = append(removed, configDir)
			}
		}
	} else {
		unInfo("Dossier de configuration non trouvé — ignoré")
	}

	// ════════════════════════════════════════════════════════
	//  DATA (onion key + database) — critical warning
	// ════════════════════════════════════════════════════════
	unStep("4 / 6  —  Données (base de données + clé .onion)")
	dataDir := "/var/lib/fialka-mailbox"

	if dirExists(dataDir) {
		fmt.Println()
		fmt.Println(unRed.Render("  ⚠  ATTENTION — Cette action est IRREVERSIBLE"))
		fmt.Println()
		fmt.Println("  Sera supprimé :")
		fmt.Println("    • La base de données SQLite (tous les messages en attente)")
		fmt.Println("    • Le fichier " + unBold.Render("onion.key") + " — vous " + unRed.Render("PERDREZ définitivement") + " l'adresse .onion")
		fmt.Println()
		fmt.Println(unDim.Render("  Si vous voulez conserver votre adresse .onion, sauvegardez d'abord :"))
		fmt.Println(unDim.Render("    " + filepath.Join(dataDir, "tor", "onion.key")))
		fmt.Println()

		if !unConfirm(unRed.Render("Supprimer DÉFINITIVEMENT les données " + dataDir + " ?")) {
			unWarn("Données conservées.")
			skipped = append(skipped, dataDir)
		} else {
			// Double confirmation for this destructive action
			fmt.Println()
			fmt.Print(unRed.Render("  ▶ Dernière confirmation — tapez exactement \"SUPPRIMER\" pour confirmer : "))
			var confirm string
			fmt.Scanln(&confirm) //nolint:errcheck
			if strings.TrimSpace(confirm) != "SUPPRIMER" {
				unWarn("Confirmation incorrecte — données conservées.")
				skipped = append(skipped, dataDir)
			} else {
				if err := os.RemoveAll(dataDir); err != nil {
					unWarn("Erreur : " + err.Error())
				} else {
					unOk("Données supprimées → " + dataDir)
					removed = append(removed, dataDir)
				}
			}
		}
	} else {
		unInfo("Dossier de données non trouvé — ignoré")
	}

	// Logs
	logDir := "/var/log/fialka-mailbox"
	if dirExists(logDir) {
		if unConfirm(fmt.Sprintf("Supprimer les logs %s ?", unCyan.Render(logDir))) {
			os.RemoveAll(logDir) //nolint:errcheck
			unOk("Logs supprimés → " + logDir)
			removed = append(removed, logDir)
		} else {
			skipped = append(skipped, logDir)
		}
	}

	// ════════════════════════════════════════════════════════
	//  SYSTEM USER
	// ════════════════════════════════════════════════════════
	unStep("5 / 6  —  Utilisateur système")

	if userExists("fialka") {
		fmt.Println()
		if !unConfirm("Supprimer l'utilisateur système 'fialka' ?") {
			unWarn("Utilisateur conservé.")
			skipped = append(skipped, "utilisateur fialka")
		} else {
			runSilent("userdel", "-r", "fialka")
			unOk("Utilisateur 'fialka' supprimé")
			removed = append(removed, "utilisateur:fialka")
		}
	} else {
		unInfo("Utilisateur 'fialka' non trouvé — ignoré")
	}

	// ════════════════════════════════════════════════════════
	//  TOR (optional)
	// ════════════════════════════════════════════════════════
	unStep("6 / 6  —  Tor (optionnel)")
	fmt.Println()
	fmt.Println(unDim.Render("  Tor peut être utilisé par d'autres applications sur ce serveur."))
	fmt.Println(unDim.Render("  La suppression est optionnelle et distincte du reste."))
	fmt.Println()

	removeTor := unConfirm("Supprimer Tor et son dépôt The Tor Project ?")
	if removeTor {
		unInfo("Arrêt et désactivation du service Tor...")
		runSilent("systemctl", "stop", "tor")
		runSilent("systemctl", "disable", "tor")

		if isDebianBased() {
			unInfo("Suppression des paquets tor et keyring...")
			runSilent("apt-get", "remove", "-y", "tor", "deb.torproject.org-keyring")
			runSilent("apt-get", "autoremove", "-y")

			torList := "/etc/apt/sources.list.d/tor.list"
			torKeyring := "/usr/share/keyrings/tor-archive-keyring.gpg"
			os.Remove(torList)    //nolint:errcheck
			os.Remove(torKeyring) //nolint:errcheck
			runSilent("apt-get", "update", "-qq")
			unOk("Tor et dépôt The Tor Project supprimés")
			removed = append(removed, "tor", torList, torKeyring)
		} else {
			runSilent("apt-get", "remove", "-y", "tor")
			unOk("Tor supprimé (dépôt non géré sur cette distrib)")
			removed = append(removed, "tor")
		}
	} else {
		unWarn("Tor conservé.")
		skipped = append(skipped, "tor")
	}

	// ════════════════════════════════════════════════════════
	//  SUMMARY
	// ════════════════════════════════════════════════════════
	fmt.Println()
	unHr()
	fmt.Printf("\n  %s\n\n", unBold.Render("Résumé de la désinstallation"))

	if len(removed) > 0 {
		fmt.Println(unGreen.Render("  Supprimé :"))
		for _, r := range removed {
			fmt.Printf("    %s  %s\n", unGreen.Render("✓"), r)
		}
	}
	if len(skipped) > 0 {
		fmt.Println()
		fmt.Println(unYellow.Render("  Conservé :"))
		for _, s := range skipped {
			fmt.Printf("    %s  %s\n", unYellow.Render("⚠"), s)
		}
	}

	fmt.Println()
	unHr()
	fmt.Println()
	if len(skipped) == 0 {
		fmt.Println(unGreen.Render("  Désinstallation complète. Fialka Mailbox a été entièrement supprimé."))
	} else {
		fmt.Println(unYellow.Render("  Désinstallation partielle. Certains éléments ont été conservés."))
	}
	fmt.Println()
	return nil
}

// ── OS helpers ────────────────────────────────────────────────────────────────

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func userExists(name string) bool {
	out, err := exec.Command("id", name).Output()
	return err == nil && len(out) > 0
}

func isDebianBased() bool {
	out, err := exec.Command("lsb_release", "-si").Output()
	if err != nil {
		return false
	}
	id := strings.ToLower(strings.TrimSpace(string(out)))
	return id == "ubuntu" || id == "debian" || id == "raspbian"
}

func systemctlIsActive(service string) string {
	out, err := exec.Command("systemctl", "is-active", service).Output()
	if err != nil {
		return "inactive"
	}
	return strings.TrimSpace(string(out))
}

func statusLabel(s string) string {
	switch s {
	case "active":
		return unGreen.Render("active (running)")
	case "inactive", "failed":
		return unDim.Render(s)
	default:
		return s
	}
}

func runSilent(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Run()
}

func listDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, _ := e.Info()
		size := ""
		if info != nil && !e.IsDir() {
			size = fmt.Sprintf("  %s", unDim.Render(fmt.Sprintf("(%d B)", info.Size())))
		}
		marker := "  "
		if e.IsDir() {
			marker = "/"
		}
		fmt.Printf("    %s%s%s\n", unDim.Render("→ "), filepath.Join(dir, e.Name())+marker, size)
	}
}
