package pin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/github/gh-actions-lock/internal/dep"
	"github.com/github/gh-actions-lock/internal/lockfile"
	"github.com/github/gh-actions-lock/internal/pipeline/checks"
	"github.com/github/gh-actions-lock/internal/workflowfile"
)

// CommitOptions configures the Commit pass.
type CommitOptions struct {
	// OnProgress is called at each phase boundary. Nil means no progress.
	OnProgress func(phase string)
	// SkipNewWorkflowEntries forces the Commit phase to skip workflows with no existing lockfile entry.
	SkipNewWorkflowEntries bool
	// MutationHeld means the caller holds State.BeginMutation for this run.
	MutationHeld bool
}

// Commit validates all proposed source and lock changes before writing. A write
// failure restores prior source contents; restoration failures are returned too.
func Commit(ctx context.Context, rec *Record, store *lockfile.State, copts *CommitOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if copts == nil || !copts.MutationHeld {
		release, err := store.BeginMutation(ctx)
		if err != nil {
			return err
		}
		defer release()
	}
	progress := func(string) {}
	if copts != nil && copts.OnProgress != nil {
		progress = copts.OnProgress
	}
	if copts != nil && copts.SkipNewWorkflowEntries {
		workflows := rec.Workflows[:0]
		for _, wp := range rec.Workflows {
			if store.HasWorkflow(workflowfile.KeyFromPath(wp.Path)) {
				workflows = append(workflows, wp)
			}
		}
		rec.Workflows = workflows
	}
	if err := validateSourceBaselines(rec.Workflows); err != nil {
		return err
	}
	if err := validateSourceTopology(rec.Workflows); err != nil {
		return err
	}
	if err := validateRequiredRewrites(rec.Workflows); err != nil {
		return err
	}
	files, err := stageSourceChanges(rec.Workflows)
	if err != nil {
		return err
	}

	pinnedByWorkflow := groupPinnedByWorkflow(rec)
	progress("Validating lockfile")
	for _, wp := range rec.Workflows {
		wfKey := workflowfile.KeyFromPath(wp.Path)
		deps := pinnedByWorkflow[wp.Path]
		parentMap := buildParentMap(rec, wp.Path)
		directKeys := buildDirectKeys(rec, wp.Path)
		deps = retainUnresolvablePins(rec, store, wp.Path, deps, directKeys)
		if err := store.Set(ctx, wfKey, deps, parentMap, directKeys); err != nil {
			return fmt.Errorf("updating lockfile for %s: %w", wp.Path, err)
		}
	}
	if err := store.Validate(); err != nil {
		return fmt.Errorf("validating lockfile: %w", err)
	}
	if err := validateCandidateCoverage(rec.Workflows, files, store); err != nil {
		return err
	}
	if err := validateSourceBaselines(rec.Workflows); err != nil {
		return err
	}
	if err := validateSourceTopology(rec.Workflows); err != nil {
		return err
	}
	// Check every baseline before the first write so an editor change cannot
	// be silently overwritten by the staged content.
	for _, file := range files {
		current, err := os.ReadFile(file.path)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, file.original) {
			return fmt.Errorf("%s changed while preparing rewrites; re-run", file.path)
		}
	}
	var written []sourceChange
	rollback := func(cause error) error {
		for i := len(written) - 1; i >= 0; i-- {
			file := written[i]
			if err := replaceSource(file.path, file.original, file.mode); err != nil {
				cause = errors.Join(cause, fmt.Errorf("restoring %s: %w", file.path, err))
			}
		}
		return cause
	}
	progress("Rewriting workflows")
	for _, file := range files {
		if bytes.Equal(file.original, file.content) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return rollback(err)
		}
		if err := replaceSource(file.path, file.content, file.mode); err != nil {
			return rollback(fmt.Errorf("writing %s: %w", file.path, err))
		}
		written = append(written, file)
	}
	progress("Saving lockfile")
	if err := ctx.Err(); err != nil {
		return rollback(err)
	}
	if err := store.Save(); err != nil {
		return rollback(fmt.Errorf("saving lockfile: %w", err))
	}
	rec.Committed = true
	return nil
}

type sourceChange struct {
	path              string
	original, content []byte
	mode              os.FileMode
}

func stageSourceChanges(plans []WorkflowPlan) ([]sourceChange, error) {
	merged := make(map[string]map[string]string)
	for _, wp := range plans {
		for _, source := range append([]string{wp.Path}, wp.SelfActionFiles...) {
			path, err := canonicalSourcePath(source)
			if err != nil {
				return nil, fmt.Errorf("resolving source %s: %w", source, err)
			}
			if merged[path] == nil {
				merged[path] = make(map[string]string)
			}
			for oldUse, newUse := range wp.Rewrites {
				if existing, ok := merged[path][oldUse]; ok && existing != newUse {
					return nil, fmt.Errorf("conflicting rewrites for %s in %s", oldUse, path)
				}
				merged[path][oldUse] = newUse
			}
		}
	}
	paths := make([]string, 0, len(merged))
	for path := range merged {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var files []sourceChange
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		if err := workflowfile.ValidatePathWithinRoot(cwd, path); err != nil {
			return nil, fmt.Errorf("validating source path %s: %w", path, err)
		}
		wf, err := workflowfile.Load(path)
		if err != nil {
			return nil, fmt.Errorf("loading %s: %w", path, err)
		}
		content, _, err := wf.RewriteActionRefs(merged[path])
		if err != nil {
			return nil, fmt.Errorf("rewriting %s: %w", path, err)
		}
		if _, err := workflowfile.Parse(path, content); err != nil {
			return nil, fmt.Errorf("validating rewritten %s: %w", path, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		files = append(files, sourceChange{path: path, original: wf.Content, content: content, mode: info.Mode().Perm()})
	}
	return files, nil
}

func validateSourceBaselines(plans []WorkflowPlan) error {
	for _, wp := range plans {
		for path, original := range wp.SourcePaths {
			current, err := canonicalSourcePath(path)
			if err != nil {
				return fmt.Errorf("checking source identity %s: %w", path, err)
			}
			if current != original {
				return fmt.Errorf("%s changed source identity since dependency discovery; re-run", path)
			}
		}
		for path, original := range wp.SourceContents {
			current, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("checking source baseline %s: %w", path, err)
			}
			if !bytes.Equal(current, original) {
				return fmt.Errorf("%s changed since dependency discovery; re-run", path)
			}
		}
	}
	return nil
}

func validateSourceTopology(plans []WorkflowPlan) error {
	for _, wp := range plans {
		if wp.SourceContents == nil {
			continue
		}
		wf, err := workflowfile.Load(wp.Path)
		if err != nil {
			return err
		}
		refs := wf.ExtractActionRefs()
		current := workflowfile.ScanSelfRepositoryActions(wp.Path, refs.SelfRepositoryActionRefs, refs.LocalWorkflowRefs...)
		if len(current.Errors) > 0 || len(current.SelfRepositoryRefErrs) > 0 || len(current.LocalPaths) > 0 {
			return fmt.Errorf("source dependencies for %s changed since discovery; re-run", wp.Path)
		}
		original := make(map[string]bool, len(wp.SelfActionFiles))
		for _, path := range wp.SelfActionFiles {
			abs, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			original[abs] = true
		}
		if len(current.ActionFiles) != len(original) {
			return fmt.Errorf("source dependencies for %s changed since discovery; re-run", wp.Path)
		}
		for _, path := range current.ActionFiles {
			if !original[path] {
				return fmt.Errorf("source dependencies for %s changed since discovery; re-run", wp.Path)
			}
		}
	}
	return nil
}

func validateCandidateCoverage(plans []WorkflowPlan, files []sourceChange, store *lockfile.State) error {
	candidates := map[string][]byte{}
	for _, file := range files {
		abs, err := canonicalSourcePath(file.path)
		if err != nil {
			return err
		}
		candidates[abs] = file.content
	}
	for _, wp := range plans {
		pw := checks.ParsedWorkflow{Path: wp.Path}
		var err error
		pw.ExistingDeps, err = store.Get(workflowfile.KeyFromPath(wp.Path))
		if err != nil {
			return err
		}
		for _, path := range append([]string{wp.Path}, wp.SelfActionFiles...) {
			abs, err := canonicalSourcePath(path)
			if err != nil {
				return err
			}
			wf, err := workflowfile.Parse(path, candidates[abs])
			if err != nil {
				return err
			}
			scan := wf.ExtractActionRefs()
			if len(scan.LocalPaths) > 0 || len(scan.SelfRepositoryRefErrs) > 0 {
				return fmt.Errorf("source %s has incomplete dependency coverage", path)
			}
			pw.Refs = append(pw.Refs, scan.Refs...)
		}
		_, missing := pw.PartitionRefs()
		if len(missing) > 0 {
			return fmt.Errorf("candidate lockfile does not cover %s in %s", missing[0].FullName()+"@"+missing[0].Ref, wp.Path)
		}
	}
	return nil
}

func canonicalSourcePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func replaceSource(path string, content []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".actions-lock-source-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func isPinOrVerified(r Resolution) bool {
	return r == Pinned || r == Verified
}

func groupPinnedByWorkflow(rec *Record) map[string][]dep.Dependency {
	result := make(map[string][]dep.Dependency)
	for _, e := range rec.Entries {
		if !isPinOrVerified(e.Resolution) {
			continue
		}
		for _, wf := range e.Workflows {
			result[wf] = append(result[wf], dep.Dependency{
				Hostname:    e.Hostname,
				NWO:         e.NWO,
				Ref:         e.Ref,
				SHA:         e.SHA,
				OriginalRef: e.AutoFixedRef,
				RenamedFrom: e.RenamedFrom,
				Branch:      e.OnBranch,
				Tag:         e.Tag,
			})
		}
	}
	return result
}

// retainUnresolvablePins re-adds the workflow's existing on-disk pins for any
// entry that cannot be resolved this run (flagged for investigation or transiently
// unresolvable, e.g. 403/SSO). Without this a co-located re-pin silently
// drops the existing pin.
func retainUnresolvablePins(rec *Record, store *lockfile.State, wfPath string, deps []dep.Dependency, directKeys map[string]bool) []dep.Dependency {
	retain := make(map[string]bool)
	for _, e := range rec.Entries {
		if e.Resolution != Unresolved && e.Resolution != Investigate {
			continue
		}
		for _, wf := range e.Workflows {
			if wf == wfPath {
				retain[strings.ToLower(e.NWO)+"@"+e.Ref] = true
			}
		}
	}
	if len(retain) == 0 {
		return deps
	}
	existing, err := store.Get(workflowfile.KeyFromPath(wfPath))
	if err != nil {
		return deps
	}
	have := make(map[string]bool, len(deps))
	for _, d := range deps {
		have[strings.ToLower(d.NWO)+"@"+d.Ref] = true
	}
	for _, d := range existing {
		k := strings.ToLower(d.NWO) + "@" + d.Ref
		if retain[k] && !have[k] {
			deps = append(deps, d)
			directKeys[d.Key()] = true
			have[k] = true
		}
	}
	return deps
}

func buildParentMap(rec *Record, wfPath string) map[string][]string {
	pm := make(map[string][]string)
	for _, e := range rec.Entries {
		if !isPinOrVerified(e.Resolution) {
			continue
		}
		for _, wf := range e.Workflows {
			if wf != wfPath {
				continue
			}
			if len(e.RequiredBy) > 0 {
				key := e.NWO + "@" + e.Ref
				pm[key] = e.RequiredBy
			}
		}
	}
	return pm
}

func buildDirectKeys(rec *Record, wfPath string) map[string]bool {
	keys := make(map[string]bool)
	for _, e := range rec.Entries {
		if !isPinOrVerified(e.Resolution) || !e.Direct {
			continue
		}
		for _, wf := range e.Workflows {
			if wf == wfPath {
				keys[e.NWO+"@"+e.Ref] = true
			}
		}
	}
	return keys
}

func validateRequiredRewrites(plans []WorkflowPlan) error {
	for i := range plans {
		wp := &plans[i]
		if len(wp.RequiredRewrites) == 0 {
			continue
		}
		found := make(map[string]int)
		wp.RewrittenIn = make(map[string][]string)
		for _, path := range append([]string{wp.Path}, wp.SelfActionFiles...) {
			wf, err := workflowfile.Load(path)
			if err != nil {
				return fmt.Errorf("validating required rewrites in %s: %w", path, err)
			}
			matches, err := wf.ValidateRequiredActionRefRewrites(wp.RequiredRewrites)
			if err != nil {
				return fmt.Errorf("validating required rewrites in %s: %w", path, err)
			}
			for oldUse, n := range matches {
				found[oldUse] += n
				if n > 0 {
					wp.RewrittenIn[oldUse] = append(wp.RewrittenIn[oldUse], path)
				}
			}
		}
		for oldUse := range wp.RequiredRewrites {
			if found[oldUse] == 0 {
				return fmt.Errorf("required action rewrite for %s was not found in writable workflow sources", oldUse)
			}
		}
	}
	return nil
}
