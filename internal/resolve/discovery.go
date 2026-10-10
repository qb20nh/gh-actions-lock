package resolve

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	parserlock "github.com/github/actions-lockfile/go/pkg/lockfile"
	"github.com/github/gh-actions-lock/internal/dep"
	"github.com/github/gh-actions-lock/internal/ghapi"
	"github.com/github/gh-actions-lock/internal/pinpool"
	"github.com/github/gh-actions-lock/internal/workflowfile"
)

// CompositeLocalPathError is returned when a fetched action or workflow uses
// a caller-workspace path (./…) in its steps. Checkout and generated-file
// behavior determine its contents, so static dependency discovery is incomplete.
type CompositeLocalPathError struct {
	// Parent is the NWO@Ref of the composite action containing the local path.
	Parent string
	// LocalPath is the ./… value found in the composite's steps.
	LocalPath string
}

func (e *CompositeLocalPathError) Error() string {
	return fmt.Sprintf("dependency %s uses workspace-local path %q which cannot be resolved for pinning", e.Parent, e.LocalPath)
}

// IsCompositeLocalPath reports whether err (or any error in its chain)
// is a CompositeLocalPathError.
func IsCompositeLocalPath(err error) bool {
	var target *CompositeLocalPathError
	return errors.As(err, &target)
}

// InvalidSelfRepositoryRefError is returned when a fetched composite carries
// the invalid `$/…@ref` form.
type InvalidSelfRepositoryRefError struct {
	Parent string
	Ref    string
}

func (e *InvalidSelfRepositoryRefError) Error() string {
	return fmt.Sprintf("composite action %s uses self repository reference %q with a forbidden @ref", e.Parent, e.Ref)
}

// IsInvalidSelfRepositoryRef reports whether err contains an invalid nested
// self repository reference.
func IsInvalidSelfRepositoryRef(err error) bool {
	var target *InvalidSelfRepositoryRefError
	return errors.As(err, &target)
}

// selfRepositoryPrefix binds a nested dependency to the defining repository.
const selfRepositoryPrefix = "$/"

// InvalidMetadataError reports a manifest whose dependencies cannot be inspected.
type InvalidMetadataError struct {
	Parent string
	Err    error
}

func (e *InvalidMetadataError) Error() string {
	return fmt.Sprintf("reading dependencies of %s: %v", e.Parent, e.Err)
}

func (e *InvalidMetadataError) Unwrap() error { return e.Err }

// IsInvalidMetadata reports whether err contains an invalid dependency manifest.
func IsInvalidMetadata(err error) bool {
	var target *InvalidMetadataError
	return errors.As(err, &target)
}

// ConflictingRefError reports a mutable ref observed at different commits
// within one dependency closure, which cannot share one lockfile pin.
type ConflictingRefError struct {
	Key      string
	FirstSHA string
	NextSHA  string
}

func (e *ConflictingRefError) Error() string {
	return fmt.Sprintf("dependency %s resolved to conflicting commits %s and %s", e.Key, e.FirstSHA, e.NextSHA)
}

// IsConflictingRef reports whether a closure contains incompatible pins.
func IsConflictingRef(err error) bool {
	var target *ConflictingRefError
	return errors.As(err, &target)
}

type resolutionRequest struct {
	ref      parserlock.ActionRef
	fetchRef string
}

func (r resolutionRequest) key() ghapi.ActionRef {
	ref := r.ref.Ref
	if r.fetchRef != "" {
		ref = r.fetchRef
	}
	return ghapi.ForActionRef(r.ref.Owner, r.ref.Repo, r.ref.Path, ref)
}

// LatestRef returns the highest stable tag for an action repository.
func (r *Resolver) LatestRef(ctx context.Context, owner, repo string) (string, error) {
	key := ghapi.ForRepo(owner, repo)
	if ref, ok := r.latestRefCache.Get(key); ok {
		return ref, nil
	}

	entries, err := r.gh.ListTags(ctx, owner, repo)
	if err != nil {
		return "", err
	}

	tags := make([]string, 0, len(entries))
	for _, e := range entries {
		tags = append(tags, e.Name)
	}

	best := selectLatestTag(tags)
	if best == "" {
		return "", fmt.Errorf("%s: no tags available to upgrade", key)
	}

	r.latestRefCache.Put(key, best)
	return best, nil
}

func cacheKey(ref parserlock.ActionRef) ghapi.ActionRef {
	return ghapi.ForActionRef(ref.Owner, ref.Repo, ref.Path, ref.Ref)
}

// batchActionFileSize caps how many action refs are folded into a single
// batched ResolveActionFiles GraphQL round-trip. The action-file query is
// heavy per alias (repository + object + two blob fetches), so this stays well
// under reachability's branch batch size. ResolveActionFiles splits adaptively
// if a chunk is still rejected for cost, so this is an upper bound, not a
// fixed request shape.
const batchActionFileSize = 20

// ResolveAllRecursive reads action and reusable workflow manifests to discover
// the transitive dependency closure at their resolved commits.
// The returned ParentMap (child dep key → parent dep keys) is owned by the
// caller and safe to mutate or hold across concurrent resolver calls.
func (r *Resolver) ResolveAllRecursive(ctx context.Context, refs []parserlock.ActionRef) ([]dep.Dependency, dep.ParentMap, error) {
	return r.resolveAllRecursive(ctx, refs, false)
}

// InspectAllRecursive fetches manifests even for pins seeded from a lockfile.
// Cached manifests fetched from their defining commits remain reusable.
func (r *Resolver) InspectAllRecursive(ctx context.Context, refs []parserlock.ActionRef) ([]dep.Dependency, dep.ParentMap, error) {
	return r.resolveAllRecursive(ctx, refs, true)
}

func (r *Resolver) resolveAllRecursive(ctx context.Context, refs []parserlock.ActionRef, requireMetadata bool) ([]dep.Dependency, dep.ParentMap, error) {
	seen := make(map[ghapi.ActionRef]bool)
	var allDeps []dep.Dependency
	resolvedPins := make(map[string]string)
	parentMap := make(dep.ParentMap)
	workflowCalls := make(map[ghapi.ActionRef][]ghapi.ActionRef)

	pending := make([]resolutionRequest, len(refs))
	for i, ref := range refs {
		pending[i] = resolutionRequest{ref: ref}
	}
	depth := 0

	// Rolling counters spanning every BFS depth. Total grows as transitive
	// refs are discovered; done grows as workers complete. The caller renders
	// a single non-jumping bar over the union of work — "transitive" is not a
	// distinct top-level phase, just deeper edges in the same graph.
	var resolveDone atomic.Int64
	var resolveTotal atomic.Int64
	// Fire an initial 0/N callback so the UI shows the bar immediately at the
	// known size of the first wave (refs the caller passed in).
	if r.OnResolveProgress != nil {
		// Compute first-wave uncached size up-front for an accurate initial
		// total. Dedup by cacheKey to mirror the seen{} dedup the BFS loop
		// applies before resolving, so duplicate input refs don't inflate the
		// total and leave the bar stranded short of [N/N].
		firstWave := 0
		seenFirst := make(map[ghapi.ActionRef]bool, len(refs))
		for _, ref := range refs {
			key := cacheKey(ref)
			if seenFirst[key] {
				continue
			}
			seenFirst[key] = true
			if _, ok := r.cache.Get(key); !ok {
				firstWave++
			}
		}
		resolveTotal.Store(int64(firstWave))
		if firstWave > 0 {
			r.FireResolveProgress(0, firstWave)
		}
	}

	var resolveErr error

	for len(pending) > 0 {
		if depth >= r.MaxRecursionDepth {
			return allDeps, parentMap, fmt.Errorf("dependency recursion exceeded max depth %d", r.MaxRecursionDepth)
		}

		var toResolve []resolutionRequest
		for _, request := range pending {
			key := request.key()
			if !seen[key] {
				seen[key] = true
				toResolve = append(toResolve, request)
			}
		}

		if len(toResolve) == 0 {
			break
		}

		var deps []dep.Dependency
		var actionYMLs []string
		var err error
		deps, actionYMLs, err = r.resolveWithActionYMLParallel(ctx, toResolve, depth, &resolveDone, &resolveTotal, requireMetadata)
		if depth > 0 {
			// A remote composite's `uses:` can't be rewritten here, and the
			// runner looks it up as written, so keep the name it uses.
			for i := range deps {
				if len(deps[i].OriginalRefs) > 0 {
					deps[i].NWO = deps[i].OriginalRefs[0].NWO()
					deps[i].OriginalRefs = nil
				}
			}
		}
		// Keep partial results: per-ref failures are surfaced via err, but
		// successful resolutions in `deps` should not be discarded — downstream
		// renderers degrade gracefully per-ref instead of marking everything
		// unresolved. Continue BFS so transitive deps of successful refs are
		// still discovered.
		for _, d := range deps {
			key := strings.ToLower(d.NWO) + "@" + d.Ref
			if sha, ok := resolvedPins[key]; ok && !strings.EqualFold(sha, d.SHA) {
				resolveErr = errors.Join(resolveErr, &ConflictingRefError{Key: d.Key(), FirstSHA: sha, NextSHA: d.SHA})
			} else {
				resolvedPins[key] = d.SHA
			}
		}
		allDeps = append(allDeps, deps...)
		if err != nil {
			resolveErr = errors.Join(resolveErr, err)
		}

		var nextPending []resolutionRequest
		for i := range deps {
			yml := actionYMLs[i]
			if yml == "" {
				continue
			}

			uses, parseErr := dependencyUses(deps[i], yml)
			if parseErr != nil {
				resolveErr = errors.Join(resolveErr, &InvalidMetadataError{Parent: deps[i].FullName() + "@" + deps[i].Ref, Err: parseErr})
				continue
			}

			// parentMap is keyed at runner-download granularity (NWO@Ref =
			// Dependency.Key()). BFS traversal above is path-aware via
			// cacheKey/seen{} so we visit every sub-action's action.yml,
			// but the recorded edges flatten subpaths back into one node
			// per tarball — same model the runner uses.
			parentKey := deps[i].Key()
			for _, site := range uses {
				use := site.Value
				localWorkflow := !site.StepLevel && strings.HasPrefix(use, "./")
				if strings.HasPrefix(use, "./") && !localWorkflow {
					resolveErr = errors.Join(resolveErr, &CompositeLocalPathError{
						Parent:    parentKey,
						LocalPath: use,
					})
					continue
				}
				var request resolutionRequest
				if strings.HasPrefix(use, selfRepositoryPrefix) || localWorkflow {
					if strings.Contains(use, "@") {
						resolveErr = errors.Join(resolveErr, &InvalidSelfRepositoryRefError{
							Parent: parentKey,
							Ref:    use,
						})
						continue
					}
					// `$/…` inside a composite is a same-tarball self repository reference:
					// it resolves within THIS composite's own repo at its own
					// resolved SHA (the parent repo, not the workflow-run repo).
					// Retain the logical ref for lockfile identity while fetching
					// the sibling action file from the immutable parent commit.
					owner, repo := deps[i].OwnerRepo()
					path := strings.TrimPrefix(use, selfRepositoryPrefix)
					if localWorkflow {
						path = strings.TrimPrefix(use, "./")
					}
					if !site.StepLevel && parserlock.ParseReusableWorkflowRef(owner+"/"+repo+"/"+path+"@"+deps[i].Ref) == nil {
						resolveErr = errors.Join(resolveErr, &InvalidMetadataError{Parent: parentKey, Err: fmt.Errorf("invalid local reusable workflow %q", use)})
						continue
					}
					request = resolutionRequest{
						ref: parserlock.ActionRef{
							Owner: owner,
							Repo:  repo,
							Path:  path,
							Ref:   deps[i].Ref,
						},
						fetchRef: deps[i].SHA,
					}
				} else {
					var actionRef *parserlock.ActionRef
					if site.StepLevel {
						actionRef = parserlock.ParseActionRef(use)
					} else {
						if ref := parserlock.ParseReusableWorkflowRef(use); ref != nil {
							actionRef = &parserlock.ActionRef{Owner: ref.Owner, Repo: ref.Repo, Path: ref.Path, Ref: ref.Ref, Raw: ref.Raw}
						}
					}
					if actionRef == nil {
						if requireMetadata && strings.HasPrefix(use, "docker://") && !workflowfile.IsImmutableDockerRef(use) {
							resolveErr = errors.Join(resolveErr, &InvalidMetadataError{Parent: parentKey, Err: fmt.Errorf("container action %q does not use a full SHA-256 digest", use)})
						}
						if !site.StepLevel || !strings.HasPrefix(use, "docker://") {
							resolveErr = errors.Join(resolveErr, &InvalidMetadataError{Parent: parentKey, Err: fmt.Errorf("invalid dependency reference %q", use)})
						}
						continue
					}
					request = resolutionRequest{ref: *actionRef}
				}
				actionRef := request.ref
				if actionRef.Owner == "" {
					continue
				}
				if !site.StepLevel {
					owner, repo := deps[i].OwnerRepo()
					caller := ghapi.ForActionRef(owner, repo, deps[i].Path, deps[i].Ref)
					workflowCalls[caller] = append(workflowCalls[caller], cacheKey(actionRef))
				}
				childKey := actionRef.NWO() + "@" + actionRef.Ref
				// Same-tarball edge: a composite whose `uses:` names another
				// subpath in its own repo+ref. At runner-download granularity
				// this is not a new transitive dependency (same tarball, same
				// SHA), so we must not record a parentMap edge — once subpaths
				// collapse to NWO@Ref it would become a self-edge. But we must
				// still descend into the sibling sub-action's action.yml: it
				// can pull in cross-repo transitive deps the parent never
				// references directly (e.g. nested-composite → simple-composite
				// → other-repo). The path-aware seen{} set keys on FullName@Ref
				// and so prevents re-resolving an exact self repository reference, making
				// this enqueue loop-safe.
				if childKey == parentKey {
					nextPending = append(nextPending, request)
					continue
				}
				// Track all parents, deduplicating.
				parents := parentMap[childKey]
				found := false
				for _, p := range parents {
					if p == parentKey {
						found = true
						break
					}
				}
				if !found {
					parentMap[childKey] = append(parents, parentKey)
				}
				nextPending = append(nextPending, request)
			}
		}

		pending = nextPending
		depth++
	}

	if cycle, ok := reusableWorkflowCycle(workflowCalls); ok {
		resolveErr = errors.Join(resolveErr, &InvalidMetadataError{Parent: cycle.String(), Err: fmt.Errorf("reusable workflow call cycle")})
	}
	return dep.Dedup(allDeps), parentMap, resolveErr
}

// Workflow paths remain distinct here even when the lockfile collapses them
// into the same repository tarball.
func reusableWorkflowCycle(calls map[ghapi.ActionRef][]ghapi.ActionRef) (ghapi.ActionRef, bool) {
	visited, active := make(map[ghapi.ActionRef]bool), make(map[ghapi.ActionRef]bool)
	var visit func(ghapi.ActionRef) (ghapi.ActionRef, bool)
	visit = func(ref ghapi.ActionRef) (ghapi.ActionRef, bool) {
		if active[ref] {
			return ref, true
		}
		if visited[ref] {
			return ghapi.ActionRef{}, false
		}
		visited[ref], active[ref] = true, true
		for _, child := range calls[ref] {
			if cycle, ok := visit(child); ok {
				return cycle, true
			}
		}
		active[ref] = false
		return ghapi.ActionRef{}, false
	}
	roots := make([]ghapi.ActionRef, 0, len(calls))
	for ref := range calls {
		roots = append(roots, ref)
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].String() < roots[j].String() })
	for _, ref := range roots {
		if cycle, ok := visit(ref); ok {
			return cycle, true
		}
	}
	return ghapi.ActionRef{}, false
}

func dependencyUses(d dep.Dependency, content string) ([]workflowfile.UseSite, error) {
	f, err := workflowfile.Parse(d.FullName(), []byte(content))
	if err != nil {
		return nil, err
	}
	if parserlock.ParseReusableWorkflowRef(d.FullName()+"@"+d.Ref) != nil {
		return f.WorkflowUses()
	}
	meta, err := parserlock.ParseActionMeta(content)
	if err != nil {
		return nil, err
	}
	if meta.Execution == parserlock.ExecUnknown {
		return nil, fmt.Errorf("action manifest has no supported runs.using value")
	}
	if meta.Execution != parserlock.ExecComposite {
		return nil, nil
	}
	return f.Uses(), nil
}

// resolveWithActionYMLParallel resolves refs one-per-worker via the shared pool
// so the UI can show a stable [done/total] counter and per-worker status rows.
// Cached refs short-circuit without a worker.
//
// resolveDone and resolveTotal are rolling counters owned by ResolveAllRecursive
// that span every BFS depth, so a single non-jumping progress bar can cover
// direct + transitive resolution as one phase.
func (r *Resolver) resolveWithActionYMLParallel(ctx context.Context, refs []resolutionRequest, depth int, resolveDone, resolveTotal *atomic.Int64, requireMetadata bool) ([]dep.Dependency, []string, error) {
	type resolveResult struct {
		dep dep.Dependency
		yml string
		ok  bool
	}
	results := make([]resolveResult, len(refs))

	var uncachedIdx []int
	for i, request := range refs {
		entry, ok := r.cache.Get(request.key())
		if !ok && request.fetchRef != "" {
			entry, ok = r.cache.Get(cacheKey(request.ref))
			ok = ok && strings.EqualFold(entry.dep.SHA, request.fetchRef)
		}
		if requireMetadata && entry.actionYML == "" {
			ok = false
		}
		if ok {
			d := entry.dep
			d.Ref = request.ref.Ref
			results[i] = resolveResult{dep: d, yml: entry.actionYML, ok: true}
		} else {
			uncachedIdx = append(uncachedIdx, i)
		}
	}

	flatten := func() ([]dep.Dependency, []string) {
		var deps []dep.Dependency
		var ymls []string
		for _, res := range results {
			if !res.ok {
				continue
			}
			deps = append(deps, res.dep)
			ymls = append(ymls, res.yml)
		}
		return deps, ymls
	}

	total := len(uncachedIdx)
	if total == 0 {
		deps, ymls := flatten()
		return deps, ymls, nil
	}

	// Grow the rolling resolve total by the new uncached refs at this depth.
	// First-wave size was preseeded by ResolveAllRecursive, so only count
	// deeper depths here to avoid double-counting.
	if depth > 0 {
		newTotal := resolveTotal.Add(int64(total))
		r.FireResolveProgress(int(resolveDone.Load()), int(newTotal))
	}

	// Group the wave's uncached refs into batches, each resolved in one
	// ResolveActionFiles GraphQL round-trip instead of one request per ref.
	// One pool job == one batch; the per-ref progress counter still ticks once
	// per ref so the bar stays ref-denominated. The pool runs with an empty
	// label so its batch-denominated counter doesn't fight FireResolveProgress.
	type actionBatch struct {
		idxs []int                     // global indices into refs/results
		reqs []ghapi.ActionFileRequest // aligned 1:1 with idxs
	}
	var batches []actionBatch
	for start := 0; start < len(uncachedIdx); start += batchActionFileSize {
		end := start + batchActionFileSize
		if end > len(uncachedIdx) {
			end = len(uncachedIdx)
		}
		span := uncachedIdx[start:end]
		b := actionBatch{
			idxs: make([]int, len(span)),
			reqs: make([]ghapi.ActionFileRequest, len(span)),
		}
		for j, idx := range span {
			request := refs[idx]
			fetchRef := request.fetchRef
			if fetchRef == "" {
				fetchRef = request.ref.Ref
			}
			b.idxs[j] = idx
			b.reqs[j] = ghapi.ActionFileRequest{
				Owner: request.ref.Owner, Repo: request.ref.Repo,
				Path: request.ref.Path, Ref: fetchRef,
			}
		}
		batches = append(batches, b)
	}

	poolErr := pinpool.RunTyped(r.Pool, ctx, "",
		batches,
		func(b actionBatch) string {
			head := refs[b.idxs[0]].ref
			label := "resolving " + head.NWO() + "@" + head.Ref
			if len(b.idxs) > 1 {
				label = fmt.Sprintf("%s (+%d more)", label, len(b.idxs)-1)
			}
			return label
		},
		func(ctx context.Context, _ int, b actionBatch) error {
			res := r.gh.ResolveActionFiles(ctx, b.reqs)
			var errs []error
			for j, idx := range b.idxs {
				ref := refs[idx].ref
				if j < len(res) && res[j].Err == nil {
					d := dep.Dependency{
						Hostname: res[j].Hostname,
						NWO:      res[j].Owner + "/" + res[j].Repo,
						Path:     res[j].Path,
						Ref:      ref.Ref,
						SHA:      res[j].CommitOID,
					}
					if res[j].OriginalNWO != "" {
						d.OriginalRefs = []parserlock.ActionRef{ref}
					}
					r.cache.Put(refs[idx].key(), resolvedEntry{dep: d, actionYML: res[j].ActionYML})
					results[idx] = resolveResult{dep: d, yml: res[j].ActionYML, ok: true}
				} else if j < len(res) && res[j].Err != nil {
					if ghapi.IsMetadataError(res[j].Err) {
						errs = append(errs, &InvalidMetadataError{Parent: ref.FullName() + "@" + ref.Ref, Err: res[j].Err})
					} else {
						errs = append(errs, fmt.Errorf("%s@%s: %w", ref.FullName(), ref.Ref, res[j].Err))
					}
				}
				done := resolveDone.Add(1)
				r.FireResolveProgress(int(done), int(resolveTotal.Load()))
			}
			return errors.Join(errs...)
		},
	)

	deps, ymls := flatten()
	return deps, ymls, poolErr
}

// selectLatestTag returns the highest semver tag from a list of tag names.
// Prefers vN (major-only) over vN.M.P when a matching major exists.
func selectLatestTag(tags []string) string {
	seen := make(map[string]struct{}, len(tags))
	bestMajor := -1
	bestMajorTag := ""
	bestVersion := [3]int{-1, -1, -1}
	bestVersionTag := ""
	bestFallback := ""

	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		if tag > bestFallback {
			bestFallback = tag
		}

		sv, ok := parserlock.ParseSemVer(tag)
		if !ok || !sv.IsStable() {
			continue
		}

		if sv.Raw == sv.MajorTag() && sv.Major > bestMajor {
			bestMajor = sv.Major
			bestMajorTag = tag
		}

		version := [3]int{sv.Major, sv.Minor, sv.Patch}
		if version[0] > bestVersion[0] ||
			(version[0] == bestVersion[0] && version[1] > bestVersion[1]) ||
			(version[0] == bestVersion[0] && version[1] == bestVersion[1] && version[2] > bestVersion[2]) {
			bestVersion = version
			bestVersionTag = tag
		}
	}

	if bestMajorTag != "" {
		return bestMajorTag
	}
	if bestVersionTag != "" {
		return bestVersionTag
	}
	return bestFallback
}
