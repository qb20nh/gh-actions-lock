# Actions lock protections

GitHub currently ignores syntactically malformed lock YAML in the reproduced
upstream [#125](https://github.com/github/gh-actions-lock/issues/125) case.
The workflows fetch the exact candidate revision and read its raw Git blobs in a
separate, action-free job before consumers can execute. Export attributes and
checkout filters cannot hide or rewrite tracked workflows. The trusted validator publishes
the `Lock policy` status for the revision it inspected. CI and release consumers
must require successful validation, including PR merge revisions and merge
queue revisions.

The source graph also uses full commit SHAs. `--verify-frozen` audits remote
manifests recursively so pinning a parent cannot hide a mutable remote child.
Same-commit `$/` references remain bound to their defining source. Declared
`docker://` actions require full SHA-256 digests. Dockerfile images, job and service
containers, shell downloads, and tool installation have separate trust boundaries.

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
branch explicitly; the default branch must also be deployed and validated, and
both branch heads must remain unchanged during setup.

Deploy `.github/workflows/lock-policy.yml` on the repository's **default branch**
before requiring its status. Since December 8, 2025, `pull_request_target` always
uses that branch's workflow definition and commit, even when a PR targets a
different branch. Installing it only on a PR's base branch cannot activate the
event. The workflow posts separate statuses onto the exact PR head and merge
commits it validates. See [GitHub's source-selection change](https://github.blog/changelog/2025-11-07-actions-pull_request_target-and-environment-branch-protections-changes/).

Confirm the applicable Actions event policy permits `pull_request_target` for
this workflow. GitHub's default rule for affected public repositories currently
evaluates that event and will block it from November 2, 2026 unless an applicable
policy explicitly allows it. Check the policy and its insights before enabling
the required status; see [workflow execution protections](https://github.blog/changelog/2026-09-17-workflow-execution-protections-in-github-actions-generally-available/)
and [secure use of `pull_request_target`](https://docs.github.com/en/actions/reference/security/securely-using-pull_request_target).

The plan enables the full-SHA action requirement and replaces the selected
external allowlist with the reviewed exact references. GitHub-owned and verified
Marketplace creators receive no blanket exemption. It requires `Lock policy`
from GitHub Actions, requires the branch to be current, and enforces checks for
administrators. Existing unrelated checks, approval rules, restrictions,
signatures, and repository settings remain in place. A disabled Actions setting
or an existing local-only policy stops setup for explicit review.

Enable Dependency graph under repository Code security before requiring the
dependency-review check. The fork's initial review API request returned 403; its
[rerun](https://github.com/qb20nh/gh-actions-lock/actions/runs/38066247251)
now completes successfully. The review check remains blocking.

When the verifier or raw-tree helper changes, publish and test a new immutable
bootstrap commit, then update `LOCK_VERIFIER_SHA` in both validator workflows.
Candidate code is never used to build the trusted verifier.

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

The pre-commit hook builds the verifier from the exact staged tree and checks its
workflow/lock snapshot with `--require-lockfile --verify-local`, including
partially staged files, and requires full-SHA refs matching their recorded commits
throughout the inspected inventory. This offline check cannot discover remote
edges omitted from the recorded graph. It copies raw index blobs so ignore rules,
archive export attributes, and checkout filters cannot remove or rewrite tracked
files during validation. It needs Go, Git, Python 3, and Bash, and uses no token or
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
- [Implemented combo validation](../test/runtime/evidence/2026-10-11-combo.md)
