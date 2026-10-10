# Post-merge M6 validation

Recorded on 2026-10-11 KST. PR #2 merged into the fork's default branch at
`2169abe6e476bdf58aa32105efcc2a2792fc92b3`. This delivery is for personal fork
use; no upstream contribution is planned.

## Merged-source checks

[Main tests](https://github.com/qb20nh/gh-actions-lock/actions/runs/38072869871)
passed every job: formatting, vet, the full Go race and release suite,
five materializer tests, 14 protection/hook tests, all 35 stub scenarios,
and all 103 live scenarios. [Lock policy](https://github.com/qb20nh/gh-actions-lock/actions/runs/38072869694)
posted success on the exact merged commit from `github-actions[bot]`.

[Evidence/protection PR #4](https://github.com/qb20nh/gh-actions-lock/pull/4)
subsequently merged at `b258e96524e81453878e071e8a5cef2d4b214aba`.
Its [main tests](https://github.com/qb20nh/gh-actions-lock/actions/runs/38075214001)
passed every job, and [main Lock policy](https://github.com/qb20nh/gh-actions-lock/actions/runs/38075213613)
passed on that exact revision. Ruleset API readback still confirms all four
required checks, strict freshness, no bypass, and no creation exception.

## Actual PR-target delivery

[Disposable PR #3](https://github.com/qb20nh/gh-actions-lock/pull/3) exercised
the deployed `pull_request_target` event against `main`, then restored its valid
lock and closed without merging. Unlike the earlier isolated push harness,
these runs used actual GitHub PR event delivery.

Every policy run used workflow revision
`2169abe6e476bdf58aa32105efcc2a2792fc92b3` and immutable trusted verifier
`e69dc3bf0f7f9a9d4287415b11cf4dfcf17aacbb`. Logs identify both sources.
All head and merge policy statuses were published by `github-actions[bot]`.

| Control | Exact head | Exact merge | Result |
| --- | --- | --- | --- |
| Valid lock | `e01d5e81f1461f5cf59337586492ef23854909d3` | `d3c461e94ac5e9cf024f81148c3bb5de045310e3` | [Policy 38073289838](https://github.com/qb20nh/gh-actions-lock/actions/runs/38073289838) succeeded on both revisions, with both `LOCK_POLICY_PASSED` log markers. |
| Malformed YAML (`version: [` plus newline) | `e28f731143703d86f8289b7981aa2bfbfba95b90` | `b94f101e73cd8ff3b66061960428e79720cf57b4` | [Policy 38073405914](https://github.com/qb20nh/gh-actions-lock/actions/runs/38073405914) failed both statuses: `line 1: did not find expected node content`; no success markers. |
| Valid lock restored | `cbf1978b328fc357804445462e1644b51f88a492` | `a899630b1d8c39e3f7c6a93494c5c9c4dd158c3e` | [Policy 38073503116](https://github.com/qb20nh/gh-actions-lock/actions/runs/38073503116) succeeded on both. The restored tree is identical to the initial valid tree. |

The valid control's [PR tests](https://github.com/qb20nh/gh-actions-lock/actions/runs/38073289922)
passed all 35 stub and 103 live scenarios; [dependency review](https://github.com/qb20nh/gh-actions-lock/actions/runs/38073289945)
also passed. On malformed input, [PR tests](https://github.com/qb20nh/gh-actions-lock/actions/runs/38073407384)
failed the validator and skipped Go, stub, and live consumers;
[dependency review](https://github.com/qb20nh/gh-actions-lock/actions/runs/38073407381)
skipped its consumer. Main was not changed by the fixture.

## Fresh draft release

[Build 38073256994](https://github.com/qb20nh/gh-actions-lock/actions/runs/38073256994)
passed its lock gate, formatting, vet, full race/release suite, all 35 stub
scenarios, five materializer tests, and 14 protection/hook tests. It produced
[draft prerelease 409109569](https://github.com/qb20nh/gh-actions-lock/releases/tag/untagged-88bcef74c4ff66093ea3)
with 12 attested platform binaries from the exact merged source
`2169abe6e476bdf58aa32105efcc2a2792fc92b3`.

Annotated tag `m6-2169abe6e476bdf58aa32105efcc2a2792fc92b3`, tag object
`9aa942aba2a3a3764d473feb9f64fd0e19a1de03`, resolves to that commit.
All 12 release API digests match their attestation subjects, whose provenance
identifies the merged commit. [Asset evidence](2026-10-11-release-assets.json)
records every digest and attestation ID.

[Uploaded-binary smoke 38073725827](https://github.com/qb20nh/gh-actions-lock/actions/runs/38073725827)
passed at `9328e011659b7edc087cc846156430f98c40dc20`. It downloaded the draft's
Linux amd64 binary and checked its SHA-256 against both the release API and
`8fe885929ebfef92639f6b9da37378073291bf402808e2f6f9ec000e7893bc93`.
`gh attestation verify` required the hosted `m6-validation.yml` signer and exact
source commit. The uploaded binary executed, verified the frozen workflow graph,
and rejected a missing lock. Its reported version was
`v0.1.7-rc.1.0.20261010174301-2169abe6e476`.

The smoke workflow preserves current draft notes before appending its proof;
it no longer replaces them with evidence from an earlier candidate. The release
remains a draft prerelease.

## Protection activation

The user created active [ruleset `protect`, ID 24848059](https://github.com/qb20nh/gh-actions-lock/rules/24848059)
for `~DEFAULT_BRANCH`. Public API inspection confirms `main` is protected,
with no bypass, deletion and force-push prevention, and required PRs with zero
approvals. After the user saved required checks, API readback of the ruleset
updated at 2026-10-11 03:09:09 KST confirms `Lock policy`, `go-test`,
`integration-stub`, and `dependency-review`, each bound to GitHub Actions
(integration ID `15368`). `do_not_enforce_on_create` is `false`, so branch
creation does not bypass these checks.

After the user enabled **Require branches to be up to date before merging**,
API readback confirms `strict_required_status_checks_policy` is `true`, with
all four required checks and the other rules retained. All four check names
and their source are verified by hosted controls;
`integration-live` remains advisory. The prepared classic branch-protection
script adds only `Lock policy` and does not modify rulesets.

The user confirms that **Require actions to be pinned to a full-length commit
SHA** is checked and the exact eight-entry allowlist was saved. These settings
are owner-confirmed; Administration API readback is unavailable. The current
connection cannot inspect or change Actions permission or workflow-event
administration settings.

The user then saved [Actions event policy 7089](https://github.com/qb20nh/gh-actions-lock/settings/actions/rules/7089)
after the setup instructions for an Active policy targeting only
`.github/workflows/lock-policy.yml`, with allowed events `pull_request_target`,
`push`, `workflow_dispatch`, and `merge_group`. This policy is owner-confirmed;
the connector rejected its API endpoint as unsupported, so its stored fields
have not been independently inspected. The setup leaves the undocumented
native **Require lockfile** option unchecked until its behavior is validated;
the independent required verifier already rejects missing and malformed locks.
See [activation instructions](../../../docs/actions-protection.md).

Live merge queues are not applicable to this personal fork:
[GitHub requires an eligible organization repository](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue).
The existing handler remains implemented. On an eligible repository, every
required check must support `merge_group` before queue activation; dependency
review currently handles only PRs.

## Remaining goal blockers

- The four required checks and strict branch freshness are confirmed by
  ruleset API readback. Full-SHA policy, the exact allowlist, and workflow-event
  policy are owner-confirmed but cannot yet be independently read back through
  the current connection. This is an evidence limitation, not a failed setup.
- Upstream [#125](https://github.com/github/gh-actions-lock/issues/125) remains
  open. The repository-controlled gate rejects malformed input for its covered
  consumers; the platform's own malformed-YAML rejection remains unproven.
  Re-run the original service negative control after an upstream fix, requiring
  a lock-specific startup error and no execution sentinel.

These results do not publish a stable release or merge an upstream PR.
