// Package gitsync keeps the notes folder in step with a git remote, so the
// same notes can be used on more than one machine. It is not part of upstream
// lazymark; it was added in this fork.
//
// The notes folder is its own git repository. A sync commits local changes,
// fetches, merges what the remote has, and pushes. It works by running the
// system git, so it uses the machine's normal git and SSH setup.
//
// Conflicts are never resolved automatically. When a merge conflicts, the sync
// stops and reports the files; the user fixes them and syncs again.
package gitsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/MathiasDrizzy/lazymark/internal/i18n"
)

// Remote is the name of the git remote the notes folder syncs with.
const Remote = "origin"

// autoSyncKey is the git config setting, stored in the notes folder's own
// repository, that turns automatic sync on.
const autoSyncKey = "lazymark.autosync"

// stopGrace is how long git gets to stop cleanly before it is killed.
const stopGrace = 5 * time.Second

// ConnectTimeout is how long ssh waits for the server to answer.
const ConnectTimeout = 8 * time.Second

// Errors a caller may want to tell apart.
var (
	// ErrNotSetUp means the notes folder is not a git repository of its own.
	ErrNotSetUp = i18n.NewError(
		"la sincronización no está configurada en esta carpeta (ejecuta: lazymark sync setup --remote <url>)",
		"sync is not set up for this folder (run: lazymark sync setup --remote <url>)")
	// ErrNoRemote means the repository has no remote to sync with.
	ErrNoRemote = i18n.NewError(
		"la carpeta de notas no tiene un remoto de git (ejecuta: lazymark sync setup --remote <url>)",
		"the notes folder has no git remote (run: lazymark sync setup --remote <url>)")
	// ErrNoIdentity means git has no name and email to commit with.
	ErrNoIdentity = i18n.NewError(
		"git no tiene nombre ni correo para hacer commits; configúralos primero:\n"+
			"  git config --global user.name \"Tu Nombre\"\n"+
			"  git config --global user.email \"tu@ejemplo.com\"",
		"git has no name and email to commit with; set them first:\n"+
			"  git config --global user.name \"Your Name\"\n"+
			"  git config --global user.email \"you@example.com\"")
)

// Repo is the notes folder as a git repository.
type Repo struct {
	Dir string

	// Interactive lets git and ssh ask questions on the terminal, such as a
	// key passphrase or whether to trust a new host. Leave it false while the
	// board is on screen: questions would corrupt the display, so git is told
	// to fail instead of asking.
	Interactive bool

	// sshCommand, when set, is used as GIT_SSH_COMMAND.
	sshCommand string
}

// Result is the outcome of a sync that did not fail outright.
type Result struct {
	// Conflicts lists files, relative to the notes folder, that have conflicts
	// to fix by hand. When it is not empty the sync stopped before pushing.
	Conflicts []string
	// Pulled is true when files in the notes folder may have changed.
	Pulled bool
	// Pushed is true when local commits were sent to the remote.
	Pushed bool
}

// IsRepo reports whether the notes folder is the top of its own git
// repository. A folder that merely sits inside some other repository does not
// count, so a sync can never commit files outside the notes folder.
func (r Repo) IsRepo(ctx context.Context) bool {
	top, err := r.git(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return false
	}
	return samePath(top, r.Dir)
}

// AutoSync reports whether automatic sync is turned on for the notes folder.
func (r Repo) AutoSync(ctx context.Context) bool {
	if !r.IsRepo(ctx) {
		return false
	}
	v, _ := r.git(ctx, "config", "--local", "--get", "--bool", autoSyncKey)
	return v == "true"
}

// SetAutoSync turns automatic sync on or off for the notes folder.
func (r Repo) SetAutoSync(ctx context.Context, on bool) error {
	if !r.IsRepo(ctx) {
		return ErrNotSetUp
	}
	_, err := r.git(ctx, "config", "--local", autoSyncKey, fmt.Sprint(on))
	return err
}

// RemoteURL returns the URL the notes folder syncs with, or "" if none is set.
func (r Repo) RemoteURL(ctx context.Context) string {
	url, _ := r.git(ctx, "remote", "get-url", Remote)
	return url
}

// SSHCommand returns the ssh command configured for the notes folder, or "".
func (r Repo) SSHCommand(ctx context.Context) string {
	cmd, _ := r.git(ctx, "config", "--local", "--get", "core.sshCommand")
	return cmd
}

// SetupOptions is what Setup needs to know.
type SetupOptions struct {
	// Remote is the git URL to sync with. Required unless the repository
	// already has one.
	Remote string
	// SSHKey is the path of a private key to use for this repository only.
	// Empty means use whatever ssh would use anyway.
	SSHKey string
}

// Setup prepares the notes folder for syncing: it makes the folder a git
// repository if it is not one, and records the remote and the SSH key. It
// does not sync and does not turn automatic sync on; the caller does both
// once Setup succeeds.
func (r Repo) Setup(ctx context.Context, opts SetupOptions) error {
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return err
	}

	if !r.IsRepo(ctx) {
		if _, err := r.git(ctx, "init"); err != nil {
			return err
		}
		// Name the first branch main, whatever this git's default is.
		if _, err := r.git(ctx, "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
			return err
		}
	}

	name, _ := r.git(ctx, "config", "user.name")
	email, _ := r.git(ctx, "config", "user.email")
	if name == "" || email == "" {
		return ErrNoIdentity
	}

	if opts.Remote != "" {
		args := []string{"remote", "add", Remote, opts.Remote}
		if r.RemoteURL(ctx) != "" {
			args = []string{"remote", "set-url", Remote, opts.Remote}
		}
		if _, err := r.git(ctx, args...); err != nil {
			return err
		}
	} else if r.RemoteURL(ctx) == "" {
		return ErrNoRemote
	}

	if opts.SSHKey != "" {
		key, err := expandPath(opts.SSHKey)
		if err != nil {
			return err
		}
		if info, err := os.Stat(key); err != nil {
			return fmt.Errorf("SSH key: %w", err)
		} else if info.IsDir() {
			return fmt.Errorf("SSH key: %s is a folder", key)
		}
		// IdentitiesOnly makes ssh offer this key and no others.
		sshCmd := "ssh -i " + shellQuote(key) + " -o IdentitiesOnly=yes"
		if _, err := r.git(ctx, "config", "--local", "core.sshCommand", sshCmd); err != nil {
			return err
		}
	}
	return nil
}

// Gitignore is the .gitignore that EnsureGitignore writes.
//
// The trash is left out on purpose: its index is one file that every delete
// rewrites, so syncing it would conflict whenever both machines deleted
// something. A deleted note simply disappears from the other machine, and
// stays in the trash of the machine it was deleted on.
const Gitignore = `# Written by lazymark sync. Keeps the trash and editor leftovers out of the sync.
.trash/
.lazymark-*.tmp
.DS_Store
*.swp
*.swo
*~
`

// EnsureGitignore adds a .gitignore that keeps the trash and editor leftovers
// out of the sync, if the notes folder has none, and reports whether it wrote
// one. Call it before the first sync so those files are never committed.
// Every machine writes the same content, so the copies merge cleanly.
func (r Repo) EnsureGitignore() (written bool, err error) {
	path := filepath.Join(r.Dir, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return false, nil // keep the one that is there
	}
	return true, os.WriteFile(path, []byte(Gitignore), 0o644)
}

// Sync brings the notes folder and the remote into step:
//
//  1. Commit any local changes.
//  2. Fetch from the remote.
//  3. Merge what the remote has.
//  4. Push.
//
// If the merge conflicts, Sync stops and returns the conflicted files. The
// repository is left mid-merge, with conflict markers in those files. Once the
// markers are gone, the next Sync finishes the merge and carries on.
func (r Repo) Sync(ctx context.Context) (Result, error) {
	var res Result
	if ctx.Err() != nil {
		return res, errors.New("sync stopped before it started")
	}
	if !r.IsRepo(ctx) {
		return res, ErrNotSetUp
	}
	// Give up on a server that does not answer after a few seconds, so that
	// being offline never holds the program up for long.
	base := r.SSHCommand(ctx)
	if base == "" {
		base = os.Getenv("GIT_SSH_COMMAND")
	}
	if base == "" {
		base = "ssh"
	}
	r.sshCommand = base + " -o ConnectTimeout=" + fmt.Sprint(int(ConnectTimeout.Seconds()))
	if !r.Interactive {
		// Make ssh fail instead of asking for a passphrase or host check.
		r.sshCommand += " -o BatchMode=yes"
	}

	if r.mergeInProgress(ctx) {
		unresolved, err := r.Unresolved(ctx)
		if err != nil {
			return res, err
		}
		if len(unresolved) > 0 {
			return Result{Conflicts: unresolved}, nil
		}
		// Every conflict has been fixed by hand: finish the merge.
		if _, err := r.git(ctx, "add", "-A"); err != nil {
			return res, err
		}
		if _, err := r.git(ctx, "commit", "--no-edit"); err != nil {
			return res, err
		}
		res.Pulled = true
	} else if err := r.commitAll(ctx); err != nil {
		return res, err
	}

	if r.RemoteURL(ctx) == "" {
		return res, ErrNoRemote
	}

	// If the push is rejected because the remote moved on in the meantime,
	// fetch and merge once more before giving up.
	var pushErr error
	for range 2 {
		if _, err := r.git(ctx, "fetch", Remote); err != nil {
			return res, fmt.Errorf("could not reach the remote: %w", err)
		}

		if !r.refExists(ctx, "HEAD") {
			// Nothing local yet: take the remote's branch as it is.
			branch := r.remoteBranchToAdopt(ctx)
			if branch == "" {
				return res, nil // nothing on either side
			}
			if _, err := r.git(ctx, "checkout", "-B", branch, "--track", Remote+"/"+branch); err != nil {
				return res, err
			}
			res.Pulled = true
			return res, nil
		}

		branch, err := r.git(ctx, "symbolic-ref", "--short", "HEAD")
		if err != nil {
			return res, errors.New("the notes folder is not on a branch; check it out with git and try again")
		}
		remoteRef := "refs/remotes/" + Remote + "/" + branch

		if r.refExists(ctx, remoteRef) && !r.isAncestor(ctx, remoteRef, "HEAD") {
			// Merging by the short name makes the conflict markers read
			// "origin/main", which is easier on the eye than the full ref.
			_, err := r.git(ctx, "merge", "--no-edit", "--allow-unrelated-histories", Remote+"/"+branch)
			if err != nil {
				conflicts, cerr := r.unmerged(ctx)
				if cerr == nil && len(conflicts) > 0 {
					return Result{Conflicts: conflicts, Pulled: true}, nil
				}
				return res, err
			}
			res.Pulled = true
		}

		if r.refExists(ctx, remoteRef) && r.sameCommit(ctx, remoteRef, "HEAD") {
			return res, nil // nothing to push
		}
		if _, pushErr = r.git(ctx, "push", "-u", Remote, branch); pushErr == nil {
			res.Pushed = true
			return res, nil
		}
	}
	return res, fmt.Errorf("could not push: %w", pushErr)
}

// Unresolved lists the conflicted files that still need fixing by hand:
// those that still contain conflict markers.
//
// A conflict where one side deleted the file and the other changed it has no
// markers. It counts as resolved in favour of keeping the file, so an edit is
// never lost; delete the file by hand if the deletion was what you wanted.
func (r Repo) Unresolved(ctx context.Context) ([]string, error) {
	conflicted, err := r.unmerged(ctx)
	if err != nil {
		return nil, err
	}
	var unresolved []string
	for _, rel := range conflicted {
		data, err := os.ReadFile(filepath.Join(r.Dir, filepath.FromSlash(rel)))
		if err != nil {
			continue // deleted: nothing left to fix
		}
		if hasConflictMarkers(data) {
			unresolved = append(unresolved, rel)
		}
	}
	return unresolved, nil
}

// HasLocalChanges reports whether the notes folder has changes that have not
// been committed yet.
func (r Repo) HasLocalChanges(ctx context.Context) bool {
	out, err := r.git(ctx, "status", "--porcelain")
	return err == nil && out != ""
}

// Unpushed reports whether the notes folder has commits that are not on the
// remote, going by what the last sync saw there. It does not contact the
// remote.
func (r Repo) Unpushed(ctx context.Context) bool {
	if !r.refExists(ctx, "HEAD") {
		return false // no commits at all
	}
	branch, err := r.git(ctx, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return true
	}
	remoteRef := "refs/remotes/" + Remote + "/" + branch
	return !r.refExists(ctx, remoteRef) || !r.isAncestor(ctx, "HEAD", remoteRef)
}

// commitAll commits every change in the notes folder, if there are any.
func (r Repo) commitAll(ctx context.Context) error {
	if _, err := r.git(ctx, "add", "-A"); err != nil {
		return err
	}
	// "diff --cached --quiet" exits 1 when something is staged.
	if _, code, _ := r.run(ctx, "diff", "--cached", "--quiet"); code == 0 {
		return nil
	}
	_, err := r.git(ctx, "commit", "-m", commitMessage())
	return err
}

func commitMessage() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "lazymark: sync"
	}
	return "lazymark: sync from " + strings.TrimSuffix(host, ".local")
}

func (r Repo) mergeInProgress(ctx context.Context) bool {
	return r.refExists(ctx, "MERGE_HEAD")
}

// unmerged lists the files git considers conflicted.
func (r Repo) unmerged(ctx context.Context) ([]string, error) {
	out, err := r.git(ctx, "diff", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

func (r Repo) refExists(ctx context.Context, ref string) bool {
	_, code, err := r.run(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil && code == 0
}

func (r Repo) isAncestor(ctx context.Context, ancestor, descendant string) bool {
	_, code, err := r.run(ctx, "merge-base", "--is-ancestor", ancestor, descendant)
	return err == nil && code == 0
}

func (r Repo) sameCommit(ctx context.Context, a, b string) bool {
	ha, errA := r.git(ctx, "rev-parse", a+"^{commit}")
	hb, errB := r.git(ctx, "rev-parse", b+"^{commit}")
	return errA == nil && errB == nil && ha == hb
}

// remoteBranchToAdopt picks the remote branch to start from when the local
// repository has no commits: the branch with the local branch's name if the
// remote has it, otherwise the remote's default branch.
func (r Repo) remoteBranchToAdopt(ctx context.Context) string {
	if local, err := r.git(ctx, "symbolic-ref", "--short", "HEAD"); err == nil {
		if r.refExists(ctx, "refs/remotes/"+Remote+"/"+local) {
			return local
		}
	}
	// "ls-remote --symref" prints a line like "ref: refs/heads/main\tHEAD".
	out, err := r.git(ctx, "ls-remote", "--symref", Remote, "HEAD")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			branch, _, _ := strings.Cut(rest, "\t")
			if r.refExists(ctx, "refs/remotes/"+Remote+"/"+branch) {
				return branch
			}
		}
	}
	return ""
}

// git runs a git command in the notes folder and returns its output with
// surrounding whitespace removed. A non-zero exit is an error carrying what
// git printed.
func (r Repo) git(ctx context.Context, args ...string) (string, error) {
	out, code, err := r.run(ctx, args...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("git %s: %s", args[0], out)
	}
	return out, nil
}

// run runs a git command in the notes folder. It returns git's exit code, and
// an error only if git could not be run at all. On a non-zero exit the output
// is what git printed to stderr, or stdout if stderr was empty.
func (r Repo) run(ctx context.Context, args ...string) (output string, code int, err error) {
	// Housekeeping that git decides to do must finish before git returns,
	// so that nothing is left running after the program has exited.
	full := append([]string{"-c", "gc.autoDetach=false", "-c", "maintenance.autoDetach=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = r.Dir

	// When a sync is stopped or times out, ask git to stop instead of
	// killing it outright: git then removes its lock files on the way out.
	// It is killed only if it has not gone after stopGrace.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = stopGrace
	if !r.Interactive {
		// Nobody is at the keyboard to press ctrl+c, so stopping has to
		// reach the helpers git starts (ssh) as well as git itself.
		ownGroup(cmd)
		cmd.Cancel = func() error { return interruptGroup(cmd) }
	}

	cmd.Env = append(os.Environ(), "GIT_MERGE_AUTOEDIT=no")
	if r.Interactive {
		cmd.Stdin = os.Stdin
	} else {
		cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	}
	if r.sshCommand != "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND="+r.sshCommand)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err = cmd.Run()
	if ctx.Err() != nil && !r.Interactive {
		killGroup(cmd) // anything that ignored the request to stop
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
		return strings.TrimSpace(stdout.String()), 0, nil
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "", -1, fmt.Errorf("git %s: timed out", args[0])
	case ctx.Err() != nil:
		return "", -1, fmt.Errorf("git %s: stopped", args[0])
	case errors.As(err, &exit):
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return msg, exit.ExitCode(), nil
	}
	return "", -1, fmt.Errorf("could not run git: %w", err)
}

func hasConflictMarkers(data []byte) bool {
	return bytes.HasPrefix(data, []byte("<<<<<<< ")) || bytes.Contains(data, []byte("\n<<<<<<< "))
}

func samePath(a, b string) bool {
	resolve := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		return filepath.Clean(p)
	}
	return resolve(a) == resolve(b)
}

// expandPath turns a path the user typed into an absolute one, expanding a
// leading "~".
func expandPath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return filepath.Abs(p)
}

// shellQuote wraps text in single quotes for the shell git uses to run the
// ssh command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
