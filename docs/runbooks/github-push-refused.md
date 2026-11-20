# Runbook: git push to this repository is refused with HTTP 403

**Owner role:** repo owner  |  **Severity if this happens:** medium
**Last verified:** 2026-11-20, 027d2e2 (symptom and fix both observed on this repository)

## When to use
`git push` from a Claude Code cloud session fails right after a repository was newly attached.

## Prerequisites
Owner (or org admin) access to the GitHub account that owns the repository.

## Steps
1. Read the message: `remote: Claude doesn't have GitHub access to <owner>/<repo> ...` then `fatal: unable to access ...: The requested URL returned error: 403`.
2. Install or extend the Claude GitHub App on the repository: https://github.com/apps/claude/installations/select_target (select the account, then the repository).
   If GitHub is not connected at all, reconnect it from the Claude settings connectors page.
3. Retry the push from the repository checkout: `git -c push.negotiate=true push -u origin <branch>` *(expected: a `[new branch]` or fast-forward line)*.

## Troubleshooting
| Symptom | Diagnosis | Fix |
|---|---|---|
| Clone and fetch work, only push fails | read access is granted, write is not | step 2 |
| `push negotiation failed` warning | harmless when the push still succeeds | ignore if the push line shows the branch updated |
| Push rejected as non-fast-forward | remote branch has commits you lack | `git fetch origin <branch>` then `git merge --no-edit FETCH_HEAD`, push again; never force-push |

## Verify
`git log origin/<branch> -1` shows your commit; the branch appears on GitHub.

## Roll back / undo
Uninstall or restrict the app from the GitHub installation settings.

## Escalate
Org admin if you cannot change app installations.
