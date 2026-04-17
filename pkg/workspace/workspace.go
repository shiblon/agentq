// Package workspace manages the agent working environment.
//
// The workspace is a root directory (AGENTQ_WORKSPACE) containing sibling
// repositories. One of those repos is the "self" repo (AGENTQ_SELF), which
// holds agent configuration and system prompts. Agents start up in the
// workspace root and clone target repos as siblings as needed.
//
// Example layout:
//
//	~/code/src/
//	  github.com/shiblon/agent-settings/   <- self (AGENTQ_SELF)
//	    prompts/
//	      coder.txt
//	      reviewer.txt
//	    agents.yaml
//	  github.com/shiblon/agentq/           <- target repo for a session
//	  github.com/shiblon/entroq/           <- another repo, visible as a sibling
package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	EnvRoot = "AGENTQ_WORKSPACE"
	EnvSelf = "AGENTQ_SELF"
)

// Workspace represents an agent's working environment.
type Workspace struct {
	root string // absolute path to the workspace root (e.g. ~/code/src)
	self string // path of the self repo relative to root (e.g. github.com/shiblon/agent-settings)
}

// New creates a Workspace with explicit root and self paths.
// self is relative to root.
func New(root, self string) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("workspace root %q: %w", root, err)
	}
	return &Workspace{root: abs, self: self}, nil
}

// FromEnv creates a Workspace from AGENTQ_WORKSPACE and AGENTQ_SELF.
// Returns nil, false if either variable is unset.
func FromEnv() (*Workspace, bool) {
	root := os.Getenv(EnvRoot)
	self := os.Getenv(EnvSelf)
	if root == "" || self == "" {
		return nil, false
	}
	w, err := New(root, self)
	if err != nil {
		return nil, false
	}
	return w, true
}

// Root returns the absolute workspace root path.
func (w *Workspace) Root() string { return w.root }

// SelfDir returns the absolute path to the self repo.
func (w *Workspace) SelfDir() string {
	return filepath.Join(w.root, filepath.FromSlash(w.self))
}

// RepoDir returns the absolute path to a repo given its path relative to root
// (e.g. "github.com/shiblon/agentq").
func (w *Workspace) RepoDir(repoPath string) string {
	return filepath.Join(w.root, filepath.FromSlash(repoPath))
}

// PromptFor reads the system prompt for the named agent from
// <self>/prompts/<name>.txt. Returns an empty string (no error) if the file
// does not exist, so agents without a dedicated prompt still work.
func (w *Workspace) PromptFor(agentName string) (string, error) {
	path := filepath.Join(w.SelfDir(), "prompts", agentName+".txt")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read prompt for %q: %w", agentName, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// PullSelf pulls the latest changes in the self repo, or clones it if absent.
// selfRemote is the remote URL used only on first clone; if empty, a default
// HTTPS URL is derived from the self path (https://<self>).
func (w *Workspace) PullSelf(ctx context.Context, selfRemote string) error {
	dir := w.SelfDir()
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		remote := selfRemote
		if remote == "" {
			remote = "https://" + w.self
		}
		if err := gitClone(ctx, remote, dir); err != nil {
			return fmt.Errorf("clone self repo: %w", err)
		}
		return nil
	}
	if err := gitPull(ctx, dir); err != nil {
		return fmt.Errorf("pull self repo: %w", err)
	}
	return nil
}

// PrepareRepo ensures the named repo exists under the workspace root, cloning
// it if absent. repoPath is relative to root (e.g. "github.com/shiblon/agentq").
// remoteURL is used only on first clone; if empty, derived as https://<repoPath>.
// Returns the absolute path to the repo directory.
func (w *Workspace) PrepareRepo(ctx context.Context, repoPath, remoteURL string) (string, error) {
	dir := w.RepoDir(repoPath)
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		remote := remoteURL
		if remote == "" {
			remote = "https://" + repoPath
		}
		if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
			return "", fmt.Errorf("create parent dirs for %q: %w", repoPath, err)
		}
		if err := gitClone(ctx, remote, dir); err != nil {
			return "", fmt.Errorf("clone %q: %w", repoPath, err)
		}
	}
	return dir, nil
}

// CommitWork commits any changes in the target repo directory with the given
// message, then pushes. A no-op (no error) if there is nothing to commit.
func (w *Workspace) CommitWork(ctx context.Context, repoDir, message string) error {
	changed, err := gitHasChanges(ctx, repoDir)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if err := gitAdd(ctx, repoDir); err != nil {
		return err
	}
	if err := gitCommit(ctx, repoDir, message); err != nil {
		return err
	}
	return gitPush(ctx, repoDir)
}
