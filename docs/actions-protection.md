# Actions lock protections

For installation and deployment in an application repository, start with
[Use this fork in another repository](using-this-fork.md). This page records
the fork's settings, verification evidence, and trust boundaries.

GitHub currently ignores syntactically malformed lock YAML in the reproduced
upstream [#125](https://github.com/github/gh-actions-lock/issues/125) case.
The workflows fetch the exact candidate revision and read its raw Git blobs in a
separate, action-free job before consumers can execute. Export attributes and
checkout filters cannot hide or rewrite tracked workflows. The trusted validator
publishes the `Lock policy` status for the revision it inspected. CI and release
consumers must require successful validation, including PR merge revisions and,
where merge queues are available, merge queue revisions.

The source graph also uses full commit SHAs. `--verify-frozen` audits remote
manifests recursively so pinning a parent cannot hide a mutable remote child.
Same-commit `$/` references remain bound to their defining source. Declared
`docker://` actions require full SHA-256 digests. Dockerfile images, job and service
containers, shell downloads, and tool installation have separate trust boundaries.

The deployed default-branch policy has now run through actual
`pull_request_target` delivery. The [valid control](https://github.com/qb20nh/gh-actions-lock/actions/runs/38073289838)
posted successful `Lock policy` statuses from GitHub Actions onto both PR head
and merge commits. The [malformed-lock control](https://github.com/qb20nh/gh-actions-lock/actions/runs/38073405914)
failed both statuses and skipped every CI and dependency-review consumer.
[Post-merge evidence](../test/runtime/evidence/2026-10-11-postmerge.md) records
the exact revisions and remaining activation requirements.

## Enable repository settings

The reviewed allowlist is [.github/actions-policy.json](../.github/actions-policy.json).
It must include every external action, composite subaction, and reusable workflow
reference in the frozen graph, with its complete path and full SHA. Updating
dependencies requires reviewing and deploying the updated policy as well.
The current eight entries match this fork's regenerated lock closure. Another
repository needs its own reviewed list; the administration helper does not
derive it from the dependency graph. The
`actions/attest-build-provenance/predicate@36fa7d0…` entry retains the subaction
path from its [immutable parent manifest](https://github.com/actions/attest-build-provenance/blob/ef244123eb79f2f7a7e75d99086184180e6d0018/action.yml);
the lock's repository key alone loses that path.

To configure the allowlist in **Settings → Actions → General**:

1. Select **Allow qb20nh, and select non-qb20nh, actions and reusable workflows**
   (use your repository owner on another fork).
2. Clear the blanket options for actions created by GitHub and Marketplace
   verified creators.
3. Enable specified actions and reusable workflows. Paste the eight
   `patterns_allowed` values from the policy file as a comma-separated list,
   without JSON quotes or brackets.
4. Keep **Require actions to be pinned to a full-length commit SHA** checked
   and save.

The two checkout pins cover v5.0.1 in tests/dependency review and v6.0.2 in
release workflows. The two setup-go pins cover direct CI use of v6.4.0 and v5
inside the vendored release composite. The policy includes both direct and
nested references. Consolidating versions requires updating those callers,
the lock, and the deployed policy together, then validating the release build.

Use an authenticated `gh` installation with repository Administration write
permission and read access to contents and statuses. Reading Actions permission
settings requires Administration read; changing them requires Administration
write. The separate [Actions policy API](https://docs.github.com/en/rest/actions/policies)
requires Administration write even to read workflow-event policies. The current
connected GitHub app cannot inspect or apply these administration settings.
Their activation is owner-confirmed; independent API readback remains
unavailable. See the post-merge evidence above.
The workflow's ordinary `GITHUB_TOKEN` cannot administer the repository. Do not
store an administrator token in the workflow to perform this setup.

The fork now has an active default-branch ruleset, `protect` (ID `24848059`),
with no bypass. API readback confirms the four required checks listed below,
bound to GitHub Actions, with no exception for branch creation. **Require
branches to be up to date before merging** is enabled and confirmed by API
readback. Preserve its deletion, force-push, pull-request, and required-check
rules when making changes under **Settings → Rules → Rulesets**. The script
does not alter rulesets.

For a repository using classic branch protection, the prepared script supports:

```sh
script/apply-actions-protection --repo OWNER/REPO
script/apply-actions-protection --repo OWNER/REPO --apply
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

For this public personal fork, use **Settings → Actions → Policies** and create
an Active event policy targeting only `.github/workflows/lock-policy.yml`.
Enable **Restrict events** and allow `pull_request_target`, `push`,
`workflow_dispatch`, and `merge_group`, matching that workflow's declared
triggers. Leave actor restrictions unset. Save, reopen the policy, and check
its enforcement, target, and events; use Policy insights to inspect any denial.
[GitHub's instructions](https://docs.github.com/en/actions/how-tos/administer/control-workflow-execution)
cover all public repositories, including personal forks.

The owner saved [policy 7089](https://github.com/qb20nh/gh-actions-lock/settings/actions/rules/7089)
after these instructions. This is owner confirmation; the current connection
cannot read its stored policy fields. The setup leaves the native **Require
lockfile** checkbox unchecked because the public policy schema does not define
its behavior. Validate that option separately before enabling it; the required
independent verifier already rejects missing and malformed locks.

Require `Lock policy`, `go-test`, `integration-stub`, and `dependency-review`
in the existing ruleset, selecting GitHub Actions as their source (app ID
`15368`), and enable **Require branches to be up to date before merging**.
Keep every existing required context and review rule. `integration-live` remains
advisory. The classic-protection script adds only `Lock policy`; the verified
software checks must also be required there when using that protection model.

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

[GitHub merge queues](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue)
require an eligible organization-owned repository. They are unavailable for the
personal `qb20nh/gh-actions-lock` fork, so live merge-queue validation is not
applicable here. Keep the existing `merge_group` handlers. Before enabling a queue
on an eligible repository, make every required check handle `merge_group`,
including dependency review, and validate actual queue delivery.

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

Full-SHA pins depend on GitHub's Git object integrity. An ordinary force-push
moves a ref; it does not replace the content identified by an existing pin.
Replacing an already trusted object under its fixed ID requires a second
preimage, which differs from preparing a colliding pair before either object is
trusted. [GitHub uses collision-detecting SHA-1](https://github.blog/open-source/git/highlights-from-git-2-48/)
against known attacks. This mitigation does not restore SHA-1's general
collision resistance.

The policy downloads its pinned verifier source from GitHub without an
independent SHA-256 content digest. Remote action manifests and parity checks
also trust GitHub's object responses. The release smoke verifies a binary's
SHA-256 and provenance; it does not provide SHA-256 verification of every source
file used by the gate or executed actions. Additional content verification would
need a reviewed digest baseline and checks of the bytes actually executed.

[Git's 3.0 plan](https://git-scm.com/docs/BreakingChanges) changes the default
object format to SHA-256 for new repositories, retains SHA-1 support, and has no
scheduled release date. Upgrading Git alone does not convert this repository or
its Actions references; support must extend through the hosting and Actions
platforms.

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

The bundled hook is for this tool's source repository: it builds the verifier
from staged `./cmd/gh-actions-lock` source. An application repository needs an
adapted hook using a separately installed, reviewed verifier; see the
[usage guide](using-this-fork.md#5-maintain-locks-and-verify-locally).

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
- [Post-merge event and release validation](../test/runtime/evidence/2026-10-11-postmerge.md)
