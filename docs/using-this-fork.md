# Use this fork in another repository

`qb20nh/gh-actions-lock` is a personal fork of `github/gh-actions-lock`. Install
its CLI once, then run it from each repository checkout. Each repository gets
its own `.github/workflows/actions.lock`.

For CI, this fork provides a reusable validator and a trusted default-branch
policy. They reject missing, malformed, incomplete, or mutable dependency graphs
before dependent jobs run. This is the repository-controlled workaround for
[upstream #125](https://github.com/github/gh-actions-lock/issues/125), where the
Actions service can ignore malformed lock YAML. It does not fix the service.

The commands and workflow examples below pin tested revision
`b258e96524e81453878e071e8a5cef2d4b214aba`. The immutable verifier bootstrap is
separately pinned inside the validator workflows. See the
[validation evidence](../test/runtime/evidence/2026-10-11-postmerge.md).

## 1. Install the fork

For macOS, Linux, or WSL, install Git, the [GitHub CLI](https://cli.github.com/),
and Go 1.25.8 or newer. Authenticate `gh` with read access to the repositories
used by your workflows:

```sh
gh auth login --hostname github.com
gh repo clone qb20nh/gh-actions-lock
cd gh-actions-lock
git checkout b258e96524e81453878e071e8a5cef2d4b214aba
go build -o gh-actions-lock ./cmd/gh-actions-lock
gh extension install .
gh actions-lock --help
```

If another `actions-lock` extension is already installed, run
`gh extension remove gh-actions-lock` before the local installation. Keep the
clone in place: `gh extension install .` links the extension to that directory.
Rebuild there when adopting a newer reviewed revision. Native Windows needs
Git for Windows and a binary built with `-o gh-actions-lock.exe`.

As of 2026-10-11, this fork's binary releases are drafts. The source installation
above works without a published release. Installing `github/gh-actions-lock`
would install the upstream tool rather than this fork.

## 2. Lock the target repository

Start from a checkout with no unrelated pending workflow changes:

```sh
cd /path/to/your-repository
git switch -c chore/actions-lock
gh actions-lock --no-narrow
gh actions-lock --verify
git diff -- .github/workflows
```

Review and commit the generated lock and any workflow changes. Run the command
again after adding, changing, or deleting workflows or dependencies. A full scan
also removes lock entries for deleted workflows; a scan naming particular files
preserves entries outside that scope.

`--no-narrow` preserves existing reference expressions, including full SHAs.
It does **not** replace tags with SHAs. The default command can narrow a partial
version such as `v4` to an exact release tag, while the lock records the commit.

For the strict CI workaround, replace remote `uses:` tags and branches with
reviewed full commit SHAs. Preserve subaction and reusable-workflow paths. For
example:

```yaml
- uses: actions/checkout@93cb6efe18208431cddfb8368fd83d5badbf9bfd # v5.0.1
```

Then regenerate and audit the graph:

```sh
gh actions-lock --no-narrow
gh actions-lock --verify-frozen
```

Frozen verification follows remote manifests at their defining commits. A pinned
parent that calls `owner/child@main` still fails. Upgrade to a reviewed parent
whose children are pinned, or vendor and review the action before changing those
children. The CLI cannot rewrite a manifest in somebody else's repository.
Declared `docker://` action uses also need full `sha256:` image digests.

Local reusable workflow calls remain supported. For `./actions/...` steps,
check checkout paths, generated files, and workspace modifications before
opting into `gh actions-lock --migrate-local-actions --no-narrow`. That migration
uses `$/...`, which binds to the defining repository and commit rather than the
caller workspace. Runtime support depends on the deployed GitHub service and
runner. Generated or caller-modified actions remain unsupported as complete
static locks; see [local-action semantics](../README.md#self-repository-actions-).

## 3. Gate CI before its consumers run

For a workflow at `.github/workflows/ci.yml`, add the reusable validator below.
Replace `./script/test` with your project's test command. The workflow needs
`contents: read`; private dependencies outside the caller repository may require
a different reviewed authentication setup because its `GITHUB_TOKEN` is scoped
to the caller repository.

```yaml
name: CI

on: [pull_request, push]

permissions:
  contents: read

jobs:
  lock-validation:
    uses: qb20nh/gh-actions-lock/.github/workflows/lock-validation.yml@b258e96524e81453878e071e8a5cef2d4b214aba
    with:
      source-sha: ${{ github.workflow_sha }}
      workflow-path: .github/workflows/ci.yml

  test:
    needs: lock-validation
    if: needs.lock-validation.result == 'success'
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@93cb6efe18208431cddfb8368fd83d5badbf9bfd # v5.0.1
      - run: ./script/test
```

Set `workflow-path` to the caller's actual filename. `source-sha` identifies that
workflow's exact revision. The validator fetches it as data and scans **all**
workflows in that revision, not only the named file. It builds the fixed verifier
from this fork, so the target repository does not need Go source.

Add successful-validation dependencies to every job that runs actions,
application code, containers, deployments, or release operations. Preserve
existing `needs` entries. If a job already has an `if` condition, combine it with
`needs.lock-validation.result == 'success'`; an unconditional `always()` must
not let a consumer run after failed validation. Put validation in a separate
job: a consumer's first step runs after its actions have already been prepared.

After adding the validator reference, regenerate the lock with
`gh actions-lock --no-narrow`, run `gh actions-lock --verify-frozen`, and commit
both workflow and lock. This makes the fork's reusable workflow a recorded
dependency too.

This gate blocks its dependent jobs. Protecting the gate against PR changes
also requires the default-branch policy in the next section.

## 4. Deploy the protected policy workaround

Repository settings do not carry over from this fork. Set them up independently
on each target repository. The workflow examples here target `github.com` and
Ubuntu hosted runners; the CLI's tenant-host support does not make these
hardcoded GitHub.com workflows portable to a tenant unchanged. GHES is unsupported.

1. Copy [lock-policy.yml](https://github.com/qb20nh/gh-actions-lock/blob/b258e96524e81453878e071e8a5cef2d4b214aba/.github/workflows/lock-policy.yml)
   into the target's `.github/workflows/lock-policy.yml`. Change its push branch
   from `main` if the target uses a different default, and remove the fork's
   `codex/m6/combo-*` development branch pattern.
2. Keep `LOCK_VERIFIER_REPOSITORY: qb20nh/gh-actions-lock` and the reviewed
   `LOCK_VERIFIER_SHA` intact. They identify the tool source, not the application.
   Preserve `contents: read`, `pull-requests: read`, and `statuses: write`.
   The policy builds trusted tool source and inspects PR files as data; do not
   change it to execute PR code in its privileged `pull_request_target` job.
3. Regenerate the target lock, then deploy the policy to its **default branch**.
   A workflow installed only on a feature branch does not activate the
   default-branch event. Complete the event-policy setup below before testing
   PR delivery or making the status required.
4. Under **Settings → Actions**, enable the full-length commit SHA requirement
   and select an exact allowlist for the target's reviewed dependency graph.
   Clear blanket GitHub-owned and verified-creator exemptions. Include full
   subaction and reusable-workflow paths, including this fork's validator when
   it is external to the target owner. Review nested remote manifests too;
   repository-only lock keys and dependency JSON cannot recover every subpath.
5. Under **Settings → Actions → Policies**, configure an Active event policy
   targeting only `.github/workflows/lock-policy.yml`, allowing its declared
   `pull_request_target`, `push`, `workflow_dispatch`, and `merge_group` events.
   Keep actor restrictions unset for this setup. Reopen the saved policy and
   inspect Policy insights. The native **Require lockfile** option is not needed
   for this workaround; its independent verifier enforces a required lock.
   Leave that option unchecked for this setup and validate its native behavior
   separately before enabling it.
   Confirm a successful default-branch `Lock policy` run, then open a normal PR
   and confirm successful statuses on both its exact head and merge revisions.
6. In the target's branch ruleset, require PRs, prevent deletion and force
   pushes, and require `Lock policy` from GitHub Actions (app ID `15368`) plus
   the target's own software checks. Require branches to be up to date, leave
   the branch-creation exception unchecked, and avoid bypass actors. Do not copy
   this tool's `go-test` or integration check names into an unrelated project.
7. Assign workflow, lock, policy, and verifier changes to the target's actual
   maintainers in CODEOWNERS. Independent owner approval provides a stronger
   barrier; requiring the sole author's own approval would strand their PRs.
   See [validator trust limits](actions-protection.md#protect-the-validator-definition).

For a controlled negative check, use a disposable PR branch to make the lock
malformed, then confirm both policy statuses fail and consumers are skipped.
Restore the valid lock before merging. This validates your repository gate;
native platform rejection remains a separate unresolved #125 criterion.

Personal repositories cannot use GitHub merge queues. On an eligible organization
repository, make every required check handle `merge_group` and validate actual
queue delivery before enabling it. Enable Dependency graph before requiring a
dependency-review check.

### Optional administration helper

For a public GitHub.com repository using **classic branch protection**, copy
`script/apply-actions-protection` into the target's `script/` and create its own
`.github/actions-policy.json`. This example matches the CI workflow above; extend
the sorted, unique list for every other reviewed dependency:

```json
{
  "version": 1,
  "required_check": {"context": "Lock policy", "app_id": 15368},
  "patterns_allowed": [
    "actions/checkout@93cb6efe18208431cddfb8368fd83d5badbf9bfd",
    "qb20nh/gh-actions-lock/.github/workflows/lock-validation.yml@b258e96524e81453878e071e8a5cef2d4b214aba"
  ]
}
```

Commit the matching JSON to the target's default branch alongside the policy
workflow, then obtain a successful `Lock policy` status on that revision. The
helper requires the deployed JSON to match its local copy before applying settings.

The helper reads policy relative to its own location. Running the fork's copy
with `--repo OWNER/REPO` would still read this fork's eight-entry policy. Use the
target's copy and a separate administrator session after successful policy
deployment:

```sh
python3 script/apply-actions-protection --repo OWNER/REPO
# Review the printed plan before applying it:
python3 script/apply-actions-protection --repo OWNER/REPO --apply
```

The helper replaces the selected external allowlist and adds the required
`Lock policy` check. It does not derive the graph, edit rulesets, configure event
policies, or add the project's software checks. Preserve existing protections
and review the readback. See [administration details](actions-protection.md).

## 5. Maintain locks and verify locally

| Task | Command |
| --- | --- |
| Record new dependencies while preserving reference expressions | `gh actions-lock --no-narrow` |
| Check recorded coverage and live repository/commit parity | `gh actions-lock --verify` |
| Check recorded coverage offline | `gh actions-lock --verify-local --require-lockfile` |
| Require the full immutable dependency graph | `gh actions-lock --verify-frozen` |
| Adopt reviewed updates for legitimately moved refs | `gh actions-lock --relock --no-narrow` |
| Produce findings for automation | `gh actions-lock --verify-frozen --json=valid,findings,pins` |

For full-SHA workflow refs, choose and review the new SHA in the workflow before
regenerating its lock. `--relock` cannot infer which new commit you intend from
an old immutable SHA. Review workflow, lock, and allowlist changes together and
update the deployed allowlist when needed. Do not automatically accept suspicious
unreachable or moved pins with `--accept-moved`.

The bundled [.githooks/pre-commit](../.githooks/pre-commit) validates the staged
tree and builds `./cmd/gh-actions-lock` from that tree. It works in this tool's
source repository; it cannot be copied unchanged into an application repository.
An adapted hook needs the [raw-tree materializer](../script/materialize-git-tree)
and a separately installed, reviewed verifier binary in place of that build.
Retain the staged-tree and JSON full-SHA checks. Running the offline command
above manually checks the working tree, not partially staged commit contents.

## Troubleshooting and remaining limits

- **A tag or remote child fails frozen verification:** pin the direct reference,
  upgrade its parent, or review and vendor the child closure. Adding a lock entry
  does not make a mutable `uses:` expression immutable.
- **Cooldown rejects the fork's unpublished workflow commit:** a positive
  cooldown needs an exact published release whose tag resolves to the adopted
  commit. This fork's draft commits do not qualify. When cooldown comes from
  user config, a reviewed `repos` entry for `qb20nh/gh-actions-lock` with
  `cooldown_days: 0` can permit adoption. A positive Dependabot
  `cooldown.default-days` takes
  precedence over that entire config, including exceptions; Dependabot
  include/exclude filters are unsupported. Keep that policy and use a qualifying
  published revision, or explicitly review changing the authoritative policy.
  Bare SHAs without a discovered release tag can also fail age checks; see
  [cooldown configuration](../README.md#release-cooldown).
- **Private dependency inspection fails:** authenticate with actual read access
  to every dependency. Frozen verification fails when inspection is inconclusive.
- **`Lock policy` does not appear:** confirm the workflow is on the default
  branch, its event policy permits the trigger, Actions is enabled, and the run
  can publish statuses. Inspect the workflow run and Policy insights.
- **The policy cannot obtain a merge revision:** resolve merge conflicts or
  retry once GitHub has calculated mergeability. It requires both current head
  and merge revisions for PR validation.
- **A native malformed lock still executes:** #125 remains external. Require
  the trusted policy and successful consumer gates rather than assuming native
  startup validation has been repaired.

These checks cover action and reusable-workflow graphs and declared `docker://`
actions. Job/service containers, Dockerfile downloads, shell downloads, installed
tools, and generated workspace actions need separate controls. SHA pins still
depend on GitHub's object integrity, and the verifier source has no independent
SHA-256 baseline. Release-binary digest/provenance checks do not cover every
source file. The [protection guide](actions-protection.md) records those trust
boundaries and hosted positive/negative evidence.
