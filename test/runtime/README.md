# Runner and service smoke fixtures

These actions test deployed GitHub Actions behavior. CLI unit and stub tests do
not establish that behavior. They use Bash, write only step outputs, and have no
remote dependencies.

On 2026-10-10, the positive source-binding and locked-download probes passed on
hosted runner `2.337.0`, Ubuntu 24.04 image `20261004.327.1`. A syntactically
malformed lock was ignored and its execution sentinel ran, so the runtime
rejection gate remains open under
[#125](https://github.com/github/gh-actions-lock/issues/125). See the
[recorded evidence](evidence/2026-10-10.md) for commits, jobs, logs, and annotations.

The wrapper calls `$/test/runtime/actions/leaf`. The leaf reports the marker
embedded in its definition and its `github.action_path`. The wrapper requires
the leaf to live beside its own source directory, distinguishing the defining
repository/ref from the caller checkout. Copy both actions to each authorized
fixture repository and pin cross-repository calls to recorded full commits.

Reusable workflows must live directly under `.github/workflows/`; the workflow
controller owns those files. A foreign reusable workflow should call this
wrapper with `$/…`, then call another provider workflow locally. Keep the caller
checkout different from the provider, or omit checkout, to make source binding
observable. Record output paths and the runner's action-download SHAs.

| Case | Required evidence |
| --- | --- |
| Same-repository wrapper → `$/` leaf, without checkout | Expected marker, `RUNTIME_SELF_REFERENCE_OK`, and sibling source paths. |
| Cross-repository wrapper → `$/` leaf | The same assertions, with both paths belonging to the pinned provider archive. |
| Foreign reusable workflow → provider wrapper, plus nested provider workflow | Each nested call succeeds at its defining repository/ref while the caller checkout differs. |
| Valid lock with a remote action pin | Setup reports `Using locked action versions from the workflow's lockfile`; download SHA and action marker match the recorded pin. Use a nonempty dependency set. |
| Syntactically malformed lock | Lock-specific rejection before any fixture execution marker. A failed run alone is insufficient evidence. |
| Valid-YAML lock with a dangling pin | The same explicit rejection and absence of fixture execution. |

Commit malformed locks at `.github/workflows/actions.lock` in separate test
revisions. Editing a checkout after the job starts does not test service-side
lock parsing. Compare each negative case with the same workflow and a valid
lock; collect run/job results, annotations, and logs through an independent
controller. A marker in a malformed-lock run establishes that execution was
allowed and must leave the runtime enforcement gate open.

Observed results:

- [Positive runtime run](https://github.com/qb20nh/gh-actions-lock/actions/runs/38056833390):
  all five jobs passed, including same-repository actions, foreign actions with
  a conflicting caller checkout, and foreign reusable workflows with nested
  local workflow calls. The
  [locked symbolic-ref job](https://github.com/qb20nh/gh-actions-lock/actions/runs/38056833390/job/114226935739)
  announced the lock and downloaded provider commit `38a3e6d` with the v1 marker,
  while the live branch pointed at `7e72f65` with the v2 marker.
- [Valid-lock control](https://github.com/qb20nh/gh-actions-lock/actions/runs/38056898057):
  passed, announced the lock, and executed `M6_LOCK_SENTINEL_EXECUTED` as expected.
- [Malformed-syntax negative](https://github.com/qb20nh/gh-actions-lock/actions/runs/38056912627):
  GitHub reported success; no lock announcement appeared and
  `M6_LOCK_SENTINEL_EXECUTED` ran. This reproduces #125.
- [Dangling-pin negative](https://github.com/qb20nh/gh-actions-lock/actions/runs/38056929771):
  startup failure with no jobs. GitHub annotated
  `Invalid lockfile: .github/workflows/actions.lock#L1` and reported
  “The lockfile could not be validated. Regenerate it by running gh actions-lock.”
  This supplies explicit rejection evidence for that case.

Support requirements are explicit:

- Runner `2.337.0` passed the recorded configuration. The latest release,
  `v2.338.0`, includes an additional
  [composite manifest parser fix](https://github.com/actions/runner/pull/4669).
  These results do not establish a universal minimum version or support for
  every deployment. Hosted runners roll out progressively; record the actual
  version from setup logs, runner image, workflow/provider commits, run URL,
  and time.
- The deployed service must provide the runner variable
  `actions_self_repository`, as used by
  [v2.338.0 source](https://github.com/actions/runner/blob/v2.338.0/src/Runner.Common/Constants.cs).
  Setting a similarly named shell environment variable does not enable it.
  Workflow and reusable-workflow parsing must also accept `$/…`.
- Lock enforcement needs an `ActionsDependencies` job payload. The
  [setup announcement](https://github.com/actions/runner/blob/v2.338.0/src/Runner.Worker/JobExtension.cs)
  is evidence that a nonempty payload reached that job; runner version alone
  does not establish enforcement. A self-only workflow may have no payload.
  The runner does not parse `actions.lock` itself: it
  [forwards dependencies to Launch](https://github.com/actions/runner/blob/v2.338.0/src/Runner.Worker/ActionManager.cs#L1052)
  on the RunServiceJob path. Require an observable pinned download when the
  symbolic ref points at a different commit; the announcement alone is weaker.
- Classify unavailable service syntax or missing feature support as
  **blocked/unsupported**, separately from passed runtime assertions. Preserve
  the failing evidence. Do not mark the release gate
  complete until positive source-binding cases and explicit malformed-lock
  rejection are observed on the target deployment.

The source-binding behavior tracked in
[#126](https://github.com/github/gh-actions-lock/issues/126) passed on the tested
deployment. Malformed-syntax rejection under
[#125](https://github.com/github/gh-actions-lock/issues/125) remains an open
release gate; rerun it after a service fix and require both a lock-specific
error and absence of the execution sentinel.
