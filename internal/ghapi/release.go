package ghapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// ReleaseByTag looks up publication metadata without relying on the recent
// releases page. Draft and unpublished releases cannot establish release age.
func (c *Client) ReleaseByTag(ctx context.Context, owner, repo, tag string) (RepoRelease, error) {
	c, err := c.ForRepo(ctx, owner, repo)
	if err != nil {
		return RepoRelease{}, err
	}
	path := fmt.Sprintf("repos/%s/%s/releases/tags/%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(tag))
	var release struct {
		TagName     string `json:"tag_name"`
		PublishedAt string `json:"published_at"`
		Immutable   bool   `json:"immutable"`
		Draft       bool   `json:"draft"`
	}
	if err := c.rest.DoWithContext(ctx, http.MethodGet, path, nil, &release); err != nil {
		if IsSAMLEnforcement(err) && c.SSOFallbackEligible(ctx, owner) {
			if anonErr := c.anonGet(ctx, path, &release); anonErr != nil {
				return RepoRelease{}, fmt.Errorf("anonymous fallback fetching release %s/%s@%s: %w", owner, repo, tag, anonErr)
			}
		} else {
			return RepoRelease{}, fmt.Errorf("fetching release %s/%s@%s: %w", owner, repo, tag, c.ssoErr(owner, err))
		}
	}
	if release.TagName != tag {
		return RepoRelease{}, fmt.Errorf("release %s/%s@%s returned tag %q", owner, repo, tag, release.TagName)
	}
	if release.Draft || release.PublishedAt == "" {
		return RepoRelease{}, fmt.Errorf("release %s/%s@%s has no published release date", owner, repo, tag)
	}
	return RepoRelease{
		TagName:     release.TagName,
		PublishedAt: release.PublishedAt,
		Immutable:   release.Immutable,
	}, nil
}
