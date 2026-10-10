# Runner and service smoke fixtures

These actions test deployed GitHub Actions behavior. CLI unit and stub tests do
not establish that behavior. They use Bash, write only step outputs, and have no
remote dependencies.

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
| Syntactically malformed lock, then a valid-YAML lock with a dangling pin | Each run has a lock-specific rejection before any fixture execution marker. A failed run alone is insufficient evidence. |

Commit malformed locks at `.github/workflows/actions.lock` in separate test
revisions. Editing a checkout after the job starts does not test service-side
lock parsing. Compare each negative case with the same workflow and a valid
lock; collect run/job results, annotations, and logs through an independent
controller. A marker in a malformed-lock run establishes that execution was
allowed and must leave the runtime enforcement gate open.

Support requirements are explicit:

- Use runner `v2.338.0` or newer as the smoke baseline. It includes the
  [composite manifest parser fix](https://github.com/actions/runner/pull/4669).
  Hosted runners roll out progressively; record the actual version from setup
  logs, along with runner image, workflow/provider commits, run URL, and time.
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
- Classify unavailable service syntax, missing feature support, or an older
  deployed runner as **blocked/unsupported**, separately from passed runtime
  assertions. Preserve the failing evidence. Do not mark the release gate
  complete until positive source-binding cases and explicit malformed-lock
  rejection are observed on the target deployment.

The outstanding runtime behaviors are tracked in
[#126](https://github.com/github/gh-actions-lock/issues/126) and
[#125](https://github.com/github/gh-actions-lock/issues/125).
