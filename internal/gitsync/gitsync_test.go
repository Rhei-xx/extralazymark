package gitsync

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests run the real git against repositories in temporary folders.
// A bare repository stands in for the server, and two notes folders, a and b,
// stand in for two machines.

var ctx = context.Background()

// isolateGit points git at a throwaway global config, so the tests neither
// read nor change the real one.
func isolateGit(t *testing.T, withIdentity bool) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	home := t.TempDir()
	config := "[init]\n\tdefaultBranch = main\n"
	if withIdentity {
		config += "[user]\n\tname = Test User\n\temail = test@example.com\n"
	}
	global := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(global, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// newRemote makes an empty bare repository and returns its path.
func newRemote(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	return dir
}

// newMachine makes a notes folder that is set up to sync with the remote.
func newMachine(t *testing.T, remote string) Repo {
	t.Helper()
	r := Repo{Dir: filepath.Join(t.TempDir(), "data")}
	if err := r.Setup(ctx, SetupOptions{Remote: remote}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	return r
}

func write(t *testing.T, r Repo, rel, content string) {
	t.Helper()
	path := filepath.Join(r.Dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, r Repo, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.Dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func exists(r Repo, rel string) bool {
	_, err := os.Stat(filepath.Join(r.Dir, filepath.FromSlash(rel)))
	return err == nil
}

// sync runs a sync that is expected to finish without conflicts.
func sync(t *testing.T, r Repo) Result {
	t.Helper()
	res, err := r.Sync(ctx)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(res.Conflicts) > 0 {
		t.Fatalf("sync: unexpected conflicts %v", res.Conflicts)
	}
	return res
}

func TestSyncCarriesChangesBetweenTwoMachines(t *testing.T) {
	isolateGit(t, true)
	remote := newRemote(t)
	a, b := newMachine(t, remote), newMachine(t, remote)

	// The first machine creates a card and syncs.
	write(t, a, "cards/one.md", "one\n")
	if res := sync(t, a); !res.Pushed {
		t.Error("first sync from a: nothing was pushed")
	}

	// The second machine starts empty and receives it.
	if res := sync(t, b); !res.Pulled {
		t.Error("first sync on b: nothing was pulled")
	}
	if got := read(t, b, "cards/one.md"); got != "one\n" {
		t.Errorf("b has %q, want %q", got, "one\n")
	}

	// A change made on the second machine reaches the first.
	write(t, b, "cards/one.md", "one, edited on b\n")
	write(t, b, "cards/two.md", "two\n")
	if res := sync(t, b); !res.Pushed {
		t.Error("sync from b: nothing was pushed")
	}
	if res := sync(t, a); !res.Pulled {
		t.Error("sync on a: nothing was pulled")
	}
	if got := read(t, a, "cards/one.md"); got != "one, edited on b\n" {
		t.Errorf("a has %q", got)
	}
	if !exists(a, "cards/two.md") {
		t.Error("a did not receive the new card")
	}

	// A deletion travels too.
	if err := os.Remove(filepath.Join(a.Dir, "cards", "two.md")); err != nil {
		t.Fatal(err)
	}
	sync(t, a)
	sync(t, b)
	if exists(b, "cards/two.md") {
		t.Error("b still has the card deleted on a")
	}

	// With nothing changed, a sync does nothing.
	if res := sync(t, a); res.Pulled || res.Pushed {
		t.Errorf("idle sync: got %+v, want no pull and no push", res)
	}
}

func TestSyncMergesEditsToDifferentCards(t *testing.T) {
	isolateGit(t, true)
	remote := newRemote(t)
	a, b := newMachine(t, remote), newMachine(t, remote)

	write(t, a, "cards/one.md", "one\n")
	write(t, a, "cards/two.md", "two\n")
	sync(t, a)
	sync(t, b)

	// Both machines edit, each a different card, before either syncs.
	write(t, a, "cards/one.md", "one, edited on a\n")
	write(t, b, "cards/two.md", "two, edited on b\n")
	sync(t, a)
	sync(t, b)
	sync(t, a)

	for _, r := range []Repo{a, b} {
		if got := read(t, r, "cards/one.md"); got != "one, edited on a\n" {
			t.Errorf("%s: one.md is %q", r.Dir, got)
		}
		if got := read(t, r, "cards/two.md"); got != "two, edited on b\n" {
			t.Errorf("%s: two.md is %q", r.Dir, got)
		}
	}
}

func TestSyncStopsOnAConflictAndFinishesOnceItIsFixed(t *testing.T) {
	isolateGit(t, true)
	remote := newRemote(t)
	a, b := newMachine(t, remote), newMachine(t, remote)

	write(t, a, "cards/one.md", "status: todo\n")
	write(t, a, "cards/other.md", "other\n")
	sync(t, a)
	sync(t, b)

	// Both machines change the same line.
	write(t, a, "cards/one.md", "status: doing\n")
	write(t, b, "cards/one.md", "status: done\n")
	write(t, b, "cards/other.md", "other, edited on b\n")
	sync(t, a)

	res, err := b.Sync(ctx)
	if err != nil {
		t.Fatalf("conflicting sync: %v", err)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0] != "cards/one.md" {
		t.Fatalf("got conflicts %v, want [cards/one.md]", res.Conflicts)
	}
	if res.Pushed {
		t.Error("a conflicted sync must not push")
	}

	// Both versions are in the file for the user to choose from.
	conflicted := read(t, b, "cards/one.md")
	for _, want := range []string{"<<<<<<< ", "status: done", "status: doing", ">>>>>>> "} {
		if !strings.Contains(conflicted, want) {
			t.Errorf("conflicted file is missing %q:\n%s", want, conflicted)
		}
	}

	// Syncing again without fixing it changes nothing and still reports it.
	res, err = b.Sync(ctx)
	if err != nil || len(res.Conflicts) != 1 {
		t.Fatalf("second sync: got %+v, %v; want the same conflict", res, err)
	}
	if got := read(t, b, "cards/one.md"); got != conflicted {
		t.Error("an unresolved conflict was altered by a second sync")
	}
	// The other machine is unaffected meanwhile.
	if got := read(t, a, "cards/one.md"); got != "status: doing\n" {
		t.Errorf("a has %q", got)
	}

	// The user fixes the file by hand; the next sync finishes the job.
	write(t, b, "cards/one.md", "status: done\n")
	if res := sync(t, b); !res.Pushed {
		t.Error("sync after fixing: nothing was pushed")
	}
	sync(t, a)
	for _, r := range []Repo{a, b} {
		if got := read(t, r, "cards/one.md"); got != "status: done\n" {
			t.Errorf("%s: one.md is %q", r.Dir, got)
		}
		if got := read(t, r, "cards/other.md"); got != "other, edited on b\n" {
			t.Errorf("%s: other.md is %q", r.Dir, got)
		}
	}
}

func TestSyncKeepsACardEditedOnOneMachineAndDeletedOnTheOther(t *testing.T) {
	isolateGit(t, true)
	remote := newRemote(t)
	a, b := newMachine(t, remote), newMachine(t, remote)

	write(t, a, "cards/one.md", "one\n")
	sync(t, a)
	sync(t, b)

	if err := os.Remove(filepath.Join(a.Dir, "cards", "one.md")); err != nil {
		t.Fatal(err)
	}
	write(t, b, "cards/one.md", "one, with an important edit\n")
	sync(t, a)

	// The clash is reported, not settled silently.
	res, err := b.Sync(ctx)
	if err != nil || len(res.Conflicts) != 1 {
		t.Fatalf("got %+v, %v; want one conflict", res, err)
	}

	// Syncing again keeps the edited card and sends it back.
	sync(t, b)
	sync(t, a)
	for _, r := range []Repo{a, b} {
		if !exists(r, "cards/one.md") || read(t, r, "cards/one.md") != "one, with an important edit\n" {
			t.Errorf("%s: the edited card was lost", r.Dir)
		}
	}
}

func TestSyncJoinsAMachineThatAlreadyHasCards(t *testing.T) {
	isolateGit(t, true)
	remote := newRemote(t)
	a := newMachine(t, remote)
	write(t, a, "cards/from-a.md", "a\n")
	sync(t, a)

	// The second machine was used on its own before sync was set up.
	b := Repo{Dir: filepath.Join(t.TempDir(), "data")}
	write(t, b, "cards/from-b.md", "b\n")
	if err := b.Setup(ctx, SetupOptions{Remote: remote}); err != nil {
		t.Fatal(err)
	}
	sync(t, b)
	sync(t, a)

	for _, r := range []Repo{a, b} {
		if !exists(r, "cards/from-a.md") || !exists(r, "cards/from-b.md") {
			t.Errorf("%s does not have both cards", r.Dir)
		}
	}
}

func TestSyncWhenTheRemoteCannotBeReached(t *testing.T) {
	isolateGit(t, true)
	a := newMachine(t, filepath.Join(t.TempDir(), "no-such-remote.git"))
	write(t, a, "cards/one.md", "one\n")

	if _, err := a.Sync(ctx); err == nil {
		t.Fatal("got no error")
	}
	// The local change was still committed, ready for the next sync.
	if a.HasLocalChanges(ctx) {
		t.Error("the local change was not committed")
	}
	if !exists(a, "cards/one.md") {
		t.Error("the card is gone")
	}
}

func TestAFolderInsideAnotherRepositoryIsNotSetUp(t *testing.T) {
	isolateGit(t, true)
	parent := t.TempDir()
	if out, err := exec.Command("git", "init", parent).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	r := Repo{Dir: filepath.Join(parent, "data")}
	write(t, r, "cards/one.md", "one\n")

	if r.IsRepo(ctx) {
		t.Error("IsRepo is true for a folder inside another repository")
	}
	if r.AutoSync(ctx) {
		t.Error("AutoSync is true for a folder inside another repository")
	}
	if _, err := r.Sync(ctx); !errors.Is(err, ErrNotSetUp) {
		t.Errorf("Sync: got %v, want ErrNotSetUp", err)
	}
}

func TestSetupRecordsTheRemoteAndSSHKey(t *testing.T) {
	isolateGit(t, true)
	key := filepath.Join(t.TempDir(), "my key's file")
	if err := os.WriteFile(key, []byte("not a real key"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := Repo{Dir: filepath.Join(t.TempDir(), "data")}
	if err := r.Setup(ctx, SetupOptions{Remote: "git@example.com:me/board.git", SSHKey: key}); err != nil {
		t.Fatal(err)
	}

	if got := r.RemoteURL(ctx); got != "git@example.com:me/board.git" {
		t.Errorf("remote: got %q", got)
	}
	// The key path is quoted so spaces and quotes survive the shell.
	want := `ssh -i '` + strings.ReplaceAll(key, "'", `'\''`) + `' -o IdentitiesOnly=yes`
	if got := r.SSHCommand(ctx); got != want {
		t.Errorf("ssh command:\ngot  %s\nwant %s", got, want)
	}

	// Running setup again changes the remote.
	if err := r.Setup(ctx, SetupOptions{Remote: "git@example.com:me/other.git"}); err != nil {
		t.Fatal(err)
	}
	if got := r.RemoteURL(ctx); got != "git@example.com:me/other.git" {
		t.Errorf("remote after second setup: got %q", got)
	}
}

func TestEnsureGitignoreWritesOnceAndKeepsAnExistingFile(t *testing.T) {
	isolateGit(t, true)
	r := newMachine(t, newRemote(t))

	if written, err := r.EnsureGitignore(); err != nil || !written {
		t.Fatalf("first call: written %v, err %v", written, err)
	}
	if !strings.Contains(read(t, r, ".gitignore"), ".DS_Store") {
		t.Error(".gitignore does not cover .DS_Store")
	}

	write(t, r, ".gitignore", "mine\n")
	if written, err := r.EnsureGitignore(); err != nil || written {
		t.Errorf("second call: written %v, err %v", written, err)
	}
	if got := read(t, r, ".gitignore"); got != "mine\n" {
		t.Errorf(".gitignore was overwritten: %q", got)
	}
}

func TestSetupErrors(t *testing.T) {
	t.Run("missing SSH key", func(t *testing.T) {
		isolateGit(t, true)
		r := Repo{Dir: filepath.Join(t.TempDir(), "data")}
		err := r.Setup(ctx, SetupOptions{Remote: "git@example.com:me/board.git", SSHKey: "/no/such/key"})
		if err == nil || !strings.Contains(err.Error(), "SSH key") {
			t.Errorf("got %v, want an SSH key error", err)
		}
	})
	t.Run("no remote", func(t *testing.T) {
		isolateGit(t, true)
		r := Repo{Dir: filepath.Join(t.TempDir(), "data")}
		if err := r.Setup(ctx, SetupOptions{}); !errors.Is(err, ErrNoRemote) {
			t.Errorf("got %v, want ErrNoRemote", err)
		}
	})
	t.Run("no git identity", func(t *testing.T) {
		isolateGit(t, false)
		r := Repo{Dir: filepath.Join(t.TempDir(), "data")}
		err := r.Setup(ctx, SetupOptions{Remote: "git@example.com:me/board.git"})
		if !errors.Is(err, ErrNoIdentity) {
			t.Errorf("got %v, want ErrNoIdentity", err)
		}
	})
}

func TestAutoSyncIsOffUntilTurnedOn(t *testing.T) {
	isolateGit(t, true)
	r := newMachine(t, newRemote(t))

	if r.AutoSync(ctx) {
		t.Error("auto sync is on straight after setup")
	}
	if err := r.SetAutoSync(ctx, true); err != nil {
		t.Fatal(err)
	}
	if !r.AutoSync(ctx) {
		t.Error("auto sync did not turn on")
	}
	if err := r.SetAutoSync(ctx, false); err != nil {
		t.Fatal(err)
	}
	if r.AutoSync(ctx) {
		t.Error("auto sync did not turn off")
	}
}

func TestUnpushed(t *testing.T) {
	isolateGit(t, true)
	a := newMachine(t, newRemote(t))

	if a.Unpushed(ctx) {
		t.Error("a folder with no commits has nothing unpushed")
	}

	write(t, a, "cards/one.md", "one\n")
	sync(t, a)
	if a.Unpushed(ctx) {
		t.Error("Unpushed is true straight after a sync")
	}

	// A commit made while the remote cannot be reached stays unpushed.
	write(t, a, "cards/two.md", "two\n")
	if _, err := a.git(ctx, "remote", "set-url", Remote, filepath.Join(t.TempDir(), "gone.git")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Sync(ctx); err == nil {
		t.Fatal("sync to a missing remote: got no error")
	}
	if !a.Unpushed(ctx) {
		t.Error("Unpushed is false with a commit the remote does not have")
	}
}

func TestAStoppedSyncSaysSoAndLeavesNoLockFiles(t *testing.T) {
	isolateGit(t, true)
	a := newMachine(t, newRemote(t))
	write(t, a, "cards/one.md", "one\n")

	// Stopped before it starts.
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	_, err := a.Sync(stopped)
	if err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Errorf("got %v, want an error saying the sync was stopped", err)
	}

	// Stopped part-way through: at some point in each run the deadline lands
	// in the middle of a git command.
	for _, after := range []time.Duration{time.Millisecond, 5 * time.Millisecond, 20 * time.Millisecond, 60 * time.Millisecond} {
		write(t, a, "cards/one.md", "one, changed after "+after.String()+"\n")
		short, cancel := context.WithTimeout(ctx, after)
		a.Sync(short)
		cancel()
	}

	locks, _ := filepath.Glob(filepath.Join(a.Dir, ".git", "*.lock"))
	if len(locks) > 0 {
		t.Errorf("lock files were left behind: %v", locks)
	}
	// The folder is still in working order.
	sync(t, a)
}
