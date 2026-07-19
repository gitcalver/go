// Copyright © 2026 Michael Shields
// SPDX-License-Identifier: MIT

package gitcalver

import (
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

type branchInfo struct {
	name string
	hash plumbing.Hash
}

const defaultRemote = "origin"

func detectBranch(repo *git.Repository, override string, remotes ...string) (branchInfo, error) {
	remote := defaultRemote
	if len(remotes) > 0 {
		remote = remotes[0]
	}
	if remote == "" {
		return branchInfo{}, &ExitError{exitError, "remote requires a non-empty string"}
	}
	if override != "" {
		if strings.HasPrefix(override, "refs/") {
			if info, ok := resolveFullRef(repo, override); ok {
				return info, nil
			}
		} else if info, ok := resolveBranch(repo, override, remote); ok {
			return info, nil
		}
		return branchInfo{}, &ExitError{exitError, "branch not found: " + override}
	}

	remotePrefix := "refs/remotes/" + remote + "/"
	ref, err := repo.Reference(plumbing.ReferenceName(remotePrefix+"HEAD"), false)
	if err == nil && ref.Type() == plumbing.SymbolicReference {
		target := ref.Target().String()
		if name, ok := strings.CutPrefix(target, remotePrefix); ok {
			if info, found := resolveBranch(repo, name, remote); found {
				return info, nil
			}
		}
	}

	for _, name := range []string{"main", "master"} {
		if _, found := resolveFullRef(repo, remotePrefix+name); found {
			if info, ok := resolveBranch(repo, name, remote); ok {
				return info, nil
			}
		}
	}
	for _, name := range []string{"main", "master"} {
		if info, found := resolveFullRef(repo, "refs/heads/"+name); found {
			info.name = name
			return info, nil
		}
	}

	return branchInfo{}, &ExitError{exitError, "cannot determine default branch"}
}

type headRelation int

const (
	relationOnBranch headRelation = iota
	relationOffBranch
	relationNotTraceable
)

type branchCheck struct {
	relation  headRelation
	mergeBase plumbing.Hash
}

func checkBranchRelation(
	repo *git.Repository, targetHash plumbing.Hash, branch branchInfo, _ bool,
) (branchCheck, error) {
	history, err := newHistory(repo)
	if err != nil {
		return branchCheck{}, err
	}
	anchor, found, err := findReachableBranchAnchor(history, targetHash, branch.hash)
	if err != nil {
		return branchCheck{}, err
	}
	if !found {
		return branchCheck{relation: relationNotTraceable}, nil
	}
	if anchor == targetHash {
		return branchCheck{relation: relationOnBranch}, nil
	}
	return branchCheck{relation: relationOffBranch, mergeBase: anchor}, nil
}

// resolveBranch finds a branch by name, preferring local over the cached remote.
func resolveBranch(repo *git.Repository, name, remote string) (branchInfo, bool) {
	for _, refName := range []string{
		"refs/heads/" + name,
		"refs/remotes/" + remote + "/" + name,
	} {
		if info, ok := resolveFullRef(repo, refName); ok {
			info.name = name
			return info, true
		}
	}
	return branchInfo{}, false
}

func resolveFullRef(repo *git.Repository, name string) (branchInfo, bool) {
	ref, err := repo.Reference(plumbing.ReferenceName(name), true)
	if err != nil {
		return branchInfo{}, false
	}
	return branchInfo{name: name, hash: ref.Hash()}, true
}
