package workspace

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// git runs a git command in dir and returns combined output on failure.
func git(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out.String())
	}
	return nil
}

// gitOutput runs a git command and returns stdout.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

func gitClone(ctx context.Context, remote, dir string) error {
	// Clone into parent, then the last path component becomes the dir name.
	// exec.Command with explicit dest avoids ambiguity.
	cmd := exec.CommandContext(ctx, "git", "clone", remote, dir)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone %q -> %q: %w\n%s", remote, dir, err, out.String())
	}
	return nil
}

func gitPull(ctx context.Context, dir string) error {
	return git(ctx, dir, "pull", "--ff-only")
}

func gitAdd(ctx context.Context, dir string) error {
	return git(ctx, dir, "add", "-A")
}

func gitCommit(ctx context.Context, dir, message string) error {
	return git(ctx, dir, "commit", "-m", message)
}

func gitPush(ctx context.Context, dir string) error {
	return git(ctx, dir, "push")
}

// gitHasChanges reports whether the repo has staged or unstaged changes.
func gitHasChanges(ctx context.Context, dir string) (bool, error) {
	out, err := gitOutput(ctx, dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// gitUntrackedFiles returns the paths of untracked files relative to dir.
func gitUntrackedFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := gitOutput(ctx, dir, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// GitCurrentRef returns the current HEAD commit SHA (short).
func GitCurrentRef(ctx context.Context, dir string) (string, error) {
	return gitOutput(ctx, dir, "rev-parse", "--short", "HEAD")
}

// GitCreateSessionBranch creates and checks out a branch named session/<id>,
// branching from the current HEAD. A no-op if the branch already exists.
func GitCreateSessionBranch(ctx context.Context, dir, sessionID string) error {
	branch := "session/" + sessionID
	// Check if branch exists already.
	out, _ := gitOutput(ctx, dir, "branch", "--list", branch)
	if strings.TrimSpace(out) != "" {
		return git(ctx, dir, "checkout", branch)
	}
	return git(ctx, dir, "checkout", "-b", branch)
}

// GitMergeFrom merges the named branch (or ref) into the current branch.
// Typically used to pull prompt updates from main into a session branch.
func GitMergeFrom(ctx context.Context, dir, ref string) error {
	return git(ctx, dir, "merge", "--no-edit", ref)
}
