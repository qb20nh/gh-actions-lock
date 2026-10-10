package pipeline

import (
	"context"
	"fmt"
	"strings"

	parserlock "github.com/github/actions-lockfile/go/pkg/lockfile"
	"github.com/github/gh-actions-lock/internal/dep"
	"github.com/github/gh-actions-lock/internal/pipeline/checks"
	"github.com/github/gh-actions-lock/internal/workflowfile"
)

func verifyFrozen(ctx context.Context, opts RunOptions, parsed []checks.ParsedWorkflow) (*RunResult, error) {
	report := VerifyLocalCoverage(parsed, opts.Store)
	var allDeps []dep.Dependency
	allParents := make(dep.ParentMap)
	if opts.Resolver != nil {
		opts.Resolver.OnResolveProgress = opts.OnResolveProgress
		defer func() { opts.Resolver.OnResolveProgress = nil }()
	}
	for i, pw := range parsed {
		wr := &report.Workflows[i]
		// SHA references are the expected policy here, so do not recommend
		// replacing them with symbolic refs as the normal coverage check does.
		findings := wr.Findings[:0]
		for _, f := range wr.Findings {
			if f.Category != checks.ShaAsRef {
				findings = append(findings, f)
			}
		}
		wr.Findings = findings
		for source, content := range pw.SourceContents {
			definition, err := workflowfile.Parse(source, content)
			if err != nil {
				continue // ParseAll already records source errors.
			}
			for _, site := range definition.Uses() {
				use := strings.TrimSpace(site.Value)
				if site.StepLevel && strings.HasPrefix(use, "docker://") {
					if !workflowfile.IsImmutableDockerRef(use) {
						wr.Findings = append(wr.Findings, checks.Finding{
							WorkflowPath: pw.Path,
							Category:     checks.MutableReference,
							Severity:     checks.SeverityError,
							Confidence:   checks.ConfidenceHigh,
							Detail:       fmt.Sprintf("container action %s in %s does not use a full SHA-256 digest", use, source),
							Remediation:  "use docker://IMAGE@sha256:DIGEST with a full SHA-256 digest",
						})
					}
					continue
				}
				if strings.HasPrefix(use, "./") || strings.HasPrefix(use, "$/") {
					continue
				}
				valid := parserlock.ParseActionRef(use) != nil
				if !site.StepLevel {
					valid = parserlock.ParseReusableWorkflowRef(use) != nil
				}
				if !valid {
					wr.Findings = append(wr.Findings, checks.Finding{
						WorkflowPath: pw.Path,
						Category:     checks.NotPinned,
						Severity:     checks.SeverityError,
						Confidence:   checks.ConfidenceHigh,
						Detail:       fmt.Sprintf("invalid dependency reference %q in %s", use, source),
						Remediation:  "use a valid repository reference and full commit SHA",
					})
				}
			}
		}
		seen := make(map[string]bool)
		checkRef := func(ref parserlock.ActionRef, parent string) {
			key := ref.FullName() + "@" + ref.Ref
			if parserlock.IsFullSha(ref.Ref) || seen[key] {
				return
			}
			seen[key] = true
			wr.Findings = append(wr.Findings, checks.Finding{
				WorkflowPath: pw.Path,
				ActionRef:    &ref,
				ParentNWO:    parent,
				Category:     checks.MutableReference,
				Severity:     checks.SeverityError,
				Confidence:   checks.ConfidenceHigh,
				Detail:       fmt.Sprintf("dependency %s uses a mutable remote reference", key),
				Remediation:  "use a full commit SHA; upgrade or vendor the parent if the reference is inside a remote dependency",
			})
		}
		for _, ref := range pw.Refs {
			checkRef(ref, "")
		}
		if isBlocked(pw) || len(pw.LocalPaths) > 0 || len(pw.Refs) == 0 {
			continue
		}
		if opts.Resolver == nil {
			return nil, fmt.Errorf("frozen graph verification requires a resolver")
		}
		resolved, parents, err := opts.Resolver.InspectAllRecursive(ctx, pw.Refs)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			wr.Findings = append(wr.Findings, checks.Finding{
				WorkflowPath: pw.Path,
				Category:     checks.ReachabilityUnknown,
				Severity:     checks.SeverityError,
				Confidence:   checks.ConfidenceLow,
				Detail:       fmt.Sprintf("cannot verify the complete frozen dependency graph: %s", err),
				Remediation:  "fix inaccessible or invalid dependency manifests, then retry",
			})
		}
		allDeps = append(allDeps, resolved...)
		for child, ps := range parents {
			allParents[child] = append(allParents[child], ps...)
		}
		file := opts.Store.File()
		closure := opts.Store.Closure(workflowfile.KeyFromPath(pw.Path), func(pin parserlock.Pin) bool {
			for _, ref := range pw.Refs {
				if !strings.EqualFold(pin.NWO, ref.NWO()) {
					continue
				}
				if pin.Ref == ref.Ref || (parserlock.IsFullSha(ref.Ref) && strings.EqualFold(lockedSHA(file.Dependencies[pin.String()].Commit), ref.Ref)) {
					return true
				}
			}
			return false
		})
		for _, d := range resolved {
			owner, repo := d.OwnerRepo()
			ref := parserlock.ActionRef{Owner: owner, Repo: repo, Path: d.Path, Ref: d.Ref}
			parent := ""
			if ps := parents[d.Key()]; len(ps) > 0 {
				parent = ps[0]
			}
			checkRef(ref, parent)
			covered := false
			for _, pin := range closure {
				matchesName := strings.EqualFold(pin.Pin.NWO, d.NWO)
				for _, original := range d.OriginalRefs {
					matchesName = matchesName || strings.EqualFold(pin.Pin.NWO, original.NWO())
				}
				if matchesName && strings.EqualFold(lockedSHA(pin.Action.Commit), d.SHA) {
					covered = true
					break
				}
			}
			if !covered {
				wr.Findings = append(wr.Findings, checks.Finding{
					WorkflowPath: pw.Path,
					ActionRef:    &ref,
					Dependency:   &d,
					ParentNWO:    parent,
					Category:     checks.NotPinned,
					Severity:     checks.SeverityError,
					Confidence:   checks.ConfidenceHigh,
					Detail:       fmt.Sprintf("discovered dependency %s@%s is missing from this workflow's recorded lockfile closure", d.FullName(), d.Ref),
					Remediation:  "regenerate the lockfile after reviewing the complete dependency graph",
				})
			}
			if parserlock.IsFullSha(d.Ref) && !strings.EqualFold(d.Ref, d.SHA) {
				wr.Findings = append(wr.Findings, checks.Finding{
					WorkflowPath: pw.Path,
					ActionRef:    &ref,
					Category:     checks.MisleadingSHA,
					Severity:     checks.SeverityError,
					Confidence:   checks.ConfidenceHigh,
					Detail:       fmt.Sprintf("dependency %s@%s resolves to %s instead of the referenced commit", d.FullName(), d.Ref, d.SHA),
					Remediation:  "use the full commit SHA, rather than a tag object or SHA-shaped tag",
				})
			}
		}
	}
	if opts.Resolver != nil {
		checkParity(ctx, opts.Resolver, parsed, opts.Store, report, nil, dep.Dedup(allDeps), allParents, func(checks.Category) bool { return true })
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for i := range report.Workflows {
		for j := range report.Workflows[i].Findings {
			f := &report.Workflows[i].Findings[j]
			if f.Category.IsInconclusive() {
				f.Severity = checks.SeverityError
			}
		}
	}
	return &RunResult{Report: report, Valid: report.IsValid()}, nil
}
