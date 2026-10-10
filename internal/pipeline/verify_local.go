package pipeline

import (
	"context"
	"fmt"
	"strings"

	parserlock "github.com/github/actions-lockfile/go/pkg/lockfile"
	"github.com/github/gh-actions-lock/internal/dep"
	"github.com/github/gh-actions-lock/internal/lockfile"
	"github.com/github/gh-actions-lock/internal/pipeline/checks"
	"github.com/github/gh-actions-lock/internal/workflowfile"
)

// VerifyLocalCoverage performs a zero-network static check: every action ref
// in the parsed workflows must have a matching lockfile entry. Returns a
// Report with NotPinned findings for any uncovered refs, suitable for rendering
// with the standard terminal or JSON formatters.
func VerifyLocalCoverage(parsed []checks.ParsedWorkflow, store *lockfile.State) *checks.Report {
	lf := store.File()
	results := make([]checks.WorkflowReport, 0, len(parsed))

	for _, pw := range parsed {
		wr := checks.WorkflowReport{
			Path:       pw.Path,
			ActionRefs: pw.Refs,
			RewriteRefs: func() []parserlock.ActionRef {
				if pw.RewriteRefs != nil {
					return pw.RewriteRefs
				}
				return pw.Refs
			}(),
			SelfActionFiles: pw.SelfActionFiles,
			SelfActionRefs:  pw.SelfActionRefs,
			Deps:            pw.ExistingDeps,
		}
		populateLocalInventory(&wr, pw, store)

		if pw.LoadErr != nil {
			wr.Findings = append(wr.Findings, checks.Finding{
				WorkflowPath: pw.Path,
				Category:     checks.NotPinned,
				Severity:     checks.SeverityError,
				Confidence:   checks.ConfidenceHigh,
				Detail:       fmt.Sprintf("could not parse workflow: %v", pw.LoadErr),
			})
			results = append(results, wr)
			continue
		}

		if pw.DepsErr != nil {
			wr.Findings = append(wr.Findings, checks.Finding{
				WorkflowPath: pw.Path,
				Category:     checks.NotPinned,
				Severity:     checks.SeverityError,
				Confidence:   checks.ConfidenceHigh,
				Remediable:   true,
				Detail:       fmt.Sprintf("failed to read dependencies: %s", pw.DepsErr),
				Remediation:  "fix or regenerate the dependencies: section with `gh actions-lock`",
			})
			results = append(results, wr)
			continue
		}

		// `$/…@ref` is invalid regardless of lockfile coverage — a self
		// reference always resolves to the running ref. Surface it in the
		// offline check too; it needs no network to detect.
		if len(pw.SelfRepositoryRefErrs) > 0 {
			wr.Findings = append(wr.Findings, checks.Finding{
				WorkflowPath: pw.Path,
				Category:     checks.InvalidSelfRepositoryRef,
				Severity:     checks.SeverityError,
				Confidence:   checks.ConfidenceHigh,
				Detail:       fmt.Sprintf("self repository actions must not carry an @ref: %s", strings.Join(pw.SelfRepositoryRefErrs, ", ")),
				Remediation:  "drop the `@ref` suffix — `$/…` always resolves to the running ref",
			})
		}
		for _, resolutionErr := range pw.SelfRepositoryResolutionErrs {
			wr.Findings = append(wr.Findings, checks.Finding{
				WorkflowPath: pw.Path,
				Category:     checks.InvalidSelfRepositoryRef,
				Severity:     checks.SeverityError,
				Confidence:   checks.ConfidenceHigh,
				Detail:       resolutionErr,
				Remediation:  "fix the `$/…` action path so its dependencies can be inspected",
			})
		}
		if len(pw.LocalPaths) > 0 {
			wr.Findings = append(wr.Findings, checks.Finding{
				WorkflowPath: pw.Path,
				Category:     checks.LocalAction,
				Severity:     checks.SeverityError,
				Confidence:   checks.ConfidenceHigh,
				Detail:       "cannot verify lockfile coverage for local path actions: " + strings.Join(pw.LocalPaths, ", "),
				Remediation:  "inspect checkout and generated-file behavior before opting into --migrate-local-actions, or use remote action references",
			})
		}

		// Use RunChecks with a nil resolver to get structural-only findings
		// (NotPinned, ShaAsRef, RefChanged, Stale). No network calls.
		findings := checks.RunChecks(context.Background(), pw, lf, nil)
		wr.Findings = append(wr.Findings, findings...)
		results = append(results, wr)
	}

	return &checks.Report{Workflows: results}
}

func populateLocalInventory(wr *checks.WorkflowReport, pw checks.ParsedWorkflow, store *lockfile.State) {
	seen := make(map[string]bool)
	for _, ref := range pw.Refs {
		d := dep.Dependency{NWO: ref.NWO(), Path: ref.Path, Ref: ref.Ref}
		for _, locked := range pw.ExistingDeps {
			if strings.EqualFold(locked.NWO, d.NWO) && (locked.Ref == ref.Ref || strings.EqualFold(locked.SHA, ref.Ref)) {
				d = locked
				d.Path, d.Ref = ref.Path, ref.Ref
				break
			}
		}
		if key := foldKey(d.Key()); !seen[key] {
			seen[key] = true
			wr.Inventory = append(wr.Inventory, checks.InventoryEntry{Dep: d, File: pw.Path, Direct: true})
		}
	}
	file := store.File()
	closure := store.Closure(workflowfile.KeyFromPath(pw.Path), func(pin parserlock.Pin) bool {
		for _, ref := range pw.Refs {
			if strings.EqualFold(pin.NWO, ref.NWO()) && (pin.Ref == ref.Ref || strings.EqualFold(lockedSHA(file.Dependencies[pin.String()].Commit), ref.Ref)) {
				return true
			}
		}
		return false
	})
	byKey := make(map[string]dep.Dependency)
	for _, d := range store.AllDeps() {
		byKey[foldKey(d.Key())] = d
	}
	for _, pin := range closure {
		key := foldKey(pin.Pin.NWO + "@" + pin.Pin.Ref)
		if pin.Parent == "" || seen[key] {
			continue
		}
		seen[key] = true
		wr.Inventory = append(wr.Inventory, checks.InventoryEntry{Dep: byKey[key], File: pw.Path, Parents: []string{pin.Parent}})
	}
}
