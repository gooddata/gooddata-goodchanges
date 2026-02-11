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
		// List all merge commits in ancestry order (oldest first with --reverse).
		// NOTE: -1 cannot be combined with --reverse (git applies -1 before reversing),
		// so we get all results and take the first line.
		mergeList, err := Cmd("log", "--ancestry-path", head+".."+branch,
			"--merges", "--first-parent", "--reverse", "--pretty=%H")
		if err != nil || mergeList == "" {
			return base, nil
		}
		mergeCommit := strings.SplitN(mergeList, "\n", 2)[0]
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

// ShowFile returns the content of a file at a specific commit.
// Returns empty string and no error if the file didn't exist at that commit.
func ShowFile(commit string, path string) (string, error) {
	cmd := exec.Command("git", "show", commit+":"+path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// File might not exist at this commit — that's fine
		return "", nil
	}
	return string(out), nil
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
