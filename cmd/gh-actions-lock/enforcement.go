package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	parserlock "github.com/github/actions-lockfile/go/pkg/lockfile"
	"github.com/github/gh-actions-lock/internal/ghapi"
	"github.com/github/gh-actions-lock/internal/lockfile"
	"github.com/github/gh-actions-lock/internal/pin"
	"github.com/github/gh-actions-lock/internal/tag"
)

type cooldownCommit struct {
	host string
	nwo  string
	sha  string
}

func cooldownCommitKey(host, nwo, sha, home string) cooldownCommit {
	if host == "" {
		host = home
	}
	return cooldownCommit{strings.ToLower(host), strings.ToLower(nwo), strings.ToLower(sha)}
}

// validateCooldownCandidates runs against the original store before staging
// writes. Existing commits remain usable even when release metadata is absent.
func validateCooldownCandidates(ctx context.Context, record *pin.Record, store *lockfile.State, client *ghapi.Client, cfg tag.CooldownConfig) error {
	if record == nil || !cooldownEnabled(cfg) {
		return nil
	}
	home := "github.com"
	if client != nil {
		home = client.Hostname
	}
	adopted := map[cooldownCommit]bool{}
	var recorded []cooldownRecordedRelease
	if store != nil {
		for _, d := range store.AllDeps() {
			key := cooldownCommitKey(d.Hostname, d.NWO, d.SHA, home)
			adopted[key] = true
			recorded = append(recorded, cooldownRecordedRelease{key, d.Ref, d.Tag})
		}
	}
	checked := map[cooldownCommit]bool{}
	now := time.Now()
	for _, e := range record.Entries {
		if e.Resolution != pin.Pinned && e.Resolution != pin.Verified {
			continue
		}
		owner, repo, ok := parserlock.SplitNWO(e.NWO)
		if !ok {
			return fmt.Errorf("cooldown: invalid repository %q", e.NWO)
		}
		days := cfg.CooldownDays(owner, repo)
		if days <= 0 {
			continue
		}
		key := cooldownCommitKey(e.Hostname, e.NWO, e.SHA, home)
		if adopted[key] || checked[key] {
			continue
		}
		unchangedRename := false
		for _, old := range e.RenamedFrom {
			if previous, ok := parserlock.ParsePin(old); ok && adopted[cooldownCommitKey(e.Hostname, previous.NWO, e.SHA, home)] {
				// Plan only records RenamedFrom after verifying repository identity.
				unchangedRename = true
				break
			}
		}
		if unchangedRename {
			continue
		}
		if client == nil {
			return fmt.Errorf("cooldown for %s@%s: release age is unknown (no API client)", e.NWO, e.Ref)
		}
		if e.SHA == "" {
			return fmt.Errorf("cooldown for %s@%s: candidate commit is unknown", e.NWO, e.Ref)
		}
		routed, err := client.ForRepo(ctx, owner, repo)
		if err != nil {
			return fmt.Errorf("cooldown for %s@%s: %w", e.NWO, e.Ref, err)
		}
		if !strings.EqualFold(routed.Hostname, key.host) {
			return fmt.Errorf("cooldown for %s@%s: repository resolved on %s, expected %s", e.NWO, e.Ref, routed.Hostname, key.host)
		}
		releaseTag := cooldownReleaseTag(e)
		if releaseTag == "" {
			return fmt.Errorf("cooldown for %s@%s: release age is unknown (no exact release tag)", e.NWO, e.Ref)
		}
		for _, previous := range recorded {
			if previous.commit.host == key.host && previous.commit.nwo == key.nwo && previous.commit.sha != key.sha && (previous.ref == releaseTag || previous.tag == releaseTag) {
				return fmt.Errorf("cooldown for %s@%s: release %s was retargeted from a recorded commit; its publication date does not establish the new candidate's age", e.NWO, e.Ref, releaseTag)
			}
		}
		release, err := routed.ReleaseByTag(ctx, owner, repo, releaseTag)
		if err != nil {
			return fmt.Errorf("cooldown for %s@%s: release age is unknown: %w", e.NWO, e.Ref, err)
		}
		if err := validateCooldownPublication(release.PublishedAt, days, now); err != nil {
			return fmt.Errorf("cooldown for %s@%s (release %s): %w", e.NWO, e.Ref, releaseTag, err)
		}
		sha, err := routed.CommitSHA(ctx, owner, repo, "refs/tags/"+releaseTag)
		if err != nil {
			return fmt.Errorf("cooldown for %s@%s: verifying release commit: %w", e.NWO, e.Ref, err)
		}
		if !strings.EqualFold(sha, e.SHA) {
			return fmt.Errorf("cooldown for %s@%s: release %s resolves to %s, not candidate %s", e.NWO, e.Ref, releaseTag, sha, e.SHA)
		}
		checked[key] = true
	}
	return nil
}

type cooldownRecordedRelease struct {
	commit cooldownCommit
	ref    string
	tag    string
}

func cooldownEnabled(cfg tag.CooldownConfig) bool {
	if cfg.DefaultDays > 0 {
		return true
	}
	for _, days := range cfg.RepoOverrides {
		if days > 0 {
			return true
		}
	}
	return false
}

func cooldownReleaseTag(e pin.Entry) string {
	if sv, ok := parserlock.ParseSemVer(e.Ref); ok && sv.IsFull() {
		return e.Ref
	}
	if e.Tag != "" && !parserlock.IsFullSha(e.Tag) {
		return e.Tag
	}
	return ""
}

func validateCooldownPublication(publication string, days int, now time.Time) error {
	published, err := time.Parse(time.RFC3339, publication)
	if err != nil {
		return fmt.Errorf("release age is unknown (invalid published_at %q): %w", publication, err)
	}
	// Division avoids overflowing a duration for a large configured day count.
	if age := now.Sub(published); age < 0 || int64(age/(24*time.Hour)) < int64(days) {
		return fmt.Errorf("published %s; requires at least %d days before adopting this commit", published.Format(time.RFC3339), days)
	}
	return nil
}
