package git

import (
	"fmt"
	"os/exec"
	"strings"
)

func Cmd(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// MergeBase returns the merge-base commit between HEAD and the given branch.
// If HEAD is already merged into the branch (e.g. testing an already-merged PR),
// it finds the merge commit on the branch's first-parent line and uses its
// first parent to compute the correct divergence point.
func MergeBase(branch string) (string, error) {
	base, err := Cmd("merge-base", "HEAD", branch)
	if err != nil {
		return "", err
	}

	head, err := Cmd("rev-parse", "HEAD")
	if err != nil {
		return base, nil
	}

	// If merge-base == HEAD, HEAD is an ancestor of the branch (already merged).
	// Find the merge commit that brought HEAD into the branch, then use its
	// first parent to compute the real divergence point.
	if base == head {
		mergeCommit, err := Cmd("log", "--ancestry-path", head+".."+branch,
			"--merges", "--first-parent", "--reverse", "--pretty=%H", "-1")
		if err != nil || mergeCommit == "" {
			return base, nil
		}
		firstParent, err := Cmd("rev-parse", mergeCommit+"^1")
		if err != nil {
			return base, nil
		}
		realBase, err := Cmd("merge-base", head, firstParent)
		if err != nil {
			return base, nil
		}
		return realBase, nil
	}

	return base, nil
}

// DiffSince returns the unified diff of all changes since the given commit.
func DiffSince(commit string) (string, error) {
	return Cmd("diff", commit)
}

// DiffSincePath returns the unified diff for a specific path since the given commit.
func DiffSincePath(commit string, path string) (string, error) {
	return Cmd("diff", commit, "--", path)
}

// ChangedFilesSince returns the list of changed file paths since the given commit.
func ChangedFilesSince(commit string) ([]string, error) {
	raw, err := Cmd("diff", "--name-only", commit)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\n"), nil
}
