package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/MathiasDrizzy/lazymark/internal/config"
	"github.com/MathiasDrizzy/lazymark/internal/editors"
	"github.com/MathiasDrizzy/lazymark/internal/i18n"
)

// Uninstalling (added in this fork).
//
// Everything lazymark puts on a machine is one of four things, and this
// command knows all four: the program file, the settings folder (config.json
// and the user's themes), the editor plugins, and the trash inside the notes
// folder. Anything added later that stores a file anywhere else must be
// listed and removed here too.
//
// The notes themselves are never deleted: they are the user's files.

// Uninstaller removes lazymark from a machine. The fields say where things
// are, so tests can point it at a temporary folder.
type Uninstaller struct {
	// Exe is the program file.
	Exe string
	// ConfigDir is the settings folder (it holds config.json and themes).
	ConfigDir string
	// NotesDir is the notes folder. It is kept; only its trash can go.
	NotesDir string
	// Editors is where the editor plugins live.
	Editors editors.Env

	// Trash also empties the trash inside the notes folder.
	Trash bool
	// Yes skips the confirmation.
	Yes bool

	In  io.Reader
	Out io.Writer
}

func uninstallUsage() string {
	return i18n.E(
		`uso: lazymark uninstall [--trash] [--yes] [--dir <carpeta>]
  Quita el programa, sus ajustes y los plugins de editor. Las notas no se tocan.
  --trash  vacía también la papelera de la carpeta de notas
  --yes    no pide confirmación`,
		`usage: lazymark uninstall [--trash] [--yes] [--dir <folder>]
  Removes the program, its settings and the editor plugins. Notes are not touched.
  --trash  also empties the trash inside the notes folder
  --yes    does not ask for confirmation`)
}

// RunUninstall implements `lazymark uninstall`.
func RunUninstall(args []string, dir string, env editors.Env, stdin io.Reader, stdout, stderr io.Writer) int {
	u := Uninstaller{Editors: env, In: stdin, Out: stdout}
	for _, a := range withoutDirArg(args) {
		switch a {
		case "-h", "--help":
			fmt.Fprintln(stdout, uninstallUsage())
			return ExitOK
		case "--trash":
			u.Trash = true
		case "--yes":
			u.Yes = true
		default:
			fmt.Fprintln(stderr, uninstallUsage())
			return ExitUsage
		}
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return ExitFailure
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	u.Exe = exe
	u.ConfigDir = filepath.Dir(config.ThemesDir())
	if notes, err := notesFolder(dir); err == nil {
		u.NotesDir = notes
	}

	if err := u.Run(); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return ExitFailure
	}
	return ExitOK
}

// Run lists what is on the machine, asks, and removes it.
func (u Uninstaller) Run() error {
	say := func(es, en string, args ...any) { fmt.Fprintf(u.Out, i18n.E(es, en)+"\n", args...) }

	// A program installed by a package manager is the package manager's to
	// remove; deleting the file under it would leave its records wrong.
	managed := strings.Contains(u.Exe, "/Cellar/") || strings.Contains(u.Exe, "/Caskroom/")

	hasConfig := isDir(u.ConfigDir) && filepath.Base(u.ConfigDir) == config.AppName
	trashDir := filepath.Join(u.NotesDir, ".trash")
	hasTrash := u.NotesDir != "" && isDir(trashDir)

	say("lazymark tiene esto en esta máquina:", "lazymark has these on this machine:")
	fmt.Fprintln(u.Out)
	say("  Programa       %s", "  Program        %s", u.Exe)
	if hasConfig {
		say("  Ajustes        %s", "  Settings       %s", u.ConfigDir)
	} else {
		say("  Ajustes        ninguno", "  Settings       none")
	}
	say("  Plugins        los de micro, vim y nano, si están instalados", "  Editor plugins those for micro, vim and nano, if installed")
	if u.NotesDir != "" {
		say("  Notas          %s  (no se tocan)", "  Notes          %s  (not touched)", u.NotesDir)
	}
	if hasTrash {
		say("  Papelera       %s", "  Trash          %s", trashDir)
	}
	fmt.Fprintln(u.Out)
	say("No guarda nada más: ni caché, ni registros, ni procesos en segundo plano.",
		"It keeps nothing else: no cache, no logs and no background processes.")
	fmt.Fprintln(u.Out)

	switch {
	case hasTrash && u.Trash:
		say("Esto quita el programa, los ajustes y los plugins, y vacía la papelera.",
			"This removes the program, the settings and the editor plugins, and empties the trash.")
	case hasTrash:
		say("Esto quita el programa, los ajustes y los plugins. La papelera se queda; para vaciarla también: lazymark uninstall --trash",
			"This removes the program, the settings and the editor plugins. The trash stays; to empty it too: lazymark uninstall --trash")
	default:
		say("Esto quita el programa, los ajustes y los plugins.",
			"This removes the program, the settings and the editor plugins.")
	}

	if !u.Yes {
		fmt.Fprint(u.Out, i18n.E("¿Quitar lazymark? [s/N] ", "Remove lazymark? [y/N] "))
		line, _ := bufio.NewReader(u.In).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes", "s", "si", "sí":
		default:
			return i18n.NewError("no se quitó nada", "nothing was removed")
		}
	}
	fmt.Fprintln(u.Out)

	// The program goes last: if an earlier step fails it is still there to
	// run the command again.
	for _, name := range editors.Names {
		done, err := editors.Uninstall(name, u.Editors)
		for _, line := range done {
			fmt.Fprintln(u.Out, line)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}

	if hasTrash && u.Trash {
		if err := os.RemoveAll(trashDir); err != nil {
			return err
		}
		say("Vaciada   %s", "Emptied   %s", trashDir)
	}

	if hasConfig {
		if err := os.RemoveAll(u.ConfigDir); err != nil {
			return err
		}
		say("Quitado   %s", "Removed   %s", u.ConfigDir)
	}

	if managed {
		say("El programa lo instaló Homebrew; quítalo con: brew uninstall lazymark",
			"The program was installed by Homebrew; remove it with: brew uninstall lazymark")
	} else {
		if err := os.Remove(u.Exe); err != nil {
			if errors.Is(err, os.ErrPermission) {
				return i18n.Errorf("sin permiso para quitar %s; quítalo con: sudo rm %s",
					"no permission to remove %s; remove it with: sudo rm %s", u.Exe, u.Exe)
			}
			return err
		}
		say("Quitado   %s", "Removed   %s", u.Exe)
	}

	fmt.Fprintln(u.Out)
	say("No se tocó, porque no lo creó lazymark o es compartido:",
		"Not touched, because lazymark did not create it or it is shared:")
	if u.NotesDir != "" {
		say("  - Tus notas en %s, y los datos de git de esa carpeta si la sincronizas",
			"  - Your notes in %s, and that folder's git data if you sync it", u.NotesDir)
	}
	say("  - La carpeta del código fuente y cualquier otra copia del programa",
		"  - The source folder you built from, and any other copy of the program")
	say("  - Las cachés de compilación y de módulos de Go, que usan todos los proyectos de Go (para vaciarlas: go clean -cache -modcache)",
		"  - Go's build and module caches, used by every Go project (to clear them: go clean -cache -modcache)")
	return nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
