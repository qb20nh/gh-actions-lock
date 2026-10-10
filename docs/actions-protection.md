# Actions lock protections

GitHub currently ignores syntactically malformed lock YAML in the reproduced
upstream [#125](https://github.com/github/gh-actions-lock/issues/125) case.
The workflows validate the exact candidate revision in a separate,
action-free job before consumers can execute. The trusted validator publishes
the `Lock policy` status for the revision it inspected. CI and release consumers
must require successful validation, including PR merge revisions and merge
queue revisions.

The source graph also uses full commit SHAs. `--verify-frozen` audits remote
manifests recursively so pinning a parent cannot hide a mutable remote child.
Same-commit `$/` references remain bound to their defining source. Image tags,
shell downloads, and tool installation have separate trust boundaries.

## Enable repository settings

The reviewed allowlist is [.github/actions-policy.json](../.github/actions-policy.json).
It must include every external action, composite subaction, and reusable workflow
reference in the frozen graph, with its complete path and full SHA. Updating
dependencies requires reviewing and deploying the updated policy as well.
The current eight entries match the regenerated lock closure. The
`actions/attest-build-provenance/predicate@36fa7d0…` entry retains the subaction
path from its [immutable parent manifest](https://github.com/actions/attest-build-provenance/blob/ef244123eb79f2f7a7e75d99086184180e6d0018/action.yml);
the lock's repository key alone loses that path.

Use an authenticated `gh` installation with repository Administration write
permission and read access to contents and statuses. The workflow's ordinary
`GITHUB_TOKEN` cannot administer the repository. Do not store an administrator
token in the workflow to perform this setup.

```sh
script/apply-actions-protection --repo qb20nh/gh-actions-lock
script/apply-actions-protection --repo qb20nh/gh-actions-lock --apply
```

The first command prints a read-only plan. Apply only after the default branch
contains the matching policy and has a successful `Lock policy` status from
GitHub Actions. The script checks that precondition and the branch revision
before writing, then reads the settings back. `--branch NAME` selects another
branch explicitly.

The plan enables the full-SHA action requirement and replaces the selected
external allowlist with the reviewed exact references. GitHub-owned and verified
Marketplace creators receive no blanket exemption. It requires `Lock policy`
from GitHub Actions, requires the branch to be current, and enforces checks for
administrators. Existing unrelated checks, approval rules, restrictions,
signatures, and repository settings remain in place. A disabled Actions setting
or an existing local-only policy stops setup for explicit review.

New branch protection requires PRs with zero approvals, allowing the personal
repository's sole owner to merge their own reviewed work after checks pass.
Existing approval requirements are preserved. The script does not alter rulesets;
a branch governed only by rulesets needs separately reviewed administration.

The selected-action API applies to public repositories. GitHub implicitly permits
owned components and local `./` and `$/` uses; the frozen graph audit covers owned
remote references. GitHub's SHA setting permits tagged reusable workflows, so
the exact allowlist and recursive audit remain necessary. These are documented
platform limits, not proof that our settings have already been enabled.

## Protect the validator definition

[CODEOWNERS](../.github/CODEOWNERS) assigns workflow, verifier, policy, hook, and
dependency changes to `@qb20nh`. GitHub reads that ownership from a PR's base
branch. Requiring approval by an independent code owner provides a stronger
barrier against a PR replacing or removing the gate. That requires adding an
independent owner or integrator first: a PR author cannot approve their own PR,
so requiring the sole owner's approval would strand their own changes.

The setup script intentionally preserves existing review rules and does not
enable sole-owner approval requirements. Without independent review or a
separately administered validator, someone permitted to modify trusted workflow
definitions can also replace their checks. Binding a required status to the
GitHub Actions app does not distinguish two workflows using that app.

## Enable local validation

```sh
git config --local core.hooksPath .githooks
```

The pre-commit hook builds the current verifier and checks the exact staged
workflow/lock snapshot with `--require-lockfile --verify-local`, including
partially staged files, and requires full-SHA refs matching their recorded commits
throughout the inspected inventory. This offline check cannot discover remote
edges omitted from the recorded graph. It needs Go, Git, Python 3, `tar`, and `rg`, and uses no token or
network for lock verification. A build may need to download uncached Go modules.
It does not repair or stage files automatically.

For an authenticated recursive graph audit before each commit, opt in with:

```sh
GH_ACTIONS_LOCK_VERIFY_FROZEN=1 git commit
```

Hooks are local and can be bypassed; protected validation remains the merge
boundary. The protection planner is covered by `script/test-actions-protection`,
which checks preservation, idempotency, source binding, and rejection before
administration writes.

## References

- [Actions permissions REST API](https://docs.github.com/en/rest/actions/permissions)
- [Branch protection REST API](https://docs.github.com/en/rest/branches/branch-protection)
- [Repository action policy limits](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/enabling-features-for-your-repository/managing-github-actions-settings-for-a-repository)
- [Hosted workaround evidence](../test/runtime/evidence/2026-10-10-workarounds.md)
