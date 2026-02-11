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
func MergeBase(branch string) (string, error) {
	return Cmd("merge-base", "HEAD", branch)
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
