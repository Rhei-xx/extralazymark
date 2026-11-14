package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MathiasDrizzy/lazymark/internal/editors"
	"github.com/MathiasDrizzy/lazymark/internal/i18n"
)

// newUninstall returns an Uninstaller over a pretend installation in a
// temporary folder: a program file, a settings folder with a theme, a notes
// folder with a note and a trash, and an installed editor plugin.
func newUninstall(t *testing.T) Uninstaller {
	t.Helper()
	i18n.SetLanguage("en")
	t.Cleanup(func() { i18n.SetLanguage("es") })

	root := t.TempDir()
	u := Uninstaller{
		Exe:       filepath.Join(root, "bin", "lazymark"),
		ConfigDir: filepath.Join(root, "settings", "lazymark"),
		NotesDir:  filepath.Join(root, "notes"),
		Editors:   editors.Env{Home: filepath.Join(root, "home"), Getenv: func(string) string { return "" }},
	}
	for path, content := range map[string]string{
		u.Exe: "program",
		filepath.Join(u.ConfigDir, "config.json"):         "{}",
		filepath.Join(u.ConfigDir, "themes", "mine.json"): "{}",
		filepath.Join(u.NotesDir, "plan.md"):              "my notes\n",
		filepath.Join(u.NotesDir, ".git", "config"):       "sync data\n",
		filepath.Join(u.NotesDir, ".trash", "old.md"):     "deleted\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := editors.Install("vim", u.Editors); err != nil {
		t.Fatal(err)
	}
	return u
}

func runUninstall(u Uninstaller, answer string) (string, error) {
	var out bytes.Buffer
	u.In, u.Out = strings.NewReader(answer), &out
	err := u.Run()
	return out.String(), err
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// vimPluginFiles lists what the vim plugin left under the pretend home.
func leftUnderHome(t *testing.T, u Uninstaller) []string {
	t.Helper()
	var files []string
	filepath.WalkDir(u.Editors.Home, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	return files
}

func TestUninstallRemovesEverythingButTheNotes(t *testing.T) {
	u := newUninstall(t)
	if len(leftUnderHome(t, u)) == 0 {
		t.Fatal("the editor plugin was not installed, so the test proves nothing")
	}

	out, err := runUninstall(u, "y\n")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	for what, path := range map[string]string{"the program": u.Exe, "the settings folder": u.ConfigDir} {
		if pathExists(path) {
			t.Errorf("%s is still there: %s", what, path)
		}
	}
	if left := leftUnderHome(t, u); len(left) > 0 {
		t.Errorf("editor plugin files are still there: %v", left)
	}

	// The notes, their sync data and (without --trash) the trash are kept.
	for _, rel := range []string{"plan.md", ".git/config", ".trash/old.md"} {
		if !pathExists(filepath.Join(u.NotesDir, filepath.FromSlash(rel))) {
			t.Errorf("%s was deleted from the notes folder", rel)
		}
	}
	for _, want := range []string{u.Exe, u.ConfigDir, u.NotesDir, "not touched", "lazymark uninstall --trash"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

func TestUninstallWithTrashEmptiesTheTrashAndStillKeepsTheNotes(t *testing.T) {
	u := newUninstall(t)
	u.Trash = true

	if out, err := runUninstall(u, "yes\n"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if pathExists(filepath.Join(u.NotesDir, ".trash")) {
		t.Error("the trash is still there")
	}
	if !pathExists(filepath.Join(u.NotesDir, "plan.md")) || !pathExists(filepath.Join(u.NotesDir, ".git", "config")) {
		t.Error("the notes or their sync data were deleted")
	}
}

func TestUninstallDoesNothingUnlessConfirmed(t *testing.T) {
	for _, answer := range []string{"n\n", "\n", "", "maybe\n"} {
		u := newUninstall(t)
		u.Trash = true
		out, err := runUninstall(u, answer)
		if err == nil {
			t.Errorf("answer %q: got no error\n%s", answer, out)
		}
		for _, path := range []string{u.Exe, filepath.Join(u.ConfigDir, "config.json"), filepath.Join(u.NotesDir, ".trash", "old.md")} {
			if !pathExists(path) {
				t.Errorf("answer %q: %s was removed", answer, path)
			}
		}
		if len(leftUnderHome(t, u)) == 0 {
			t.Errorf("answer %q: the editor plugin was removed", answer)
		}
	}
}

func TestUninstallYesSkipsTheQuestion(t *testing.T) {
	u := newUninstall(t)
	u.Yes = true
	if out, err := runUninstall(u, ""); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if pathExists(u.Exe) {
		t.Error("the program was not removed")
	}
}

func TestUninstallOnlyRemovesASettingsFolderNamedLazymark(t *testing.T) {
	// If the settings folder were ever resolved wrongly, it must not be deleted.
	u := newUninstall(t)
	wrong := filepath.Join(filepath.Dir(u.ConfigDir), "something-else")
	if err := os.Rename(u.ConfigDir, wrong); err != nil {
		t.Fatal(err)
	}
	u.ConfigDir, u.Yes = wrong, true

	out, err := runUninstall(u, "")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !pathExists(filepath.Join(wrong, "config.json")) {
		t.Error("a folder not named lazymark was deleted")
	}
}

func TestUninstallLeavesAHomebrewInstallToHomebrew(t *testing.T) {
	u := newUninstall(t)
	brewed := filepath.Join(filepath.Dir(filepath.Dir(u.Exe)), "Cellar", "lazymark", "1.0", "bin", "lazymark")
	if err := os.MkdirAll(filepath.Dir(brewed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(u.Exe, brewed); err != nil {
		t.Fatal(err)
	}
	u.Exe, u.Yes = brewed, true

	out, err := runUninstall(u, "")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !pathExists(brewed) {
		t.Error("the Homebrew-installed program was deleted directly")
	}
	if !strings.Contains(out, "brew uninstall lazymark") {
		t.Errorf("output does not say to use brew:\n%s", out)
	}
	if pathExists(u.ConfigDir) {
		t.Error("the settings folder should still have been removed")
	}
}
