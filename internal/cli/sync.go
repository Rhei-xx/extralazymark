package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MathiasDrizzy/lazymark/internal/config"
	"github.com/MathiasDrizzy/lazymark/internal/gitsync"
	"github.com/MathiasDrizzy/lazymark/internal/i18n"
)

// Sync between machines (added in this fork; see internal/gitsync).
//
// The texts here are Spanish/English pairs through i18n.E, so the other six
// interface languages show them in English.

// syncTimeout stops a sync that hangs.
const syncTimeout = 2 * time.Minute

func syncUsage() string {
	return i18n.E(
		`uso: lazymark sync setup --remote <url> [--ssh-key <archivo>] [--dir <carpeta>]
     lazymark sync now    [--dir <carpeta>]   sincroniza una vez
     lazymark sync status [--dir <carpeta>]   muestra cómo está configurada
     lazymark sync on|off [--dir <carpeta>]   activa o desactiva la sincronización automática`,
		`usage: lazymark sync setup --remote <url> [--ssh-key <file>] [--dir <folder>]
       lazymark sync now    [--dir <folder>]   sync once
       lazymark sync status [--dir <folder>]   show how sync is set up
       lazymark sync on|off [--dir <folder>]   turn automatic sync on or off`)
}

// RunSync implements `lazymark sync setup|now|status|on|off`. dir is the
// --dir value, or "" for the configured notes folder.
func RunSync(args []string, dir string, stdout, stderr io.Writer) int {
	// --dir is read by the caller; drop it here so it can sit anywhere.
	args = withoutDirArg(args)
	if len(args) == 0 {
		fmt.Fprintln(stderr, syncUsage())
		return ExitUsage
	}
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Fprintln(stdout, syncUsage())
			return ExitOK
		}
	}

	notes, err := notesFolder(dir)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return ExitFailure
	}
	ctx := context.Background()
	repo := gitsync.Repo{Dir: notes, Interactive: true}

	fail := func(err error) int {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return ExitFailure
	}

	switch args[0] {
	case "setup":
		return syncSetup(ctx, repo, args[1:], stdout, stderr)

	case "now":
		if len(args) > 1 {
			break
		}
		res, err := repo.Sync(ctx)
		if err != nil {
			return fail(err)
		}
		reportSync(stdout, res)
		if len(res.Conflicts) > 0 {
			return ExitConflict
		}
		return ExitOK

	case "status":
		if len(args) > 1 {
			break
		}
		if err := syncStatus(ctx, repo, stdout); err != nil {
			return fail(err)
		}
		return ExitOK

	case "on", "off":
		if len(args) > 1 {
			break
		}
		on := args[0] == "on"
		if err := repo.SetAutoSync(ctx, on); err != nil {
			return fail(err)
		}
		if on {
			fmt.Fprintln(stdout, i18n.E("La sincronización automática está activada.", "Automatic sync is on."))
		} else {
			fmt.Fprintln(stdout, i18n.E("La sincronización automática está desactivada.", "Automatic sync is off."))
		}
		return ExitOK
	}

	fmt.Fprintln(stderr, syncUsage())
	return ExitUsage
}

// syncSetup prepares the folder, runs a first sync, and only then turns
// automatic sync on, so a setup that cannot reach the remote changes nothing
// about how lazymark behaves.
func syncSetup(ctx context.Context, repo gitsync.Repo, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("lazymark sync setup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	remote := flags.String("remote", "", "")
	sshKey := flags.String("ssh-key", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		fmt.Fprintln(stderr, syncUsage())
		return ExitUsage
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return ExitFailure
	}

	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		return fail(err)
	}
	if err := repo.Setup(ctx, gitsync.SetupOptions{Remote: *remote, SSHKey: *sshKey}); err != nil {
		return fail(err)
	}
	fmt.Fprintln(stdout, i18n.E("Carpeta:", "Folder:"), repo.Dir)
	fmt.Fprintln(stdout, i18n.E("Remoto: ", "Remote:"), repo.RemoteURL(ctx))
	if ssh := repo.SSHCommand(ctx); ssh != "" {
		fmt.Fprintln(stdout, "SSH:   ", ssh)
	}

	// Keep the trash and editor leftovers out of the sync, from the very first
	// commit. Both machines write the same file, so it merges without fuss.
	written, err := repo.EnsureGitignore()
	if err != nil {
		return fail(err)
	}
	if !written {
		// The folder already had a .gitignore of the user's own; it is left alone.
		if data, _ := os.ReadFile(filepath.Join(repo.Dir, ".gitignore")); !strings.Contains(string(data), ".trash") {
			fmt.Fprintln(stdout, i18n.E(
				"Aviso: tu .gitignore no excluye la papelera. Añade una línea \".trash/\" para que no se sincronice.",
				"Note: your .gitignore does not exclude the trash. Add a \".trash/\" line to keep it out of the sync."))
		}
	}

	fmt.Fprintln(stdout, i18n.E("Primera sincronización…", "Running a first sync…"))
	res, err := repo.Sync(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		fmt.Fprintln(stderr, i18n.E(
			"La sincronización automática NO se activó. Corrige el problema y repite el setup.",
			"Automatic sync was NOT turned on. Fix the problem and run setup again."))
		return ExitFailure
	}
	reportSync(stdout, res)
	if len(res.Conflicts) > 0 {
		fmt.Fprintln(stderr, i18n.E(
			"La sincronización automática NO se activó. Resuelve los conflictos y ejecuta: lazymark sync now && lazymark sync on",
			"Automatic sync was NOT turned on. Fix the conflicts, then run: lazymark sync now && lazymark sync on"))
		return ExitConflict
	}

	if err := repo.SetAutoSync(ctx, true); err != nil {
		return fail(err)
	}
	fmt.Fprintln(stdout, i18n.E(
		"La sincronización automática está activada: lazymark sincroniza al abrir y al cerrar.",
		"Automatic sync is on: lazymark syncs when it opens and when it closes."))
	return ExitOK
}

func syncStatus(ctx context.Context, repo gitsync.Repo, out io.Writer) error {
	row := func(es, en, value string) { fmt.Fprintf(out, "%-18s %s\n", i18n.E(es, en), value) }

	row("Carpeta:", "Folder:", repo.Dir)
	if !repo.IsRepo(ctx) {
		row("Sincronización:", "Sync:", i18n.E(
			"sin configurar (ejecuta: lazymark sync setup --remote <url>)",
			"not set up (run: lazymark sync setup --remote <url>)"))
		return nil
	}

	orElse := func(v, es, en string) string {
		if v == "" {
			return i18n.E(es, en)
		}
		return v
	}
	pick := func(cond bool, yesES, yesEN, noES, noEN string) string {
		if cond {
			return i18n.E(yesES, yesEN)
		}
		return i18n.E(noES, noEN)
	}

	row("Remoto:", "Remote:", orElse(repo.RemoteURL(ctx), "ninguno", "none"))
	row("Comando SSH:", "SSH command:", orElse(repo.SSHCommand(ctx), "el del sistema", "the system default"))
	row("Automática:", "Automatic sync:", pick(repo.AutoSync(ctx), "activada", "on", "desactivada", "off"))
	row("Cambios locales:", "Local changes:", pick(repo.HasLocalChanges(ctx) || repo.Unpushed(ctx),
		"sin sincronizar", "not yet synced", "ninguno", "none"))

	conflicts, err := repo.Unresolved(ctx)
	if err != nil {
		return err
	}
	if len(conflicts) == 0 {
		row("Conflictos:", "Conflicts:", i18n.E("ninguno", "none"))
		return nil
	}
	row("Conflictos:", "Conflicts:", "")
	for _, f := range conflicts {
		fmt.Fprintln(out, "  "+f)
	}
	return nil
}

// reportSync prints what a sync did.
func reportSync(out io.Writer, res gitsync.Result) {
	if len(res.Conflicts) > 0 {
		fmt.Fprintln(out, i18n.E(
			"La sincronización se detuvo. Estas notas cambiaron en las dos máquinas:",
			"Sync stopped. These notes were changed on both machines:"))
		for _, f := range res.Conflicts {
			fmt.Fprintln(out, "  "+f)
		}
		fmt.Fprintln(out, i18n.E(
			"Cada archivo tiene las dos versiones entre líneas <<<<<<<, ======= y >>>>>>>.\n"+
				"Abre cada uno, deja las líneas que quieras, borra las tres líneas de marca y ejecuta: lazymark sync now\n"+
				"No se sobrescribió nada.",
			"Each file holds both versions between <<<<<<<, ======= and >>>>>>> lines.\n"+
				"Open each one, keep the lines you want, delete the three marker lines, then run: lazymark sync now\n"+
				"Nothing was overwritten."))
		return
	}
	switch {
	case res.Pulled && res.Pushed:
		fmt.Fprintln(out, i18n.E("Sincronizado: se recibieron y enviaron cambios.", "Synced: received and sent changes."))
	case res.Pulled:
		fmt.Fprintln(out, i18n.E("Sincronizado: se recibieron cambios.", "Synced: received changes."))
	case res.Pushed:
		fmt.Fprintln(out, i18n.E("Sincronizado: se enviaron cambios.", "Synced: sent changes."))
	default:
		fmt.Fprintln(out, i18n.E("Sincronizado: todo al día.", "Synced: already up to date."))
	}
}

// SyncOnOpen syncs the notes folder before the interface starts, if automatic
// sync is on, so the notes on screen include what the other machine changed.
// It runs before the interface takes over the terminal, so git can ask for a
// key passphrase. A failure or a conflict is shown and waits for Enter, since
// the interface would otherwise cover the message at once.
func SyncOnOpen(notesDir string, in io.Reader, out io.Writer) {
	repo := gitsync.Repo{Dir: notesDir, Interactive: true}
	if !repo.AutoSync(context.Background()) {
		return
	}
	fmt.Fprintln(out, i18n.E("Sincronizando notas…", "Syncing notes…"))
	if syncAndReport(repo, out) {
		return
	}
	fmt.Fprint(out, i18n.E("Pulsa Enter para abrir lazymark. ", "Press Enter to open lazymark. "))
	bufio.NewReader(in).ReadString('\n')
}

// SyncOnClose syncs the notes folder after the interface has closed, if
// automatic sync is on and something changed. With nothing to send it does
// not contact the remote at all.
func SyncOnClose(notesDir string, out io.Writer) {
	ctx := context.Background()
	repo := gitsync.Repo{Dir: notesDir, Interactive: true}
	if !repo.AutoSync(ctx) || !(repo.HasLocalChanges(ctx) || repo.Unpushed(ctx)) {
		return
	}
	fmt.Fprintln(out, i18n.E("Sincronizando notas…", "Syncing notes…"))
	syncAndReport(repo, out)
}

// syncAndReport runs one sync and prints the outcome if it needs the user's
// attention. It reports whether the sync went through cleanly.
func syncAndReport(repo gitsync.Repo, out io.Writer) (clean bool) {
	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	defer cancel()

	res, err := repo.Sync(ctx)
	switch {
	case err != nil:
		fmt.Fprintf(out, i18n.E("No se pudo sincronizar: %v\n", "Could not sync: %v\n"), err)
		fmt.Fprintln(out, i18n.E(
			"Tus notas están guardadas en esta máquina y se sincronizarán la próxima vez.",
			"Your notes are saved on this machine and will sync next time."))
		return false
	case len(res.Conflicts) > 0:
		reportSync(out, res)
		return false
	}
	return true
}

// notesFolder is the notes folder a command acts on: dir if given, or the
// configured one. It reads the configuration without writing anything.
func notesFolder(dir string) (string, error) {
	cfg, err := config.LoadReadOnly(dir)
	if err != nil {
		return "", err
	}
	// Speak the language chosen in Settings, as the other commands do; with
	// none chosen, the system language that i18n detected at start-up stays.
	if lang := strings.ToLower(strings.TrimSpace(cfg.Language)); lang != "" && lang != "auto" {
		i18n.SetLanguage(lang)
	}
	return cfg.NotesDir, nil
}

// withoutDirArg removes --dir and its value from args.
func withoutDirArg(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--dir":
			i++
		case strings.HasPrefix(args[i], "--dir="):
		default:
			out = append(out, args[i])
		}
	}
	return out
}
