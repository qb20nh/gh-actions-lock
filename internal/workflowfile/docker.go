package workflowfile

import (
	"strings"

	parserlock "github.com/github/actions-lockfile/go/pkg/lockfile"
)

// IsImmutableDockerRef accepts only container action refs with full SHA-256 digests.
func IsImmutableDockerRef(ref string) bool {
	if !strings.HasPrefix(ref, "docker://") {
		return false
	}
	image, digest, found := strings.Cut(strings.TrimPrefix(ref, "docker://"), "@sha256:")
	return found && image != "" && len(digest) == 64 && parserlock.IsFullSha(digest)
}
