// Copyright © 2026 Michael Shields
// SPDX-License-Identifier: MIT

// Package gitcalver computes calendar-based version strings from git history.
package gitcalver

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

const (
	exitError       = 1
	exitDirty       = 2
	exitWrongBranch = 3

	dateFormat = "20060102"
)

var versionRe = regexp.MustCompile(`^(\d{8})\.([1-9]\d*)$`)

// Options configures a gitcalver invocation.
type Options struct {
	Dir         string
	Target      string // git revision or version string
	Prefix      string
	Dirty       string // non-empty enables dirty mode with this suffix; empty refuses dirty
	NoDirtyHash bool
	Branch      string
	Short       bool
}

// ExitError represents an error with a specific exit code.
type ExitError struct {
	Code    int
	Message string
}

func (e *ExitError) Error() string {
	return e.Message
}

// Run executes gitcalver and returns the output string.
func Run(opts *Options) (string, error) {
	dir := opts.Dir
	if dir == "" {
		dir = "."
	}

	repo, err := git.PlainOpenWithOptions(dir, &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err != nil {
		return "", &ExitError{exitError, "not a git repository"}
	}

	if sc, ok := repo.Storer.(interface {
		Shallow() ([]plumbing.Hash, error)
	}); ok {
		if hashes, err := sc.Shallow(); err == nil && len(hashes) > 0 {
			return "", &ExitError{exitError, "shallow clone detected; full history is required (use git fetch --unshallow)"}
		}
	}

	lookup := opts.Target
	if opts.Prefix != "" && strings.HasPrefix(lookup, opts.Prefix) {
		lookup = strings.TrimPrefix(lookup, opts.Prefix)
	}
	if lookup != "" && versionRe.MatchString(lookup) {
		return reverse(repo, opts, lookup)
	}

	return forward(repo, opts)
}

func forward(repo *git.Repository, opts *Options) (string, error) {
	if opts.Short {
		return "", &ExitError{exitError, "--short is only valid in reverse lookup mode"}
	}

	isHead := opts.Target == "" || opts.Target == "HEAD"
	var targetHash plumbing.Hash

	if isHead {
		headRef, err := repo.Head()
		if err != nil {
			return "", &ExitError{exitError, "no commits in repository"}
		}
		targetHash = headRef.Hash()
	} else {
		hash, err := repo.ResolveRevision(plumbing.Revision(opts.Target))
		if err != nil {
			return "", &ExitError{exitError, "not a gitcalver version or git revision: " + opts.Target}
		}
		targetHash = *hash
	}

	subject := "HEAD"
	if !isHead {
		subject = opts.Target
	}

	branch, err := detectBranch(repo, opts.Branch)
	if err != nil {
		return "", err
	}

	check, err := checkBranchRelation(repo, targetHash, branch, isHead)
	if err != nil {
		return "", &ExitError{exitError, err.Error()}
	}

	versionHash := targetHash
	dirty := false

	switch check.relation {
	case relationOnBranch:
	case relationOffBranch:
		if opts.Dirty == "" {
			return "", &ExitError{
				exitDirty,
				subject + " is not on the default branch (" + branch.name + ") (use --dirty to allow)",
			}
		}
		dirty = true
		versionHash = check.mergeBase
	default: // relationNotTraceable or unexpected value
		return "", &ExitError{exitWrongBranch, subject + " is not traceable to the default branch (" + branch.name + ")"}
	}

	if isHead && !dirty {
		if wt, wtErr := repo.Worktree(); wtErr == nil {
			status, statusErr := wt.Status()
			if statusErr != nil {
				return "", &ExitError{exitError, statusErr.Error()}
			}
			if !status.IsClean() {
				if opts.Dirty == "" {
					return "", &ExitError{exitDirty, "workspace is dirty (use --dirty to allow)"}
				}
				dirty = true
			}
		}
	}

	date, count, err := walkFirstParent(repo, versionHash)
	if err != nil {
		return "", err
	}

	var dirtyStr, hash string
	if dirty {
		dirtyStr = opts.Dirty
		if !opts.NoDirtyHash {
			hash = shortHash(targetHash)
		}
	}

	return formatVersion(opts.Prefix, date, count, dirtyStr, hash), nil
}

func walkFirstParent(repo *git.Repository, startHash plumbing.Hash) (string, int, error) {
	commit, err := repo.CommitObject(startHash)
	if err != nil {
		return "", 0, err
	}

	date := commit.Committer.When.UTC().Format(dateFormat)
	count := 1

	for commit.NumParents() > 0 {
		parent, parentErr := commit.Parent(0)
		if parentErr != nil {
			return "", 0, parentErr
		}

		parentDate := parent.Committer.When.UTC().Format(dateFormat)
		if parentDate != date {
			if parentDate > date {
				return "", 0, &ExitError{
					exitError,
					"committer date goes backwards (found " +
						parentDate + " after " + date + " in history)",
				}
			}
			break
		}

		count++
		commit = parent
	}

	return date, count, nil
}

func reverse(repo *git.Repository, opts *Options, lookup string) (string, error) {
	matches := versionRe.FindStringSubmatch(lookup)
	dateStr := matches[1]
	if _, err := time.Parse(dateFormat, dateStr); err != nil {
		return "", &ExitError{exitError, "invalid date in version: " + opts.Target}
	}
	n, _ := strconv.Atoi(matches[2]) //nolint:errcheck // versionRe guarantees a valid integer

	branch, err := detectBranch(repo, opts.Branch)
	if err != nil {
		return "", err
	}

	commit, err := repo.CommitObject(branch.hash)
	if err != nil {
		return "", err
	}

	var candidates []plumbing.Hash
	for {
		commitDate := commit.Committer.When.UTC().Format(dateFormat)
		if commitDate == dateStr {
			candidates = append(candidates, commit.Hash)
		} else if commitDate < dateStr {
			break
		}

		if commit.NumParents() == 0 {
			break
		}
		parent, parentErr := commit.Parent(0)
		if parentErr != nil {
			return "", parentErr
		}
		commit = parent
	}

	if n > len(candidates) {
		return "", &ExitError{exitError, "version not found: " + opts.Target}
	}

	// N=1 is oldest on that date, N=len is newest; candidates are newest-first.
	targetHash := candidates[len(candidates)-n]

	if opts.Short {
		return shortHash(targetHash), nil
	}
	return targetHash.String(), nil
}
