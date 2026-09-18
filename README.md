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

State lives in `<repo>/.git/coord.json`, so every worktree of a repository
shares one table. Each session entry is tied to its Claude process PID and
disappears when that process dies.

## Commands the model runs

    coord status [-v]        live sessions, notes, leases with seconds left
    coord note "<intent>"    one line of what this session is doing
    coord claim <path>...    take a write lease by hand (directories end with /)
    coord release --all      drop this session's leases early

## Hooks (registered in ~/.claude/settings.json)

    PreToolUse   Read|Edit|Write|MultiEdit|NotebookEdit|Bash   coord hook pre
    PostToolUse  same                                          coord hook post
    SessionStart                                               coord hook start
    SessionEnd                                                 coord hook end

`hook start` prints the status table into context only when another session
holds leases or a note. The only outright denies are `git update-ref`, a
recursive `rm` with a relative path, and a recursive `rm` of the checkout
or any directory above it.

## Limits

Shell writes are detected for redirects, `sed -i`, `tee`, `mv`, `cp`, `rm`,
`touch`, `mkdir`, `dd of=`, `gofmt -w`, `prettier --write`, `eslint --fix`
and `git checkout|restore|rm|mv|add`. Whole-tree operations (`git restore .`,
`git stash`, `git reset --hard`, `git clean -f`) take a write lease on the
checkout root, which waits for every lease any other session holds. Branch
`checkout` and `switch` take no lease. Inline python, perl or node scripts
that open files themselves are invisible; claim those paths by hand. Shell
reads (`cat`, `grep`) take no lease.

    make test      go test ./...
    make install   builds to ~/go/bin/coord
