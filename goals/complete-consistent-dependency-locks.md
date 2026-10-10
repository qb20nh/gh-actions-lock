# Goal: Produce complete, consistent dependency locks without unintended changes

Deliver reviewable fixes so verification reports dependency coverage accurately, successful updates preserve workflow semantics, and rejected validation, resolution, or policy checks leave workflow and lock files unchanged.

- **Repository:** [github/gh-actions-lock](https://github.com/github/gh-actions-lock)
- **Assessment baseline:** `3f7c302310dde7556291a2ce0792de36ec41a4c8`
- **Status:** CLI implementation and the repository-controlled #125 workaround combo are implemented and tested. M6 remains open for upstream runtime rejection and deployment of repository protections.
- **Progress:** 5 of 6 milestones complete.
- **Last validated:** 2026-10-11 KST.
- **Delivery:** [Implementation PR](https://github.com/qb20nh/gh-actions-lock/pull/2), [workaround branch](https://github.com/qb20nh/gh-actions-lock/tree/codex/m6/combo-20261011), [published release evidence](https://github.com/qb20nh/gh-actions-lock/tree/codex/m6/evidence-combo-20261011), and 12 attested draft prerelease binaries from `a23bb9b0d13aad17565ca53d8436f6959acaff6b`. Upstream merge remains separate.
- **Execution order:** M1 → M2 → M3 → M4 → M5 → M6.

## Milestones

- [x] **M1 — Complete dependency discovery and verification.**

  Recognize reusable workflow references and traverse their dependencies at the exact pinned commit, including nested and local workflow calls in the defining repository. Detect missing direct dependency metadata, dangling child references, cycles, and missing coverage for full-SHA references. Report manifest parsing failures and handle symlinked manifests consistently across API backends.

  **Acceptance:** Deterministic tests distinguish complete and incomplete graphs in online and offline verification. A reusable workflow dependency remains pinned after reconciliation. Incomplete discovery cannot produce a successful verification result or prune required pins.

  **Upstream:** [#129](https://github.com/github/gh-actions-lock/issues/129). Code scan also found incomplete-lock verification and symlink response discrepancies.

  **Implemented:** [Immutable discovery](../internal/resolve/discovery.go), [local action/workflow traversal](../internal/workflowfile/workflowfile.go), [API metadata handling](../internal/ghapi/graphql_action_files.go), and [coverage checks](../internal/pipeline/checks/structural.go). Online and offline verification reject incomplete metadata and missing full-SHA coverage.

  **Validated:** [Reusable workflow regressions](../internal/resolve/reusable_test.go), [metadata regressions](../internal/ghapi/metadata_test.go), [coverage regressions](../internal/pipeline/checks/coverage_test.go), and [CLI coverage regressions](../cmd/gh-actions-lock/coverage_regression_test.go), passing in the full race suite.

- [x] **M2 — Preserve pin identity and dependency history.**

  Keep refs case-sensitive while normalizing repository identity appropriately. Preserve distinct refs that resolve to the same SHA, and retain the original ref required by an immutable transitive caller when narrowing a direct use. Remove obsolete child edges when a parent changes commit; combine child edges only where the parent commit is unchanged.

  **Acceptance:** `Main` and `main` remain distinct. Rescanning cannot advance an existing trusted pin without the applicable relock or moved-ref authorization. After committing, every required direct and transitive ref has valid lock coverage, including multiple refs sharing a SHA. Updating a parent removes children it no longer declares.

  **Evidence:** Code scan reproduced case collisions, omitted refs sharing a SHA, narrowed-away transitive refs, and retained obsolete children.

  **Implemented:** [Pin planning](../internal/pin/plan.go) preserves ref case, distinct refs sharing a commit, and refs required by immutable transitive callers. [State reconciliation](../internal/lockfile/state.go) combines historical child edges only for the same commit and preserves matching canonical/alias closures.

  **Validated:** [Identity regressions](../internal/pin/identity_test.go), [state reconciliation tests](../internal/lockfile/state_test.go), and [commit tests](../internal/pin/commit_test.go), passing in the full race suite.

- [x] **M3 — Constrain workflow and lock mutations.**

  Rewrite only structural dependency references. Handle Unicode, quoted values, flow mappings, folded scalars, and anchors correctly, or reject unsupported edits before writing. Reparse candidate YAML and verify agreement between rewritten references and the candidate lock. Limit migration to the selected scope, keep verification read-only, and make local-action migration explicit or conservatively guarded. Prevent overlapping runs from silently overwriting each other's changes.

  **Acceptance:** Successful updates leave valid YAML and immediately pass dependency-coverage verification. Rejected pre-write checks leave file contents unchanged. Tests cover YAML containing `#` inside quoted values, Unicode before `uses`, unsupported rewrite forms, unrelated `uses` data, local-action checkout semantics, and overlapping mutations.

  **Upstream:** [#112](https://github.com/github/gh-actions-lock/issues/112), [#111](https://github.com/github/gh-actions-lock/issues/111), and [#123](https://github.com/github/gh-actions-lock/issues/123). Code scan also reproduced invalid YAML, source/lock disagreement, arbitrary data rewrites, and lost updates.

  **Implemented:** [Structural rewrites](../internal/workflowfile/rewrite.go) handle Unicode, quoted values, and flow mappings, and reject unsupported relevant multiline/anchored edits. [Commit staging](../internal/pin/commit.go) validates candidate YAML and lock coverage before writing, preserves symlinks and modes, and checks source bytes, canonical identities, and reachable manifest selection. [Mutation guards](../internal/lockfile/mutation.go) reject overlapping or stale updates. Migration requires `--migrate-local-actions`, follows selected reachable sources, and is disabled in read-only modes.

  **Validated:** [Rewrite regressions](../internal/workflowfile/rewrite_test.go), [staged commit regressions](../internal/pin/staged_commit_test.go), [migration command tests](../cmd/gh-actions-lock/migrate_test.go), and [persistence tests](../internal/lockfile/persistence_test.go), passing in the full race suite. Regressions include newly added higher-priority manifests, equal-byte symlink retargets, and cancellation before saving.

- [x] **M4 — Integrate the upstream PR stack with the remaining correctness fixes.**

  Assess and integrate [#145](https://github.com/github/gh-actions-lock/pull/145) → [#142](https://github.com/github/gh-actions-lock/pull/142) → [#148](https://github.com/github/gh-actions-lock/pull/148). Preserve matching-commit dependency closures when canonical names and aliases coexist. Handle verified repository transfers on enterprise hosts, enforce `--no-onboard` for new full-SHA dependencies, and canonicalize workflow identities to repository-relative paths. Distinguish planned changes from applied changes in logs and stage corrupt-lock recovery before replacing the original file.

  **Acceptance:** Command tests cover each remaining gap. Relative and absolute paths identify the same in-repository workflow. Rejected runs report no applied pins and preserve existing files. Update integration expectations for current flags and rejected partial-onboarding behavior. Document the guarantees for failures during final filesystem writes separately from pre-write rejection guarantees.

  **Evidence:** The stack addresses important behavior but leaves these gaps. Its nonblocking live integration results were not fully passing at assessment time.

  **Implemented:** Integrated the stack from #148 head `566e8f199ea259a4f7b44c7bfc57b35b68e5111c` into the local working tree. Added stable enterprise transfer handling, canonical workflow paths, staged corrupt-lock recovery, and [planned/applied JSON outcomes](../cmd/gh-actions-lock/format/json.go). `--no-onboard` excludes new workflows and intentionally rejects uncovered dependencies added to tracked workflows, including bare SHAs.

  **Validated:** [Parity command tests](../cmd/gh-actions-lock/parity_test.go), [onboarding coverage regressions](../cmd/gh-actions-lock/coverage_regression_test.go), [recovery tests](../cmd/gh-actions-lock/lockrecovery_test.go), [run-log tests](../cmd/gh-actions-lock/runlog_test.go), and the updated required stub catalog all pass. Final filesystem failures use best-effort restoration and report restoration failures; multi-file crash atomicity is not claimed.

- [x] **M5 — Enforce cooldown before adopting new commits.**

  Enforce configured minimum age on newly adopted direct and transitive commits during initial locking, relocking, and accepted moved-ref updates. Preserve unchanged existing pins. Define timestamp provenance and behavior for missing metadata or API failures. Ensure `--no-narrow` cannot bypass the policy, and do not silently select an older commit under an unchanged requested ref.

  **Acceptance:** Boundary, metadata-failure, and transitive-dependency tests prove that ineligible commits are rejected before writes. Existing unchanged pins remain usable. Diagnostics explain the candidate's age, timestamp source, and rejection reason.

  **Upstream:** [#132](https://github.com/github/gh-actions-lock/issues/132). The current configuration and warning behavior does not enforce adoption eligibility.

  **Implemented:** [Pre-commit eligibility enforcement](../cmd/gh-actions-lock/enforcement.go) uses an exact published GitHub Release and verifies its tag currently resolves to the candidate SHA. Missing metadata, API failures, unknown release age, and known recorded tag retargets reject adoption. Existing commits remain usable, and `--no-narrow` cannot bypass enforcement. Current release metadata proves publication age and the present tag target; it cannot prove historical tag-to-commit continuity throughout the period.

  **Validated:** [Cooldown tests](../cmd/gh-actions-lock/enforcement_test.go) cover boundaries, direct/transitive adoption, metadata failures, tag mismatch/retarget, unchanged commits, and actual command execution with `--no-narrow` and pending migration. [Exact-release API tests](../internal/ghapi/release_test.go) also pass in the full race suite.

- [ ] **M6 — Establish release readiness and document external dependencies.**

  Run formatting, vet, race tests, and required stub integration checks in a supported environment. Perform relevant runner/service smoke tests before claiming cross-repository `$/` support. Document local-action semantics, incomplete coverage, cooldown behavior, and filesystem failure guarantees. Track runtime malformed-lock handling and generated-action support as external dependencies with explicit completion criteria.

  **Acceptance:** Every milestone links to its implementation and validation evidence. Required checks pass. External limitations have an owner or upstream reference and an explicit effect on release scope. Documentation makes no support claims beyond verified behavior.

  **External tracking:** [#125](https://github.com/github/gh-actions-lock/issues/125), [#126](https://github.com/github/gh-actions-lock/issues/126), and [#128](https://github.com/github/gh-actions-lock/issues/128).

  **Completed locally:** Required formatting, vet, release-script checks, full race suite, and stub integration pass. [README](../README.md) documents verification limits, migration semantics, cooldown provenance, planned/applied outcomes, and filesystem failure guarantees.

  **GitHub validation:** [Required checks and release build](https://github.com/qb20nh/gh-actions-lock/actions/runs/38056832635) pass at commit `34c65ecf262f580f066bf1d105a2ecc310226374`. The [validation workflow](../.github/workflows/m6-validation.yml) produced an [attested draft prerelease](https://github.com/qb20nh/gh-actions-lock/releases/tag/untagged-f25714a2bdfe7271be1e) with 12 platform binaries; its annotated tag resolves to that tested commit. The [release asset workflow](../.github/workflows/m6-release-smoke.yml) [passed](https://github.com/qb20nh/gh-actions-lock/actions/runs/38057728082) verification of the uploaded Linux binary's digest, hosted build attestation, exact source commit, execution, and offline workflow coverage. Draft notes now include the runtime results and remaining blocker.

  **Runtime validation:** The [runtime workflow](../test/runtime/workflows/m6-runtime.yml) [passed](https://github.com/qb20nh/gh-actions-lock/actions/runs/38056833390) same-repository actions, foreign actions, nested foreign reusable workflows with a poisoned caller checkout, and a symbolic ref whose live marker differs from its recorded pin. The hosted runner was `2.337.0`, Ubuntu `24.04.5`, image `20261004.327.1`. Marker and source-path assertions establish binding to the defining repository. The divergent-ref job announces locked versions and downloads the recorded `38a3e6d` commit despite the branch pointing to `7e72f65`.

  **Provider:** An isolated [fixture branch](https://github.com/qb20nh/transition-test/tree/codex/m6-gh-actions-lock-fixtures-20261010) records marker `v1` at [38a3e6d](https://github.com/qb20nh/transition-test/commit/38a3e6df964dfe1756abb8dec154228d7140a841); its live ref advances to marker `v2` at [7e72f65](https://github.com/qb20nh/transition-test/commit/7e72f651cfb3ae0aba09f652b96b1efda2454d5b). The caller lock retains `v1` for the divergent-ref test. Default branches are outside this test's mutation scope.

  **Remaining release gate:** A [committed syntactically malformed lock](https://github.com/qb20nh/gh-actions-lock/actions/runs/38056912627) was ignored, and `M6_LOCK_SENTINEL_EXECUTED` ran. This reproduces #125 and keeps M6 open. A [dangling-edge lock](https://github.com/qb20nh/gh-actions-lock/actions/runs/38056929771) was rejected at startup with an explicit `Invalid lockfile` annotation and no jobs. Re-run the syntax-negative control after the upstream service fix; completion requires a lock-specific rejection before the sentinel executes. [Recorded evidence](../test/runtime/evidence/2026-10-10.md) links all commits, runs, and assertions.

  **Locally controllable workaround:** A separate validator job with no actions, container, or services [rejected committed malformed YAML](https://github.com/qb20nh/gh-actions-lock/actions/runs/38058973704), and its dependent consumer was skipped. The [valid control](https://github.com/qb20nh/gh-actions-lock/actions/runs/38058951167) passed and executed the consumer. [Probe workflow](../test/runtime/workflows/m6-workarounds.yml) builds an immutable trusted verifier and reads the exact candidate commit as data. Independent probes confirm direct-SHA protection and show that the same pinned parent downloads different mutable children when lock enforcement is absent. [Workaround assessment](../test/runtime/evidence/2026-10-10-workarounds.md) ranks gates, protected checks, SHA policies, graph freezing, and runner-side options.

  **Workaround combo:** [Implementation and evidence](../test/runtime/evidence/2026-10-11-combo.md) and [activation instructions](../docs/actions-protection.md).

  - [x] Add an action-free trusted validator and gate all CI/release consumers on success.
  - [x] Require a nonempty regular lock and verify the actual defining-commit dependency graph with full SHAs; reject missing edges and inconclusive inspection.
  - [x] Freeze every active remote reference and vendor the release composite's mutable children, preserving build and attestation behavior.
  - [x] Materialize raw Git blobs so ignored tracked files, export attributes, and checkout filters cannot alter validation inputs.
  - [x] Enable the local staged-file hook and verify malformed/missing locks, direct/recorded-child tags, and ignored/export-ignored workflows.
  - [x] Publish exact-revision policy statuses and test valid, malformed, missing, omitted-child, export-ignore, and release-negative controls on GitHub.
  - [x] Prepare CODEOWNERS, an exact SHA allowlist, and an idempotent administration script preserving existing protections.
  - [x] Validate real PR head and merge status binding through the hosted policy-script harness; all 103 scenarios pass in the corrected live catalog.
  - [x] Build 12 attested binaries through the vendored composite and verify the uploaded Linux binary's digest, source attestation, frozen graph and missing-lock rejection.
  - [ ] Deploy the policy on the default branch, verify actual PRtarget and merge-group delivery, and apply required checks, SHA policy, and exact allowlist with an administration credential. Current connector/shell capabilities cannot apply settings.
  - [x] Confirm the fork's dependency-review API is available: the [rerun](https://github.com/qb20nh/gh-actions-lock/actions/runs/38066247251) passed both its lock gate and dependency review after the initial 403.
  - [ ] Re-run platform malformed-lock rejection after the upstream #125 fix; require an explicit lock error and no execution sentinel.

  Repository gating mitigates #125 for the covered consumers. It does not replace the original platform-enforcement completion criterion or claim to freeze arbitrary container images and shell downloads.

  | Upstream reference | Effect on release scope | Completion evidence |
  | --- | --- | --- |
  | [#125 — malformed lock ignored at runtime](https://github.com/github/gh-actions-lock/issues/125) | Reproduced on the hosted service. The repository-controlled combo rejects bad locks before covered consumers execute; activation and platform YAML rejection remain open. | Deploy protections, then re-run the original syntax-negative control after the upstream fix; require an explicit service lock error and no sentinel execution. |
  | [#126 — cross-repository `$/` execution failure](https://github.com/github/gh-actions-lock/issues/126) | Positive probes pass on the recorded deployment; migration remains opt-in because workspace semantics can differ. | Met for this deployment: same/foreign actions and nested reusable workflows bind to the defining commit on hosted runner `2.337.0`. |
  | [#128 — generated local Docker action](https://github.com/github/gh-actions-lock/issues/128), [pypa #425](https://github.com/pypa/gh-action-pypi-publish/issues/425) | Generated workspace actions remain incomplete coverage and cannot be onboarded as complete locks. | Upstream exposes deterministic dependency metadata or defines a supported generated-action model; add discovery and coverage regressions before changing support claims. |

## Validation baseline

Targeted deterministic probes reproduced the code-scan findings. Some probes assert the observed defect rather than the desired behavior; passing those probes is evidence of a defect, not a verified fix.

The assessment environment initially lacked a clean full-suite baseline: synthetic parent `.git` directories affected temporary-repository fixtures, and sandbox restrictions prevented local HTTP test servers. Final checks used the prepared Go 1.25.8 toolchain, isolated fixture directories under `/dev/shm`, executable Go build temporary files under `/tmp`, and permitted localhost networking.

## Final validation evidence

The four P2 findings from PR #2 are fixed and covered by regressions that failed
before their fixes: migration follows selected reusable workflows, parity findings
preserve tenant and public fallback hosts, the hook builds staged verifier source,
and JSON combines duplicate outcomes independent of workflow order. The updated
race/release suite, five materializer tests, 12 protection/hook tests, formatting,
vet, and all 34 stub scenarios pass. [Review validation log](/workspace/.cloud-setup/gh-actions-lock/review-p2-checks.log).

All checks below passed after the final CLI changes:

| Check | Result | Local execution log |
| --- | --- | --- |
| `make test` | Release tag selection/verification and `go test -race -count=1 ./...` pass. | [Race/release log](/workspace/.cloud-setup/gh-actions-lock/goal-final-test.log) |
| `make vet` | Pass. | [Vet log](/workspace/.cloud-setup/gh-actions-lock/goal-final-vet.log) |
| `make fmt-check` | Pass. | Re-run with the prepared toolchain. |
| `git diff --check` and `git diff --cached --check` | Pass. | No whitespace errors. |
| `make test-stub` | 34 passed, 0 failed. | [Stub integration log](/workspace/.cloud-setup/gh-actions-lock/goal-final-stub.log) |

Hosted required checks and runtime smoke results are recorded in the [M6 evidence report](../test/runtime/evidence/2026-10-10.md). M6 remains open because the service ignored a syntactically malformed lock; no required CLI test failure remains.

## Completion and follow-ups

The goal is complete when all six milestones meet their acceptance criteria and no unresolved release-blocking CLI defect remains within this scope. Track progress by checking off each milestone and linking its changes and validation evidence. Publishing or merging upstream is a separate action.

Follow-up goals cover repository configuration ([#133](https://github.com/github/gh-actions-lock/issues/133), [#134](https://github.com/github/gh-actions-lock/issues/134)), caller-local hooks ([#138](https://github.com/github/gh-actions-lock/issues/138)), and an upgrade command ([#140](https://github.com/github/gh-actions-lock/issues/140)). Build these on the corrected discovery, mutation, and policy behavior.
