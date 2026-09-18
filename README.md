# coord

Short file leases shared between Claude Code sessions working on one checkout.
The goal is narrow: stop two sessions touching one file in the same minute,
which is what makes an agent's edit fail on stale text and forces re-reads.

Leases are a side effect of tool use, not a discipline. Hooks take a lease on
every Read, Edit, Write and detected shell write. Leases slide on each touch
and expire on their own. A tool call on a file another session holds waits in
line inside the hook until the lease is free, then proceeds. The model never
sees a message and never spends a turn on it.

    read lease    30s   COORD_READ_LEASE
    write lease   60s   COORD_WRITE_LEASE
    max wait      2m    COORD_MAX_WAIT   (then proceed anyway)

Readers share; readers wait for writers; writers wait for everyone. Waiters
are served in arrival order. Blocked sessions don't renew leases, so two
sessions waiting on each other both clear within one lease.

State lives in the repository's common Git directory as `coord.json`, so
linked worktrees share one table. Entries with a known Claude process PID
are removed on the next state access after that process dies. Entries with
an unknown PID expire after 12 hours without activity.

## Install

Requires Go 1.26 or newer, Git, and Claude Code on macOS or Linux. The code
uses Unix file locking and process signals; Windows is not supported.
There are no third-party Go dependencies.

```sh
git clone https://github.com/JeremyVun/coord.git
cd coord
make install
export PATH="$HOME/go/bin:$PATH"
coord help
```

`make install` builds to `~/go/bin/coord`. Override the destination with
`make install BIN=/path/to/coord`. Ensure the installed binary is on the
`PATH` available to Claude Code, or use its absolute path in the hooks below.

## Commands the model runs

    coord status [-v]        live sessions, notes, leases with seconds left
    coord note "<intent>"    one line of what this session is doing
    coord claim <path>...    take a write lease by hand (directories end with /)
    coord release --all      drop this session's leases early

## Set up hooks

Merge the `hooks` entries from [examples/claude-settings.json](examples/claude-settings.json)
into `~/.claude/settings.json` for all projects, or `.claude/settings.json`
for one project. Preserve any existing settings and hook entries. Restart
Claude Code after changing the configuration.

The example registers these [Claude Code hooks](https://code.claude.com/docs/en/hooks):

    PreToolUse   Read|Edit|Write|MultiEdit|NotebookEdit|Bash   coord hook pre
    PostToolUse  same                                          coord hook post
    SessionStart                                               coord hook start
    SessionEnd                                                 coord hook end

`hook start` prints the status table into context only when another session
holds leases or a note. The only outright denies are `git update-ref`, a
recursive `rm` with a relative path, and a recursive `rm` of the checkout
or any directory above it.

The example gives the pre-hook 180 seconds to finish, allowing the default
two-minute wait. If you increase `COORD_MAX_WAIT`, increase the hook timeout
too. Calls touching several repositories can wait once per repository.

Set `COORD_READ_LEASE`, `COORD_WRITE_LEASE`, and `COORD_MAX_WAIT` in the
environment that launches Claude Code. Values use Go duration syntax, such
as `30s` or `2m`.

## Limits

Shell writes are detected for redirects, `sed -i`, `tee`, `mv`, `cp`, `rm`,
`touch`, `mkdir`, `dd of=`, `gofmt -w`, `prettier --write`, `eslint --fix`
and `git checkout|restore|rm|mv|add`. Whole-tree operations (`git restore .`,
`git stash`, `git reset --hard`, `git clean -f`) take a write lease on the
checkout root, which waits for every lease any other session holds. Branch
`checkout` and `switch` take no lease. Inline python, perl or node scripts
that open files themselves are invisible; claim those paths by hand. Shell
reads (`cat`, `grep`) take no lease.

Coord provides advisory coordination. Tools proceed when the wait expires,
and hook errors do not block them. Leases can expire during long operations.
The shell parser is best effort, so the command checks are not a security
boundary. Use this with cooperating sessions on a trusted local checkout.

Linked worktrees coordinate using the same relative paths, even when those
paths refer to separate files in different worktrees. Separate clones do
not share state.

## Local data

Coord makes no network requests. Its state contains session IDs, process
IDs, notes, relative file paths, and timestamps. The state and lock files
are inside Git's metadata directory and are not tracked. They are created
with mode `0644`, subject to your umask, so other local users may be able to
read them. Avoid putting secrets in notes or sharing state files in reports.

## Development

```sh
make build    # build ./coord
make test     # run tests
make check    # check formatting, run go vet and race-enabled tests
```

CI runs the checks and build on Linux and macOS. Include a regression test
with behavior changes. Use synthetic paths and session IDs in bug reports.

## License

[MIT](LICENSE).
