# goat-client — orientation for Claude Code sessions

> **You are in `dlf-dds/goat-client` — the IMPLEMENTATION repo.**
>
> Design, ADRs, and the implementation plan live in a separate repo,
> `dlf-dds/DesertBreadBird` (a.k.a. **goat-trunk**) at
> `~/src/github.com/dlf-dds/DesertBreadBird/`. Read design docs there
> by URL or local `cat`; **do not** commit there from a goat-client
> session. All Go daemon, Fyne GUI, mobile shell, packaging, and CI
> work happens HERE.

## First action of every session — verify cwd before touching anything

```bash
cd /Users/dene/src/github.com/dlf-dds/goat-client/   # MUST be this repo, not goat-trunk
pwd                                                   # confirm: ends in /goat-client (NOT /DesertBreadBird)
git remote get-url origin                             # confirm: ends in /goat-client.git
git fetch origin && git status                        # main should be clean
```

If `git status` shows files like `cartoon-peers.tf`, `active-work.yaml`,
or `docs/design/multi-agent-team-management.md` in untracked or modified
state, **you are in goat-trunk by mistake.** `cd` to goat-client and
try again — do not stash or commit those files; they belong to other
tracks.

## Where to read next

- **[`HANDOFF.md`](HANDOFF.md)** — the orientation anchor. Per-track
  scope, branch names, acceptance criteria, what to fork from netbird,
  cross-track coordination notes. Read this before starting work.
- **[`README.md`](README.md)** — public-facing project summary.
- **[`CONTRIBUTING.md`](CONTRIBUTING.md)** — branch naming, commit
  conventions (Conventional Commits + DCO sign-off + GPG sign +
  `[track: <name>]` trailer), PR process.
- **[`SECURITY.md`](SECURITY.md)** — vulnerability reporting.

## Authoritative design + ADRs (live in goat-trunk)

- [`docs/design/goat-client.md`](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/design/goat-client.md)
- [`docs/adr/0840-goat-client-cross-platform-daemon-gui.md`](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/adr/0840-goat-client-cross-platform-daemon-gui.md)
- [`docs/adr/0109-git-collaboration-trunk-based.md`](https://github.com/dlf-dds/DesertBreadBird/blob/main/docs/adr/0109-git-collaboration-trunk-based.md) — the change-control policy this repo enforces.

## Working-tree convention

Workers `cd` into the master goat-client checkout and provision a
worktree per track via `/iso enter <track-name>` (creates
`.claude/worktrees/<track-name>/` off `origin/main`). All `Edit`/`Write`
target the worktree path, never the master checkout. The master
checkout is read-only — same discipline as goat-trunk's ADR 0013.

## Post-merge closeout

Once a track's PR is merged — whether you merged it via `gh pr merge
--squash --delete-branch` or captain merged it from another session —
the per-track branch and worktree are dead weight. They accumulate
fast across parallel-track work, so close them out at end-of-session.

**Always prompt the operator before deleting branches or removing
worktrees. Closeout is destructive and must not run on the agent's
own initiative.**

Suggested closeout flow at end of session (or at the start of the
next session if the PR was merged after you left):

1. Confirm merge: `gh pr view <#> --json state,mergedAt`.
2. **Propose** to the operator, naming what will be deleted:
   "PR #<#> is merged. OK to delete remote branch
   `track/<name>` and remove worktree
   `.claude/worktrees/<name>/`?" Wait for explicit OK.
3. On OK:
   - `git push origin --delete track/<name>` (no-op if GitHub's
     squash-merge setting already deleted the remote branch; `gh pr
     merge --delete-branch` does this for you when *you* merge).
   - From the master checkout (not from inside the worktree
     itself), `git worktree remove .claude/worktrees/<name>`.
   - `git branch -D track/<name>` if a local ref remains.
4. If multiple worktrees are stale (common after a captain merges a
   batch), enumerate them in the prompt and confirm one OK covers
   the batch — don't escalate scope past what the operator
   authorized.

Refuse to remove a worktree with uncommitted changes; that's the
operator's call (commit, stash, or discard), not yours.

## Source of truth for forks

netbird upstream pinned at `3fc5a8d4a1fe308ff1068764a09b90b0859ab8fe`.
Local fork at `~/src/github.com/dfarrel1/netbird/` (one extra commit
`32d04da19` — embed-CA + ServerName-port-strip patch, already adopted
in this repo's `internal/ipc/grpc/` fork target).
