// Package gitops wires go-git into the workspace directory so the GUI can
// offer real git collaboration — status, commit history, commit+push —
// without shelling out to the git binary. This is the "in-app git" locked
// decision (docs/03-tech-stack.md) that had never actually been implemented:
// the file store was designed to be git-friendly (one YAML file per
// resource), but nothing in the app ever called go-git until now.
package gitops

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

type FileStatus struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

type Status struct {
	IsRepo    bool         `json:"isRepo"`
	Branch    string       `json:"branch"`
	Clean     bool         `json:"clean"`
	HasRemote bool         `json:"hasRemote"`
	Files     []FileStatus `json:"files"`
}

type Commit struct {
	Hash    string `json:"hash"`
	Message string `json:"message"`
	Author  string `json:"author"`
	Date    string `json:"date"`
}

// EnsureRepo opens the git repo at dir, initializing one (plus a minimal
// .gitignore for OS cruft) if it doesn't exist yet. Secrets never reach
// these YAML files in the first place (they live in the OS keychain — see
// docs/02-architecture.md §7), so there is nothing sensitive that needs
// excluding here; the only reason to auto-init is so "git collaboration"
// is zero-config from the user's first commit, not something they have to
// set up outside the app first.
func EnsureRepo(dir string) (*git.Repository, error) {
	repo, err := git.PlainOpen(dir)
	if err == nil {
		return repo, nil
	}
	if !errors.Is(err, git.ErrRepositoryNotExists) {
		return nil, fmt.Errorf("open repo: %w", err)
	}

	repo, err = git.PlainInit(dir, false)
	if err != nil {
		return nil, fmt.Errorf("init repo: %w", err)
	}
	gitignore := filepath.Join(dir, ".gitignore")
	if _, statErr := os.Stat(gitignore); errors.Is(statErr, os.ErrNotExist) {
		_ = os.WriteFile(gitignore, []byte(".DS_Store\n"), 0o644)
	}
	return repo, nil
}

// GetStatus reports the current branch, dirty/clean state, and per-file
// changes (staged or not — the frontend doesn't distinguish, since
// CommitAndPush always stages everything before committing).
func GetStatus(dir string) (Status, error) {
	repo, err := EnsureRepo(dir)
	if err != nil {
		return Status{}, err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return Status{}, fmt.Errorf("worktree: %w", err)
	}
	wtStatus, err := wt.Status()
	if err != nil {
		return Status{}, fmt.Errorf("status: %w", err)
	}

	files := make([]FileStatus, 0, len(wtStatus))
	for path, s := range wtStatus {
		files = append(files, FileStatus{Path: path, Status: statusLabel(s)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	_, remoteErr := repo.Remote("origin")

	return Status{
		IsRepo:    true,
		Branch:    currentBranch(repo),
		Clean:     wtStatus.IsClean(),
		HasRemote: remoteErr == nil,
		Files:     files,
	}, nil
}

// currentBranch handles the pre-first-commit case too (HEAD is a symbolic
// ref to a branch that doesn't exist yet as an actual commit), where
// repo.Head() alone would just return an error.
func currentBranch(repo *git.Repository) string {
	if head, err := repo.Head(); err == nil && head.Name().IsBranch() {
		return head.Name().Short()
	}
	if ref, err := repo.Reference(plumbing.HEAD, false); err == nil && ref.Type() == plumbing.SymbolicReference {
		return ref.Target().Short()
	}
	return "main"
}

func statusLabel(s *git.FileStatus) string {
	// A file can have a different staged vs. unstaged code; prefer the
	// staged (index) one since that's the change that will actually be
	// committed the next time CommitAndPush stages everything.
	code := s.Staging
	if code == git.Unmodified {
		code = s.Worktree
	}
	switch code {
	case git.Added:
		return "added"
	case git.Modified:
		return "modified"
	case git.Deleted:
		return "deleted"
	case git.Renamed:
		return "renamed"
	case git.Untracked:
		return "untracked"
	default:
		return "modified"
	}
}

// GetLog returns the last `limit` commits reachable from HEAD, newest
// first. An empty (no-commits-yet) repo is not an error — it's the normal
// state right after EnsureRepo's first init.
func GetLog(dir string, limit int) ([]Commit, error) {
	repo, err := EnsureRepo(dir)
	if err != nil {
		return nil, err
	}
	head, err := repo.Head()
	if err != nil {
		return nil, nil
	}
	iter, err := repo.Log(&git.LogOptions{From: head.Hash()})
	if err != nil {
		return nil, fmt.Errorf("log: %w", err)
	}
	defer iter.Close()

	commits := make([]Commit, 0, limit)
	err = iter.ForEach(func(c *object.Commit) error {
		if len(commits) >= limit {
			return storer.ErrStop
		}
		commits = append(commits, Commit{
			Hash:    c.Hash.String()[:8],
			Message: strings.TrimSpace(c.Message),
			Author:  c.Author.Name,
			Date:    c.Author.When.UTC().Format(time.RFC3339),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk log: %w", err)
	}
	return commits, nil
}

// CommitAndPush stages every change in dir, commits with message, and
// pushes to origin if one is configured. A push failure does NOT undo the
// local commit — matching plain `git commit && git push`, where a network
// blip shouldn't lose work already captured locally; the caller can retry
// the push once connectivity is back. Returns whether a push actually
// happened (false + nil error means "committed locally, no remote
// configured" — not a failure).
func CommitAndPush(dir, message, authorName, authorEmail string) (pushed bool, err error) {
	if strings.TrimSpace(message) == "" {
		return false, errors.New("commit message is required")
	}

	repo, err := EnsureRepo(dir)
	if err != nil {
		return false, err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return false, fmt.Errorf("worktree: %w", err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return false, fmt.Errorf("stage changes: %w", err)
	}

	st, err := wt.Status()
	if err == nil && st.IsClean() {
		return false, errors.New("nothing to commit")
	}

	if authorName == "" {
		authorName = "AUK"
	}
	if authorEmail == "" {
		authorEmail = "auk@localhost"
	}
	if _, err := wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{Name: authorName, Email: authorEmail, When: time.Now()},
	}); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}

	if _, remoteErr := repo.Remote("origin"); remoteErr != nil {
		return false, nil
	}

	if err := repo.Push(&git.PushOptions{}); err != nil {
		if errors.Is(err, git.NoErrAlreadyUpToDate) {
			return true, nil
		}
		return false, fmt.Errorf("committed locally but push failed: %w", err)
	}
	return true, nil
}
