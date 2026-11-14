# What this fork adds

This is a personal fork of [lazymark](https://github.com/MathiasDrizzy/lazymark). Upstream is kept as it is wherever possible, so its updates can still be merged in. The additions live in new files, with a few lines in `cmd/lazymark/main.go` to call them and short sections in `README.md` and `docs/cli.md`.

| Addition | Where |
|---|---|
| Sync between machines through git | `internal/gitsync/`, `internal/cli/sync.go` |
| Clean uninstall | `internal/cli/uninstall.go` |

The new commands speak English and Spanish. In the other six interface languages they show English.

## Privacy

lazymark makes no network connections. The built program contains no networking code: there is no telemetry and no update check. This was checked for this fork by listing the packages linked into the program.

Two things reach outside the program, and both are yours to start:

- **Sync** runs your system's `git`, which talks to the remote you set up and nothing else.
- **The MCP server** (`lazymark mcp`) lets an AI agent read and change your notes. See [The MCP server and local models](#the-mcp-server-and-local-models).

## Sync between machines

Sync is off until you set it up. It uses your system's `git` and SSH, and keeps its settings in the notes folder's own git config.

### Set up

Create an empty repository on your git server, then on the first machine:

```bash
lazymark sync setup --remote git@gitea.example.com:you/notes.git
```

To use a specific SSH key for this folder only:

```bash
lazymark sync setup --remote git@gitea.example.com:you/notes.git --ssh-key ~/.ssh/id_ed25519
```

Run the same command on the second machine. If it has no notes yet it receives the first machine's; if it has some, the two sets are combined.

Setup makes the notes folder a git repository, records the remote and key, runs a first sync, and turns automatic sync on only if that sync worked.

### What automatic sync does

- **When lazymark opens,** it syncs first, so you see what the other machine changed. This happens before the interface appears, so git can ask for a key passphrase.
- **When lazymark closes,** it syncs if anything changed. With nothing to send it does not contact the remote.
- **While lazymark is open,** nothing syncs. Run `lazymark sync now` in another terminal if you need to.

A sync commits your changes, fetches, merges and pushes. If the remote cannot be reached, lazymark says so and carries on; your notes stay saved and go out with the next sync that works. A server that does not answer is given up on after 8 seconds.

### Conflicts

If both machines changed the same lines of the same note, the sync stops and names the notes. Nothing is overwritten and nothing is chosen for you.

1. Open each note it names. Both versions are in the file:

   ```
   <<<<<<< HEAD
   this machine's version
   =======
   the other machine's version
   >>>>>>> origin/main
   ```

2. Keep the lines you want and delete the three marker lines.
3. Run `lazymark sync now`.

A conflict found when lazymark opens is shown before the interface starts and waits for Enter.

Because a task is one line in a note, two machines conflict only when they change the same line or lines next to each other. Moving different tasks, or editing different notes, merges cleanly.

### What is not synced

Setup writes a `.gitignore` that leaves out the trash (`.trash/`), lazymark's temporary files and editor leftovers. A note deleted on one machine disappears from the other, and stays in the trash of the machine it was deleted on.

If the folder already has a `.gitignore`, it is left alone, and setup tells you if it does not exclude the trash.

### An Obsidian vault that already syncs

If the notes folder is an Obsidian vault that something else already syncs (the Obsidian Git plugin, for example), do not set up lazymark's sync on it as well. Two tools committing to the same folder will get in each other's way. Use one.

### Commands

| Command | What it does |
|---|---|
| `lazymark sync setup --remote <url> [--ssh-key <file>]` | Set the folder up and turn automatic sync on |
| `lazymark sync now` | Sync once |
| `lazymark sync status` | Show the remote, key, unsynced changes and conflicts |
| `lazymark sync off` / `lazymark sync on` | Turn automatic sync off or on |

Add `--dir <folder>` to act on a folder other than the configured one. Exit code 4 means the sync stopped on a conflict.

## Removing lazymark

Everything lazymark puts on a machine is one of four things:

| What | Where (macOS) |
|---|---|
| The program | Wherever you installed it |
| Settings and your themes | `~/Library/Application Support/lazymark/` |
| Editor plugins, if you installed them | Your micro, vim or nano configuration |
| The trash | `.trash/` inside the notes folder |

It keeps no cache or logs and leaves no process running.

```bash
lazymark uninstall
```

This lists what is on the machine, asks, then removes the program, the settings folder and the editor plugins. Your notes are never touched, and neither is the git data in the notes folder.

```bash
lazymark uninstall --trash
```

This also empties the trash.

If the program was installed with Homebrew, the command removes the settings and plugins and tells you to run `brew uninstall lazymark` for the program itself.

Things lazymark did not create, which stay until you remove them:

| What | How to remove it |
|---|---|
| The source folder you built from | Delete the folder |
| Go's build and module caches | `go clean -cache -modcache`; this affects every Go project on the machine |
| The repository on your git server | Delete it on the server |
| Your server's line in `~/.ssh/known_hosts` | `ssh-keygen -R <host>` |

## The MCP server and local models

`lazymark mcp` does not contact any model, so it has no endpoint setting. It waits on standard input for a program to ask it things: list the notes, read one, search, move a task. That program is an MCP client, and the client is what talks to a model.

So where your notes go is decided by the client you connect, not by lazymark:

- A client pointed at a model on your own machine or network keeps the notes there.
- A client pointed at a hosted model sends whatever it reads to that provider.

To use it with a local inference server, you need an MCP client that can start a local command over stdio and that lets you set the address of an OpenAI-compatible server. The model also has to support tool calling.

## Still to do

Carried over from the earlier `tui_board` project, in the order they were ranked:

- **Ordering cards by hand across notes.** Upstream reorders tasks within one note; across notes, cards follow the order of the notes.
- **An archive column** that can be folded away. Upstream can hide finished cards and has a "done" tag, but no archive column.
- **Google Calendar** as the backend for dated tasks. Tasks have start and due dates but no times or durations yet.
- **Syncing while lazymark is open,** with the state shown in the interface.
