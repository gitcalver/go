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

func detectBranch(repo *git.Repository, override string) (branchInfo, error) {
	if override != "" {
		return resolveBranch(repo, override)
	}

	ref, err := repo.Reference(plumbing.NewRemoteHEADReferenceName("origin"), false)
	if err == nil && ref.Type() == plumbing.SymbolicReference {
		name := ref.Target().Short()
		if after, ok := strings.CutPrefix(name, "origin/"); ok {
			name = after
		}
		if info, resolveErr := resolveBranch(repo, name); resolveErr == nil {
			return info, nil
		}
	}

	for _, name := range []string{"main", "master"} {
		if info, resolveErr := resolveBranch(repo, name); resolveErr == nil {
			return info, nil
		}
	}

	return branchInfo{}, &ExitError{exitError, "cannot determine default branch"}
}

// resolveBranch finds a branch by name, preferring local over remote.
func resolveBranch(repo *git.Repository, name string) (branchInfo, error) {
	for _, refName := range []plumbing.ReferenceName{
		plumbing.NewBranchReferenceName(name),
		plumbing.NewRemoteReferenceName("origin", name),
	} {
		ref, err := repo.Reference(refName, true)
		if err == nil {
			return branchInfo{name: name, hash: ref.Hash()}, nil
		}
	}
	return branchInfo{}, &ExitError{exitError, "branch not found: " + name}
}

type headRelation int

const (
	relationOnBranch  headRelation = iota
	relationOffBranch              // traceable via first-parent intersection
	relationNotTraceable
)

type branchCheck struct {
	relation  headRelation
	mergeBase plumbing.Hash
}

//nolint:revive // isHead is a fast-path hint, not control coupling
func checkBranchRelation(
	repo *git.Repository, targetHash plumbing.Hash, branch branchInfo, isHead bool,
) (branchCheck, error) {
	if isHead {
		headRef, err := repo.Head()
		if err == nil && headRef.Name().IsBranch() && headRef.Name().Short() == branch.name {
			return branchCheck{relation: relationOnBranch}, nil
		}
	}

	if targetHash == branch.hash {
		return branchCheck{relation: relationOnBranch}, nil
	}

	branchCommit, err := repo.CommitObject(branch.hash)
	if err != nil {
		return branchCheck{}, err
	}
	targetCommit, err := repo.CommitObject(targetHash)
	if err != nil {
		return branchCheck{}, err
	}

	// Walk both first-parent chains in tandem to find the divergence
	// point in O(D) time, where D is the distance to divergence.
	branchSeen := make(map[plumbing.Hash]struct{})
	targetSeen := make(map[plumbing.Hash]struct{})
	bc, tc := branchCommit, targetCommit
	bDone, tDone := false, false

	for !bDone || !tDone {
		if !bDone {
			if bc.Hash == targetHash {
				return branchCheck{relation: relationOnBranch}, nil
			}
			branchSeen[bc.Hash] = struct{}{}
			if _, ok := targetSeen[bc.Hash]; ok {
				return branchCheck{relation: relationOffBranch, mergeBase: bc.Hash}, nil
			}
			if bc.NumParents() == 0 {
				bDone = true
			} else {
				bc, err = bc.Parent(0)
				if err != nil {
					return branchCheck{}, err
				}
			}
		}
		if !tDone {
			targetSeen[tc.Hash] = struct{}{}
			if _, ok := branchSeen[tc.Hash]; ok {
				return branchCheck{relation: relationOffBranch, mergeBase: tc.Hash}, nil
			}
			if tc.NumParents() == 0 {
				tDone = true
			} else {
				tc, err = tc.Parent(0)
				if err != nil {
					return branchCheck{}, err
				}
			}
		}
	}

	return branchCheck{relation: relationNotTraceable}, nil
}
