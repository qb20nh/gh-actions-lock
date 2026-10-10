# gh-actions-lock

Lock your workflow dependencies.

> [!WARNING]
> **Technical Preview.** gh-actions-lock is pre-1.0 and under active development. The
> lockfile format, command flags, and behavior may change without notice between
> releases. Use it, file issues, and expect rough edges.

## Background

gh-actions-lock is part of GitHub's Workflow Dependency Pinning effort. It records verified commits for workflow dependencies in a lockfile for compatible runners and Actions services to enforce. Development is ongoing and behavior may still change.

Contributions are welcome. See [CONTRIBUTING.md](./CONTRIBUTING.md) to get started.

## Requirements

GitHub Enterprise Server (GHES) is not supported.

Requires the [`gh` CLI](https://cli.github.com/). Install it first, then install the extension:

```bash
gh extension install github/gh-actions-lock
```

## Usage

Scan workflows under `.github/workflows/`, resolve their action and reusable workflow dependencies, and update the lockfile:

```bash
gh actions-lock
```

After the initial run to onboard workflows, you will need to run `gh actions-lock` when:

- A new workflow is created that has `uses` dependencies.
- An existing workflow adds or removes `uses` dependencies.

A full-directory run (`gh actions-lock` with no path arguments) also prunes lockfile entries for workflows that have been deleted from `.github/workflows/`, dropping any dependencies left orphaned by the removal. Scoped runs that name specific workflows never prune out-of-scope entries.

Locked pins are sticky: a normal run only re-resolves refs the lockfile does
not record yet. It checks repository identity, commit existence, and whether
full-version tags (e.g. `v4.2.1`) still point at the recorded commit, including
transitive pins. A verified rename or transfer can update the repository name
while preserving its commit. Failed identity or commit checks block updates.

Rejected validation, resolution, and cooldown checks leave workflow, action,
and lockfile contents unchanged, including otherwise valid workflows in the
same run. Proposed YAML rewrites and lock coverage are validated before
committing the update. Unsupported edits to anchored or multiline `uses:`
values require manual changes.

If a final filesystem write fails, the CLI attempts to restore files it changed
and reports restoration failures. This is best-effort recovery; an update
spanning multiple files is not atomic across a process crash or power loss.

Overlapping updates to the same lockfile are rejected. If the CLI detects that
the lockfile or a source file changed since it was read, retry after the other
update has finished.

To check without changing workflows, action files, or the lockfile, run:

```bash
gh actions-lock --verify
```

`--verify`, `--verify-local`, and `--no-fix` remain read-only even when combined
with `--migrate-local-actions`. `--verify-local` checks source and recorded
dependency coverage offline; it does not check current upstream metadata.

Pins to branches or partial versions (e.g. `main`, `v4`) are not advanced when
upstream moves. To regenerate the lockfile from the current upstream commits,
run:

```bash
gh actions-lock --relock
```

`--relock` re-resolves refs that have legitimately moved and rewrites the
lockfile to the new SHA. Suspicious pins whose recorded commit is no longer
reachable upstream are left as errors — use `--accept-moved` to re-resolve
those as well.

For structured output, use `--json=valid,findings,pins`. Pin entries distinguish
proposed changes (`outcome: "planned"`, `applied: false`) from persisted changes
(`applied: true`). A rejected run does not report proposed pins as applied.

`--no-onboard` skips new workflows. Adding an unrecorded dependency to an
already tracked workflow produces an onboarding error and prevents writes;
run without the flag to add that dependency.

### Release cooldown

A positive `cooldown.default-days` in a Dependabot `github-actions` entry sets
the minimum release publication age. Otherwise, the CLI reads
`~/.config/gh-actions-lock/config.yml` (or `GH_ACTIONS_LOCK_CONFIG`):

```yaml
cooldown_days: 7
repos:
  actions/checkout:
    cooldown_days: 0
```

Per-repository values override the user configuration's default; zero disables
cooldown for that repository. Unsupported Dependabot cooldown settings produce
warnings.

Cooldown applies to newly adopted direct and transitive commits during initial
locking, `--relock`, and `--accept-moved`. Commits already recorded in the
lockfile remain usable. `--no-narrow` does not bypass cooldown, and cooldown
does not substitute an older commit for the requested one.

Eligibility uses `published_at` from an exact GitHub Release whose tag currently
resolves to the proposed SHA. Missing publication metadata, API failures, or a
branch or bare SHA without a discovered release tag block adoption. A tag known
to have moved from a recorded commit also blocks adoption because its old
publication date cannot establish the new commit's age. Current release
metadata proves publication age and today's tag target; it cannot prove that
the same tag-to-commit binding existed throughout the cooldown period.

### GitHub Enterprise Cloud with data residency

Authenticate `gh` to your tenant, then
run the extension from your tenant repository checkout:

```bash
gh auth login --hostname octocorp.ghe.com
# From the repository checkout:
gh actions-lock
```

With no conflicting environment overrides, the CLI infers the host from the
repository remote and uses the credentials stored by `gh` for that host. You do
not need to export a token or pass `--hostname` on every run. The account must
have read access to the tenant repositories used by your workflows.

### Self repository actions (`$/…`)

`uses: $/…` binds an action or reusable workflow to the **defining file's
repository and commit**. The CLI treats that source as inherently pinned, so it
does not need a separate lockfile entry; its remote dependencies still do:

```yaml
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: $/actions/my-action
  call:
    uses: $/.github/workflows/reusable.yml
```

A trailing `@ref` (e.g. `$/actions/my-action@v1`) is rejected because the defining
file supplies the commit.

A step's `uses: ./…` reads from the caller's workspace. Checkout `repository`,
`ref`, and `path` settings, generated files, or earlier file modifications can
make it refer to different contents from `$/…`. In a remotely called reusable
workflow, the defining repository can also differ from the caller's checkout.
An action file existing in your repository does not establish equivalence.

Migration is opt-in. After checking these semantics, explicitly rewrite eligible
local action references in selected workflows and their reached in-repo
definitions:

```bash
gh actions-lock --migrate-local-actions
```

Without this flag, `./…` action references remain unchanged.
`--no-migrate-local-actions` is also accepted. Local reusable workflow calls are
discovered without converting them to `$/…`.

[Recorded GitHub smoke tests](./test/runtime/evidence/2026-10-10.md) passed
same-repository and cross-repository `$/…` calls, including nested reusable
workflows, on the tested hosted runner/service deployment. Support depends on
the deployed service and runner configuration. The same tests found that a
syntactically malformed lock could be ignored at runtime; strict rejection
remains tracked in [#125](https://github.com/github/gh-actions-lock/issues/125).

A [tested workaround](./test/runtime/evidence/2026-10-10-workarounds.md) puts
trusted offline verification in a separate job with no actions, container, or
services, and makes consumers depend on its success. A malformed lock then
blocks those consumers. Validation inside a consumer's first step starts after
action preparation. Full-SHA references provide additional protection, but a
pinned parent can still declare mutable remote children.

## How it works

A repo gets a lockfile at [`.github/workflows/actions.lock`](https://github.com/github/gh-actions-lock/blob/main/.github/workflows/actions.lock), and workflows are onboarded individually.

Remote reusable workflows and action manifests are inspected at their exact
resolved or recorded commit. Discovery follows composite action dependencies,
nested reusable workflows, repository-relative reusable workflow calls, and
`$/…` references in the defining repository. Selected local workflows and their
repository-relative dependency closure are inspected from the checkout.

The lockfile records repository identity, commits, and dependency relationships.
Verification checks required pins and recorded child references, including
full-SHA uses. Missing or invalid manifests, unresolved children, and invalid
reusable workflow cycles prevent complete coverage from being reported.
Reachability checks help detect commits supplied from outside the declared
upstream history.

## Limitations

Workspace-relative `./…` action steps cannot establish complete lockfile
coverage. Workflows using them report incomplete coverage and are not newly
onboarded. Existing recorded pins are preserved, and other remote dependencies
are still checked. Generated or caller-modified actions need a dependency model
beyond static repository metadata.

## License

This project is licensed under the terms of the MIT open source license. See [LICENSE](./LICENSE) for the full terms.

## Maintainers

gh-actions-lock is maintained by @github/actions-dispatch-reviewers. See [CODEOWNERS](./CODEOWNERS).

## Support

Support is best-effort and community-based. Please file bugs and feature requests as [GitHub issues](https://github.com/github/gh-actions-lock/issues). See [SUPPORT.md](./SUPPORT.md) for details.
