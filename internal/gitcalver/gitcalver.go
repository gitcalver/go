// Copyright © 2026 Michael Shields
// SPDX-License-Identifier: MIT

// Package gitcalver computes calendar-based version strings from git history.
package gitcalver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
)

const (
	exitError             = 1
	exitDirty             = 2
	exitWrongBranch       = 3
	exitIncompleteHistory = 4

	dateFormat = "20060102"
)

var versionRe = regexp.MustCompile(`^(\d{8})\.([1-9]\d*)$`)

var errInvalidGitFile = errors.New("invalid .git file")

// Options configures a gitcalver invocation.
type Options struct {
	Dir         string
	Target      string // git revision or version string
	Prefix      string
	Dirty       string // non-empty enables dirty mode with this suffix; empty refuses dirty
	NoDirtyHash bool
	Branch      string
	Remote      string
	Short       bool
	targetSet   bool
	showVersion bool
}

// ExitError represents an error with a specific exit code.
type ExitError struct {
	Code    int
	Message string
}

func (e *ExitError) Error() string {
	return e.Message
}

type repoState struct {
	repo     *git.Repository
	history  *history
	headHash plumbing.Hash
	worktree *git.Worktree
}

// Run executes gitcalver and returns the output string.
func Run(opts *Options) (string, error) {
	dir := opts.Dir
	if dir == "" {
		dir = "."
	}

	if strings.Contains(opts.Prefix, "\n") {
		return "", &ExitError{exitError, "--prefix must not contain a newline"}
	}

	state, err := validateRepo(dir)
	if err != nil {
		return "", err
	}

	targetSet := opts.targetSet || opts.Target != ""
	lookup := opts.Target
	if targetSet && opts.Prefix != "" && strings.HasPrefix(lookup, opts.Prefix) {
		lookup = strings.TrimPrefix(lookup, opts.Prefix)
	}
	if targetSet && versionRe.MatchString(lookup) {
		if opts.Prefix != "" && lookup == opts.Target {
			return "", &ExitError{
				exitError,
				fmt.Sprintf("version %s is missing required prefix %q", opts.Target, opts.Prefix),
			}
		}
		return reverse(state, opts, lookup)
	}

	return forward(state, opts)
}

func validateRepo(dir string) (*repoState, error) {
	dirs, err := findGitDirs(dir)
	if err != nil {
		return nil, &ExitError{exitError, "not a git repository"}
	}

	repo, err := openRepository(dir, false)
	if err != nil {
		repo, err = openRepository(dir, true)
	}
	if err != nil {
		return nil, &ExitError{exitError, "not a git repository"}
	}

	graftPath := filepath.Join(dirs.commonDir, "info", "grafts")
	if _, statErr := os.Stat(graftPath); !errors.Is(statErr, os.ErrNotExist) {
		return nil, &ExitError{
			exitIncompleteHistory,
			"commit graft file is not supported: " + graftPath,
		}
	}

	headRef, err := repo.Head()
	if err != nil {
		return nil, &ExitError{exitError, "no commits in repository"}
	}

	h, err := newHistory(repo)
	if err != nil {
		return nil, err
	}
	if _, err = h.commit(headRef.Hash()); err != nil {
		return nil, &ExitError{exitIncompleteHistory, "HEAD commit is missing from local history"}
	}

	worktree, _ := repo.Worktree() //nolint:errcheck // nil worktree is the expected bare-repository result

	return &repoState{
		repo:     repo,
		history:  h,
		headHash: headRef.Hash(),
		worktree: worktree,
	}, nil
}

func openRepository(dir string, detect bool) (*git.Repository, error) {
	repo, err := git.PlainOpenWithOptions(dir, &git.PlainOpenOptions{
		DetectDotGit:          detect,
		EnableDotGitCommonDir: true,
	})
	if err == nil || (!errors.Is(err, git.ErrUnknownExtension) &&
		!errors.Is(err, git.ErrUnsupportedExtensionRepositoryFormatVersion)) {
		return repo, err
	}
	return openRepositoryIgnoringPartialClone(dir)
}

type partialCloneStorer struct {
	storage.Storer
}

func (s *partialCloneStorer) Config() (*config.Config, error) {
	cfg, err := s.Storer.Config()
	if err != nil {
		return nil, err
	}
	// Filesystem storage reads a fresh Config value on every call. Removing
	// this one extension only from that in-memory value lets go-git inspect the
	// already-local object database without altering the repository or fetching.
	if cfg.Raw != nil && cfg.Raw.HasSection("extensions") {
		cfg.Raw.Section("extensions").RemoveOption("partialClone")
	}
	return cfg, nil
}

func openRepositoryIgnoringPartialClone(dir string) (*git.Repository, error) {
	dirs, err := findGitDirs(dir)
	if err != nil {
		return nil, err
	}

	dotGitFS := osfs.New(dirs.gitDir)
	repoFS := dotGitFS
	if dirs.commonDir != dirs.gitDir {
		repoFS = dotgit.NewRepositoryFilesystem(dotGitFS, osfs.New(dirs.commonDir))
	}
	storer := &partialCloneStorer{
		Storer: filesystem.NewStorage(repoFS, cache.NewObjectLRUDefault()),
	}
	if dirs.bare {
		return git.Open(storer, nil)
	}
	return git.Open(storer, osfs.New(dirs.worktreeDir))
}

func forward(state *repoState, opts *Options) (string, error) {
	if opts.Short {
		return "", &ExitError{exitError, "--short is only valid in reverse lookup mode"}
	}

	targetSet := opts.targetSet || opts.Target != ""
	targetHash := state.headHash
	if targetSet {
		resolved, err := resolveCommitRevision(state.repo, opts.Target)
		if err != nil {
			return "", &ExitError{
				exitError,
				"not a gitcalver version or git revision: " + opts.Target,
			}
		}
		targetHash = resolved
	}

	remote := opts.Remote
	if remote == "" {
		remote = defaultRemote
	}
	branch, err := detectBranch(state.repo, opts.Branch, remote)
	if err != nil {
		return "", err
	}
	if _, err = state.history.commit(branch.hash); err != nil {
		return "", &ExitError{
			exitIncompleteHistory,
			"selected branch tip is missing from local history: " + branch.name,
		}
	}

	anchor, found, err := findReachableBranchAnchor(state.history, targetHash, branch.hash)
	if err != nil {
		return "", err
	}
	if !found {
		subject := "HEAD"
		if targetSet {
			subject = opts.Target
		}
		return "", &ExitError{
			exitWrongBranch,
			"cannot trace " + subject + " to the default branch (" + branch.name + ")",
		}
	}

	offBranch := anchor != targetHash
	workspaceDirty := false
	if !targetSet && !offBranch && state.worktree != nil {
		status, statusErr := state.worktree.Status()
		if statusErr != nil {
			return "", &ExitError{exitIncompleteHistory, "local history cannot prove workspace state"}
		}
		workspaceDirty = !status.IsClean()
	}

	dirty := offBranch || workspaceDirty
	if dirty && opts.Dirty == "" {
		if offBranch {
			subject := "HEAD"
			if targetSet {
				subject = opts.Target
			}
			return "", &ExitError{
				exitDirty,
				subject + " is off the default branch (" + branch.name +
					"); use --dirty to produce a divergent version",
			}
		}
		return "", &ExitError{exitDirty, "workspace is dirty; use --dirty to allow"}
	}

	date, count, err := walkFirstParent(state.history, anchor)
	if err != nil {
		return "", err
	}

	var dirtyStr, hash string
	if dirty {
		dirtyStr = opts.Dirty
		if !opts.NoDirtyHash {
			hash = objectIDPrefix(targetHash)
		}
	}

	return formatVersion(opts.Prefix, date, count, dirtyStr, hash), nil
}

func walkFirstParent(history *history, startHash plumbing.Hash) (string, int, error) {
	commit, err := history.commit(startHash)
	if err != nil {
		return "", 0, &ExitError{exitIncompleteHistory, "local history ended inside the target date block"}
	}

	date := commit.Committer.When.UTC().Format(dateFormat)
	count := 1

	for {
		parent, ok, parentErr := history.firstParent(commit)
		if parentErr != nil {
			return "", 0, &ExitError{
				exitIncompleteHistory,
				"local history ended inside the " + date + " date block",
			}
		}
		if !ok {
			return date, count, nil
		}

		parentDate := parent.Committer.When.UTC().Format(dateFormat)
		if parentDate != date {
			if parentDate > date {
				return "", 0, dateWentBackwards(parentDate, date)
			}
			return date, count, nil
		}

		count++
		commit = parent
	}
}

func reverse(state *repoState, opts *Options, lookup string) (string, error) {
	matches := versionRe.FindStringSubmatch(lookup)
	dateStr := matches[1]
	if _, err := time.Parse(dateFormat, dateStr); err != nil {
		return "", &ExitError{exitError, "invalid date in version: " + opts.Target}
	}
	n, err := strconv.Atoi(matches[2])
	if err != nil {
		return "", &ExitError{exitError, "invalid count in version: " + opts.Target}
	}

	remote := opts.Remote
	if remote == "" {
		remote = defaultRemote
	}
	branch, err := detectBranch(state.repo, opts.Branch, remote)
	if err != nil {
		return "", err
	}
	commit, err := state.history.commit(branch.hash)
	if err != nil {
		return "", &ExitError{
			exitIncompleteHistory,
			"selected branch tip is missing from local history: " + branch.name,
		}
	}

	var candidates []plumbing.Hash
	var newerDate string
	for {
		commitDate := commit.Committer.When.UTC().Format(dateFormat)
		if newerDate != "" && commitDate > newerDate {
			return "", dateWentBackwards(commitDate, newerDate)
		}
		newerDate = commitDate

		if commitDate == dateStr {
			candidates = append(candidates, commit.Hash)
		} else if commitDate < dateStr {
			break
		}

		parent, ok, parentErr := state.history.firstParent(commit)
		if parentErr != nil {
			return "", &ExitError{
				exitIncompleteHistory,
				"local history ended before version could be proved",
			}
		}
		if !ok {
			break
		}
		commit = parent
	}

	targetHash, err := selectReverseCandidate(candidates, n, opts.Target)
	if err != nil {
		return "", err
	}
	if opts.Short {
		return objectIDPrefix(targetHash), nil
	}
	return targetHash.String(), nil
}

func selectReverseCandidate(
	candidates []plumbing.Hash, n int, version string,
) (plumbing.Hash, error) {
	if n > len(candidates) {
		return plumbing.ZeroHash, &ExitError{exitError, "version not found: " + version}
	}

	// N=1 is oldest on that date; candidates are newest-first.
	return candidates[len(candidates)-n], nil
}

func dateWentBackwards(older, newer string) *ExitError {
	return &ExitError{
		exitError,
		"committer date not monotonic: older commit dated " + older +
			" has a later date than newer commit dated " + newer,
	}
}

func resolveCommitRevision(repo *git.Repository, revision string) (plumbing.Hash, error) {
	hash, err := repo.ResolveRevision(plumbing.Revision(revision))
	if err != nil {
		return plumbing.ZeroHash, err
	}
	// go-git ResolveRevision guarantees a peeled commit hash.
	return *hash, nil
}

type gitDirectories struct {
	gitDir      string
	commonDir   string
	worktreeDir string
	bare        bool
}

func findGitDirs(dir string) (gitDirectories, error) {
	start, _ := filepath.Abs(dir) //nolint:errcheck // all supported repository paths are local filesystem paths
	if info, statErr := os.Stat(start); statErr != nil || !info.IsDir() {
		return gitDirectories{}, os.ErrNotExist
	}

	for current := start; ; current = filepath.Dir(current) {
		gitDir, found, findErr := gitDirAt(current)
		if findErr != nil {
			return gitDirectories{}, findErr
		}
		if found {
			commonDir := gitDir
			commonData, readErr := os.ReadFile(filepath.Join(gitDir, "commondir")) //nolint:gosec // repository metadata path
			if readErr == nil {
				common := strings.TrimSpace(string(commonData))
				if !filepath.IsAbs(common) {
					common = filepath.Join(gitDir, common)
				}
				commonDir = filepath.Clean(common)
			} else if !errors.Is(readErr, os.ErrNotExist) {
				return gitDirectories{}, readErr
			}
			_, dotGitErr := os.Stat(filepath.Join(current, ".git"))
			bare := errors.Is(dotGitErr, os.ErrNotExist) && gitDir == current
			return gitDirectories{
				gitDir:      filepath.Clean(gitDir),
				commonDir:   commonDir,
				worktreeDir: current,
				bare:        bare,
			}, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return gitDirectories{}, os.ErrNotExist
}

func gitDirAt(dir string) (string, bool, error) {
	dotGit := filepath.Join(dir, ".git")
	info, err := os.Stat(dotGit)
	if err == nil {
		if info.IsDir() {
			return dotGit, true, nil
		}
		data, readErr := os.ReadFile(dotGit) //nolint:gosec // repository metadata path
		if readErr != nil {
			return "", false, readErr
		}
		value, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
		if !ok {
			return "", false, errInvalidGitFile
		}
		gitDir := strings.TrimSpace(value)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(dir, gitDir)
		}
		return filepath.Clean(gitDir), true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}

	if _, headErr := os.Stat(filepath.Join(dir, "HEAD")); headErr == nil {
		if objects, objectsErr := os.Stat(filepath.Join(dir, "objects")); objectsErr == nil && objects.IsDir() {
			return dir, true, nil
		}
	}
	return "", false, nil
}
