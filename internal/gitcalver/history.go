// Copyright © 2026 Michael Shields
// SPDX-License-Identifier: MIT

package gitcalver

import (
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type history struct {
	repo    *git.Repository
	shallow map[plumbing.Hash]struct{}
}

type selectedChainResult struct {
	positions      map[plumbing.Hash]int
	incomplete     bool
	targetOnBranch bool
}

type branchAnchorResult struct {
	hash       plumbing.Hash
	found      bool
	incomplete bool
}

func newHistory(repo *git.Repository) (*history, error) {
	shallow := make(map[plumbing.Hash]struct{})
	if storer, ok := repo.Storer.(interface {
		Shallow() ([]plumbing.Hash, error)
	}); ok {
		boundaries, err := storer.Shallow()
		if err != nil {
			return nil, &ExitError{exitIncompleteHistory, "cannot read shallow boundary"}
		}
		for _, hash := range boundaries {
			shallow[hash] = struct{}{}
		}
	}
	return &history{repo: repo, shallow: shallow}, nil
}

func (h *history) commit(hash plumbing.Hash) (*object.Commit, error) {
	commit, err := h.repo.CommitObject(hash)
	if err != nil {
		return nil, &ExitError{exitIncompleteHistory, "commit is missing from local history"}
	}
	return commit, nil
}

func (h *history) firstParent(commit *object.Commit) (*object.Commit, bool, error) {
	if len(commit.ParentHashes) == 0 {
		return nil, false, nil
	}
	if _, ok := h.shallow[commit.Hash]; ok {
		return nil, false, &ExitError{exitIncompleteHistory, "local history ended at a shallow boundary"}
	}
	parent, err := h.commit(commit.ParentHashes[0])
	if err != nil {
		return nil, false, err
	}
	return parent, true, nil
}

func findReachableBranchAnchor(
	history *history, targetHash, branchHash plumbing.Hash,
) (plumbing.Hash, bool, error) {
	selectedChain, err := selectedBranchPositions(
		history, branchHash, targetHash,
	)
	if err != nil {
		return plumbing.ZeroHash, false, err
	}
	if selectedChain.targetOnBranch {
		return targetHash, true, nil
	}

	anchor := targetBranchAnchor(
		history, targetHash, selectedChain.positions,
	)
	if anchor.found {
		return anchor.hash, true, nil
	}
	if anchor.incomplete || selectedChain.incomplete {
		return plumbing.ZeroHash, false, &ExitError{
			exitIncompleteHistory,
			"local history cannot prove the target's branch relationship",
		}
	}
	return plumbing.ZeroHash, false, nil
}

func selectedBranchPositions(
	history *history, branchHash, targetHash plumbing.Hash,
) (selectedChainResult, error) {
	positions := make(map[plumbing.Hash]int)
	commit, err := history.commit(branchHash)
	if err != nil {
		return selectedChainResult{}, err
	}

	for position := 0; ; position++ {
		positions[commit.Hash] = position
		if commit.Hash == targetHash {
			return selectedChainResult{positions: positions, targetOnBranch: true}, nil
		}
		parent, ok, parentErr := history.firstParent(commit)
		if parentErr != nil {
			// The caller can still prove a reachable anchor from the known prefix.
			//nolint:nilerr // Preserve the usable prefix rather than discarding it.
			return selectedChainResult{positions: positions, incomplete: true}, nil
		}
		if !ok {
			return selectedChainResult{positions: positions}, nil
		}
		commit = parent
	}
}

func targetBranchAnchor(
	history *history,
	targetHash plumbing.Hash,
	branchPositions map[plumbing.Hash]int,
) branchAnchorResult {
	visited := make(map[plumbing.Hash]struct{})
	queue := []plumbing.Hash{targetHash}
	bestPosition := len(branchPositions)
	bestHash := plumbing.ZeroHash
	incomplete := false

	for next := 0; next < len(queue); next++ {
		hash := queue[next]
		if _, ok := visited[hash]; ok {
			continue
		}
		visited[hash] = struct{}{}

		commit, err := history.commit(hash)
		if err != nil {
			incomplete = true
			continue
		}
		if position, onBranch := branchPositions[hash]; onBranch {
			if position < bestPosition {
				bestPosition = position
				bestHash = hash
			}
			// Every parent is older than this selected-chain commit. Pruning here
			// avoids loading the target's entire reachable history.
			continue
		}
		if len(commit.ParentHashes) == 0 {
			continue
		}
		if _, shallow := history.shallow[hash]; shallow {
			incomplete = true
			continue
		}
		queue = append(queue, commit.ParentHashes...)
	}

	return branchAnchorResult{
		hash:       bestHash,
		found:      bestHash != plumbing.ZeroHash,
		incomplete: incomplete,
	}
}
