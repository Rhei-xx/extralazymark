package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MathiasDrizzy/lazymark/internal/i18n"
)

// These tests run the real git. A bare repository stands in for the server
// and two notes folders stand in for two machines.

// syncEnv isolates git and lazymark's settings from the real ones and
// returns a bare remote and a folder to keep notes folders in.
func syncEnv(t *testing.T) (remote, root string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	i18n.SetLanguage("en")
	t.Cleanup(func() { i18n.SetLanguage("es") })

	root = t.TempDir()
	global := filepath.Join(root, "gitconfig")
	config := "[init]\n\tdefaultBranch = main\n[user]\n\tname = Test User\n\temail = test@example.com\n"
	if err := os.WriteFile(global, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	remote = filepath.Join(root, "remote.git")
	if out, err := exec.Command("git", "init", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	return remote, root
}

// runSync runs `lazymark sync <args> --dir <dir>` and returns its exit code
// and everything it printed.
func runSync(dir string, args ...string) (int, string) {
	var out bytes.Buffer
	code := RunSync(append(args, "--dir", dir), dir, &out, &out)
	return code, out.String()
}

func writeNote(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readNote(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSyncSetupThenNotesTravelBetweenTwoMachines(t *testing.T) {
	remote, root := syncEnv(t)
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	writeNote(t, a, "plan.md", "- [ ] write report #kb/todo\n")
	writeNote(t, a, ".trash/old.md", "deleted earlier\n")

	code, out := runSync(a, "setup", "--remote", remote)
	if code != ExitOK || !strings.Contains(out, "Automatic sync is on") {
		t.Fatalf("setup on a: exit %d\n%s", code, out)
	}

	// The second machine starts empty and receives the note.
	if code, out := runSync(b, "setup", "--remote", remote); code != ExitOK {
		t.Fatalf("setup on b: exit %d\n%s", code, out)
	}
	if got := readNote(t, b, "plan.md"); got != "- [ ] write report #kb/todo\n" {
		t.Errorf("b has %q", got)
	}
	// The trash stays on the machine it belongs to.
	if _, err := os.Stat(filepath.Join(b, ".trash")); err == nil {
		t.Error("the trash was synced to the other machine")
	}

	// Moving the task on b reaches a.
	writeNote(t, b, "plan.md", "- [ ] write report #kb/doing\n")
	if code, out := runSync(b, "now"); code != ExitOK || !strings.Contains(out, "sent changes") {
		t.Fatalf("sync now on b: exit %d\n%s", code, out)
	}
	if code, out := runSync(a, "now"); code != ExitOK || !strings.Contains(out, "received changes") {
		t.Fatalf("sync now on a: exit %d\n%s", code, out)
	}
	if got := readNote(t, a, "plan.md"); got != "- [ ] write report #kb/doing\n" {
		t.Errorf("a has %q", got)
	}
}

func TestSyncStopsOnAConflictAndSaysWhichNote(t *testing.T) {
	remote, root := syncEnv(t)
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	writeNote(t, a, "plan.md", "- [ ] write report #kb/todo\n")
	runSync(a, "setup", "--remote", remote)
	runSync(b, "setup", "--remote", remote)

	// The same task line is changed on both machines.
	writeNote(t, a, "plan.md", "- [ ] write report #kb/doing\n")
	writeNote(t, b, "plan.md", "- [x] write report #kb/done\n")
	runSync(a, "now")

	code, out := runSync(b, "now")
	if code != ExitConflict {
		t.Errorf("exit %d, want %d (conflict)\n%s", code, ExitConflict, out)
	}
	for _, want := range []string{"changed on both machines", "plan.md", "Nothing was overwritten"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	// Both versions are in the file; neither was thrown away.
	note := readNote(t, b, "plan.md")
	if !strings.Contains(note, "#kb/doing") || !strings.Contains(note, "#kb/done") || !strings.Contains(note, "<<<<<<<") {
		t.Errorf("the conflicted note does not hold both versions:\n%s", note)
	}
	if _, out := runSync(b, "status"); !strings.Contains(out, "plan.md") {
		t.Errorf("status does not list the conflict:\n%s", out)
	}

	// Fixed by hand, the next sync finishes and the other machine gets it.
	writeNote(t, b, "plan.md", "- [x] write report #kb/done\n")
	if code, out := runSync(b, "now"); code != ExitOK {
		t.Fatalf("sync after fixing: exit %d\n%s", code, out)
	}
	runSync(a, "now")
	if got := readNote(t, a, "plan.md"); got != "- [x] write report #kb/done\n" {
		t.Errorf("a has %q", got)
	}
}

func TestSyncSetupThatCannotReachTheRemoteLeavesAutomaticSyncOff(t *testing.T) {
	_, root := syncEnv(t)
	a := filepath.Join(root, "a")
	writeNote(t, a, "plan.md", "note\n")

	code, out := runSync(a, "setup", "--remote", filepath.Join(root, "no-such-remote.git"))
	if code == ExitOK || !strings.Contains(out, "NOT turned on") {
		t.Errorf("exit %d\n%s", code, out)
	}
	if _, out := runSync(a, "status"); !strings.Contains(out, "off") {
		t.Errorf("automatic sync is not off:\n%s", out)
	}
	// With automatic sync off, opening and closing lazymark does no syncing.
	var printed bytes.Buffer
	SyncOnOpen(a, strings.NewReader(""), &printed)
	SyncOnClose(a, &printed)
	if printed.Len() > 0 {
		t.Errorf("sync ran with automatic sync off:\n%s", printed.String())
	}
}

func TestSyncOnOpenAndOnClose(t *testing.T) {
	remote, root := syncEnv(t)
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	writeNote(t, a, "plan.md", "one\n")
	runSync(a, "setup", "--remote", remote)
	runSync(b, "setup", "--remote", remote)

	// Closing with nothing changed does not sync at all.
	var out bytes.Buffer
	SyncOnClose(a, &out)
	if out.Len() > 0 {
		t.Errorf("closing with no changes printed:\n%s", out.String())
	}

	// A change made in a goes out when a closes, and comes in when b opens.
	writeNote(t, a, "plan.md", "two\n")
	SyncOnClose(a, &out)
	SyncOnOpen(b, strings.NewReader(""), &out)
	if got := readNote(t, b, "plan.md"); got != "two\n" {
		t.Errorf("b has %q, want the change made on a", got)
	}
	if strings.Contains(out.String(), "Press Enter") {
		t.Errorf("a clean sync should not wait for Enter:\n%s", out.String())
	}

	// A conflict on open is shown and waits, so the interface does not hide it.
	writeNote(t, a, "plan.md", "three, from a\n")
	writeNote(t, b, "plan.md", "three, from b\n")
	SyncOnClose(a, &out)
	out.Reset()
	SyncOnOpen(b, strings.NewReader("\n"), &out)
	for _, want := range []string{"changed on both machines", "plan.md", "Press Enter to open lazymark"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output is missing %q:\n%s", want, out.String())
		}
	}
}

func TestSyncUsageErrors(t *testing.T) {
	_, root := syncEnv(t)
	dir := filepath.Join(root, "a")
	for name, args := range map[string][]string{
		"no command":      {},
		"unknown command": {"sideways"},
		"stray argument":  {"now", "extra"},
		"setup bad flag":  {"setup", "--nope"},
	} {
		if code, _ := runSync(dir, args...); code != ExitUsage {
			t.Errorf("%s: exit %d, want %d", name, code, ExitUsage)
		}
	}
	// Before setup, the error says how to set it up.
	code, out := runSync(dir, "now")
	if code != ExitFailure || !strings.Contains(out, "lazymark sync setup") {
		t.Errorf("sync now before setup: exit %d\n%s", code, out)
	}
	if code, out := runSync(dir, "status"); code != ExitOK || !strings.Contains(out, "not set up") {
		t.Errorf("status before setup: exit %d\n%s", code, out)
	}
}
