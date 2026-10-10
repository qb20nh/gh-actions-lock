# M6: Workarounds for malformed locks ignored by GitHub Actions

We can stop our dependent jobs when a lock is malformed, even while GitHub's
service ignores it. A separate validator job provides that boundary. Full-SHA
references provide additional protection, but pinning only the parent does not
freeze its remote children.

## Hosted evidence

The [probe workflow](../workflows/m6-workarounds.yml) ran on
isolated fork branches. The existing draft release and default branches were
unchanged. The observed runner was `2.337.0`; probes started on 2026-10-10
at 23:16 KST. GitHub logs use UTC.

- [Valid-lock control](https://github.com/qb20nh/gh-actions-lock/actions/runs/38058951167): commit [2e913a0](https://github.com/qb20nh/gh-actions-lock/commit/2e913a0d85861f155e636f7eba78be8e7e2d6bc0), all jobs pass.
- [Malformed-lock control](https://github.com/qb20nh/gh-actions-lock/actions/runs/38058973704): commit [bb736a1](https://github.com/qb20nh/gh-actions-lock/commit/bb736a1bca55ac0bec5517f6e35981f23a6de058), expected overall failure from the validator. Its result-confirmation job passes.
- The malformed lock was committed before dispatch: `version: [malformed`.
- Both controls use parent action [93652f3](https://github.com/qb20nh/transition-test/commit/93652f3e19efae7f268e56fc64dc774af069a02e). Its child uses the fixture branch, whose lock target is [38a3e6d](https://github.com/qb20nh/transition-test/commit/38a3e6df964dfe1756abb8dec154228d7140a841)/`v1` and live target is [7e72f65](https://github.com/qb20nh/transition-test/commit/7e72f651cfb3ae0aba09f652b96b1efda2454d5b)/`v2`.

| Claim | Valid lock | Malformed lock | Evidence |
| --- | --- | --- | --- |
| Separate action-free validator controls its consumer | Validator succeeds; consumer runs its sentinel. | Validator reports unreadable lock, exits 2; consumer is skipped with no steps. | [Valid gate](https://github.com/qb20nh/gh-actions-lock/actions/runs/38058951167/job/114233081311), [negative gate](https://github.com/qb20nh/gh-actions-lock/actions/runs/38058973704/job/114233147367), [skipped consumer](https://github.com/qb20nh/gh-actions-lock/actions/runs/38058973704/job/114233242893). |
| Direct SHA plus same-commit self references stays fixed | Wrapper and leaf emit `v1`. | Wrapper and leaf still emit `v1` from `38a3e6d`. | [Negative direct-SHA job](https://github.com/qb20nh/gh-actions-lock/actions/runs/38058973704/job/114233147649). |
| Parent SHA does not freeze a mutable remote child | Parent `93652f3`, child download `38a3e6d`, marker `v1`. | Same parent `93652f3`, child download `7e72f65`, marker `v2`. | [Valid parent job](https://github.com/qb20nh/gh-actions-lock/actions/runs/38058951167/job/114233081171), [negative parent job](https://github.com/qb20nh/gh-actions-lock/actions/runs/38058973704/job/114233147572). |

The negative gate's exact error was:

```text
opening lockfile: lockfile is unreadable: .github/workflows/actions.lock: line 1: did not find expected ',' or ']'
```

The workflow deliberately has ungated action jobs to compare behavior. They are
test controls; copying those jobs into production would preserve the bypass.
The hosted proof covers these push workflows, not every event or reusable
workflow arrangement.

## Recommended implementation

Use a shell-only validator job before each action-consuming workflow's other
jobs. Build the verifier from immutable trusted source or download an approved
binary with a fixed digest and verified attestation. Our probe built source
`34c65ecf262f580f066bf1d105a2ecc310226374`, separately from candidate files.
It used read-only credentials and executed no candidate scripts or builds.

Fetch the workflow's exact source commit into a fresh directory, create the
`.git` root marker needed for local traversal, require a regular nonempty
`actions.lock`, reject symlinked parent directories and lock files, and verify
the intended workflow inventory. Our restricted push probe checks
`github.workflow_sha == github.sha`; other events need explicit source rules,
especially `pull_request_target`, whose definition comes from the base branch.

Run the trusted CLI with `--verify-local --hostname github.com` and require
successful exit and `valid: true`. Every consumer must depend on the validator
and require its success. The gate itself must have no actions, container,
services, or candidate-controlled build. Protect its definition against removal
or changed conditions. A validation step placed inside a consumer job starts
too late to provide this boundary.

Offline verification checks syntax, coverage of inspected local definitions,
and consistency of the recorded dependency graph. It does not fetch remote
manifests to reconstruct omitted edges, authenticate repository IDs, or prove
that the service used the lock. Online discovery/verification and source review
remain needed for remote completeness. Docker images and shell downloads also
need separate pinning.

## Other controls and their limits

| Option | Value | Limitation / status |
| --- | --- | --- |
| Trusted required validation check, plus pre-commit validation | Stops malformed locks from merging; catches conflict markers such as those in the original #125 report. | Does not stop another concurrent PR job. Validate the protected merge/head SHA, protect the validator, and support `merge_group` when using a merge queue. Proposed configuration, not enabled. |
| Full-SHA actions and reusable workflow references | Keeps their direct source fixed when the lock is ignored. | Hosted direct-action proof passes; mutable children still drift. Existing `--no-narrow` preserves explicit full SHAs but does not convert tags to SHAs. |
| Repository full-length action SHA policy | Independent service policy for actions, including owned and GitHub actions. | GitHub documents that reusable workflows may still use tags. Local actions remain allowed. Policy behavior was researched, not enabled or tested here. |
| Exact-SHA action/reusable-workflow allowlists | Can restrict permitted external versions without relying on the lock. | Owned components remain allowed in the owner-plus-selected-external mode; broad wildcards weaken the restriction. Proposed configuration. |
| Audit or vendor the full recursive graph | Can remove dependence on lock enforcement for action/workflow source selection. | Must freeze every remote child, retain same-commit local references, and preserve semantics. Maintenance burden; image tags and shell-installed code remain separate. |
| External trusted GitHub App validator | Can attach independently produced checks to candidate commits and prevent PR edits from replacing the checker. | Needs deployment, permissions, and protected-branch configuration. Does not stop unrelated workflows from executing. |
| Custom self-hosted runner check before action preparation | A runner fork can abort initialization on invalid/missing source lock. | Applies only to those runners; requires reliable source-SHA selection, credentials, and runner maintenance. Earlier service-side reusable workflow resolution remains outside its control. |

The release workflow has a concrete transitive pinning concern:
`cli/gh-extension-precompile@v2.1.0` declares `actions/setup-go@v5` and
`actions/attest-build-provenance@v1`. Freezing only its direct SHA leaves those
child references mutable if lock enforcement is absent. A future freeze tool
should audit the complete graph and reject unsupported mutable children rather
than describe a direct-only rewrite as full protection.

GitHub treats conditionally skipped required jobs as successful. Require the
validator itself, or an unconditional result-check job that fails unless the
validator actually succeeded. Selecting the expected GitHub App source helps,
but does not distinguish two attacker-modifiable workflows using the same app.
Protect workflow changes and reviewer rules as well. Organization required
workflow rulesets offer a centrally managed option; they are not an equivalent
repository feature for this personal fork.

## Runtime boundary

The public runner receives service-selected lock dependencies and forwards
them to the action resolver. It does not parse the raw lockfile in that path.
The public Go parser already reports malformed YAML as an error. The service's
ignored-error branch is not public, so its exact location remains an inference
from these paths and the reproduced behavior.

Runner preparation downloads actions before ordinary user steps. JavaScript
pre-hooks and job containers/services are further reasons not to use a
first-step check as an execution barrier. Even a self-hosted
`ACTIONS_RUNNER_HOOK_JOB_STARTED` runs after action preparation. GitHub documents
that a nonzero hook stops the job, but source handling of subsequent `always()`
pre-hooks warrants a dedicated test before claiming complete prevention. No
self-hosted hook or runner-fork behavior was tested here.

The separate-job workaround proves that the gated consumer did not execute;
it does not prove no service-side reusable workflow resolution occurred.

Regenerating the lock after checkout only changes workspace bytes; the service
already selected this run's dependency payload. Detecting missing lock log
messages and cancelling the run is also a race, not an execution barrier.
These can aid recovery or monitoring, but do not replace pre-execution gating.

## Trackable follow-up

- [x] Validate separate-job rejection with committed malformed YAML and a valid control.
- [x] Test direct-SHA fallback and demonstrate its mutable-child limitation.
- [ ] Integrate a trusted validator into normal CI and release workflows, gating every consumer. Add malformed, missing-lock, and valid controls; require the validator check on protected branches.
- [ ] Define source selection and result binding for PR, merge-group, tag, and reusable-workflow events; inspect candidate files as data.
- [ ] Document SHA preservation under `--no-narrow`; assess direct freeze/export tooling that audits the full transitive graph.
- [ ] Decide and test repository SHA/allowlist policy, with reusable-workflow and transitive cases.
- [ ] After upstream #125 changes, rerun the original ungated malformed-lock control and require service rejection before any sentinel runs.

The mitigation track is actionable locally. M6's original platform-enforcement
gate remains open until the upstream behavior changes; these controls do not
silently redefine that acceptance criterion.

## Sources

- [Original #125 report](https://github.com/github/gh-actions-lock/issues/125), whose linked lock contains unresolved merge markers.
- [Runner job dependencies payload](https://github.com/actions/runner/blob/67f01c276e0a91d967ba499ce4cdc9a95efa0262/src/Sdk/DTPipelines/Pipelines/AgentJobRequestMessage.cs#L284), [resolver forwarding](https://github.com/actions/runner/blob/67f01c276e0a91d967ba499ce4cdc9a95efa0262/src/Runner.Worker/ActionManager.cs#L1052), and [action preparation / hook order](https://github.com/actions/runner/blob/67f01c276e0a91d967ba499ce4cdc9a95efa0262/src/Runner.Worker/JobExtension.cs#L297).
- [Public parser error handling](https://github.com/github/actions-lockfile/blob/ac158834765237ee94c36606cfc7d87316aa49ab/go/pkg/lockfile/compat.go#L46).
- [GitHub action SHA policy and allowlists](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/enabling-features-for-your-repository/managing-github-actions-settings-for-a-repository).
- [Required-check behavior](https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/troubleshooting-required-status-checks), [required workflow rulesets](https://docs.github.com/en/enterprise-cloud@latest/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets), and [organization ruleset availability](https://docs.github.com/en/organizations/managing-organization-settings/creating-rulesets-for-repositories-in-your-organization).
- [Safe use of pull_request_target](https://docs.github.com/en/actions/reference/security/securely-using-pull_request_target) and [self-hosted hook documentation](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/run-scripts#handling-exit-codes).
