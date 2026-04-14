// Copyright © 2026 Michael Shields
// SPDX-License-Identifier: MIT

package gitcalver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func testRepo(t *testing.T) (string, func(dateStr string)) {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.NewBranchReferenceName("main"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}

	commitAt := func(dateStr string) {
		t.Helper()
		ts, err := time.Parse(time.RFC3339, dateStr)
		if err != nil {
			t.Fatal(err)
		}
		_, err = wt.Commit("commit", &git.CommitOptions{
			AllowEmptyCommits: true,
			Author:            &object.Signature{Name: "Test", Email: "test@test.com", When: ts},
			Committer:         &object.Signature{Name: "Test", Email: "test@test.com", When: ts},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	return dir, commitAt
}

// runCmd calls parseArgs + Run with Dir set, avoiding any CWD changes.
func runCmd(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	opts, err := parseArgs(append([]string{"--branch", "main"}, args...))
	if err != nil {
		return "gitcalver: " + err.Error(), 1
	}
	if opts == nil {
		return "", 0
	}
	opts.Dir = dir
	result, err := Run(opts)
	if err != nil {
		var exitErr *ExitError
		if errors.As(err, &exitErr) {
			return "gitcalver: " + exitErr.Message, exitErr.Code
		}
		return "gitcalver: " + err.Error(), 1
	}
	return result, 0
}

// --- Basic version computation ---

func TestSingleCommit(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	out, code := runCmd(t, dir)
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.1", out)
}

func TestThreeCommitsSameDay(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-10T10:00:00Z")
	commitAt("2026-04-10T11:00:00Z")

	out, code := runCmd(t, dir)
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.3", out)
}

func TestCommitsAcrossDays(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-10T10:00:00Z")
	commitAt("2026-04-11T09:00:00Z")

	out, code := runCmd(t, dir)
	assertEqual(t, 0, code)
	assertEqual(t, "20260411.1", out)
}

func TestDayRolloverMultiplePerDay(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-10T10:00:00Z")
	commitAt("2026-04-11T09:00:00Z")
	commitAt("2026-04-11T10:00:00Z")

	out, code := runCmd(t, dir)
	assertEqual(t, 0, code)
	assertEqual(t, "20260411.2", out)
}

// --- Prefix ---

func TestPrefix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		prefix string
		want   string
	}{
		{"empty", "", "20260410.1"},
		{"semver", "0.", "0.20260410.1"},
		{"go", "v0.", "v0.20260410.1"},
		{"custom", "myapp-", "myapp-20260410.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, commitAt := testRepo(t)
			commitAt("2026-04-10T09:00:00Z")

			args := []string{}
			if tc.prefix != "" {
				args = append(args, "--prefix", tc.prefix)
			}
			out, code := runCmd(t, dir, args...)
			assertEqual(t, 0, code)
			assertEqual(t, tc.want, out)
		})
	}
}

// --- Dirty workspace ---

func TestDirtyExits2(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0o644)

	out, code := runCmd(t, dir)
	assertEqual(t, 2, code)
	if !strings.Contains(out, "--dirty") {
		t.Fatalf("expected error to mention --dirty, got %q", out)
	}
}

func TestDirtyVersions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		args       []string
		wantExact  string
		wantPrefix string
	}{
		{"default", []string{"--dirty", "-dirty"}, "", "20260410.1-dirty."},
		{"with prefix", []string{"--prefix", "v0.", "--dirty", "-dirty"}, "", "v0.20260410.1-dirty."},
		{"no hash", []string{"--dirty", "-dirty", "--no-dirty-hash"}, "20260410.1-dirty", ""},
		{"pep440", []string{"--dirty", "+dirty"}, "", "20260410.1+dirty."},
		{"rpm", []string{"--dirty", "~dirty", "--no-dirty-hash"}, "20260410.1~dirty", ""},
		{"maven", []string{"--dirty", "-SNAPSHOT", "--no-dirty-hash"}, "20260410.1-SNAPSHOT", ""},
		{"ruby", []string{"--dirty", ".pre.dirty"}, "", "20260410.1.pre.dirty."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, commitAt := testRepo(t)
			commitAt("2026-04-10T09:00:00Z")
			os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0o644)

			out, code := runCmd(t, dir, tc.args...)
			assertEqual(t, 0, code)
			if tc.wantExact != "" {
				assertEqual(t, tc.wantExact, out)
			} else if !strings.HasPrefix(out, tc.wantPrefix) {
				t.Fatalf("expected prefix %q, got %q", tc.wantPrefix, out)
			}
		})
	}
}

func TestNoDirtyOverridesDirty(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0o644)

	_, code := runCmd(t, dir, "--dirty", "-dirty", "--no-dirty")
	assertEqual(t, 2, code)
}

func TestDirtyEmptyStringError(t *testing.T) {
	t.Parallel()
	_, err := parseArgs([]string{"--dirty", ""})
	if !errors.Is(err, errDirtyEmpty) {
		t.Fatalf("expected errDirtyEmpty, got %v", err)
	}
}

func TestNoDirtyHashWithoutDirtyError(t *testing.T) {
	t.Parallel()
	_, err := parseArgs([]string{"--no-dirty-hash"})
	if !errors.Is(err, errNoDirtyHash) {
		t.Fatalf("expected errNoDirtyHash, got %v", err)
	}
}

func TestCleanWorkspaceWithDirtyFlag(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	out, code := runCmd(t, dir, "--dirty", "-dirty")
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.1", out)
}

// --- Branch enforcement ---

func TestOffBranchExitsDirty(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	wt, _ := repo.Worktree()
	wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	commitAt("2026-04-10T10:00:00Z")

	out, code := runCmd(t, dir)
	assertEqual(t, 2, code)
	if !strings.Contains(out, "--dirty") {
		t.Fatalf("expected error to mention --dirty, got %q", out)
	}
}

func TestOffBranchDirtyVersion(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	wt, _ := repo.Worktree()
	wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	commitAt("2026-04-10T10:00:00Z")

	headRef, _ := repo.Head()

	out, code := runCmd(t, dir, "--dirty", "-dirty")
	assertEqual(t, 0, code)
	wantPrefix := "20260410.1-dirty."
	if !strings.HasPrefix(out, wantPrefix) {
		t.Fatalf("expected prefix %q, got %q", wantPrefix, out)
	}
	hashPart := strings.TrimPrefix(out, wantPrefix)
	if !strings.HasPrefix(headRef.Hash().String(), hashPart) {
		t.Fatalf("hash should be prefix of HEAD %s, got %q", headRef.Hash(), hashPart)
	}
}

func TestOffBranchMultipleMainCommits(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-10T10:00:00Z")

	repo, _ := git.PlainOpen(dir)
	wt, _ := repo.Worktree()
	wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	commitAt("2026-04-10T11:00:00Z")

	out, code := runCmd(t, dir, "--dirty", "-dirty")
	assertEqual(t, 0, code)
	if !strings.HasPrefix(out, "20260410.2-dirty.") {
		t.Fatalf("expected 20260410.2-dirty.HASH, got %q", out)
	}
}

func TestOffBranchNoDirtyHash(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	wt, _ := repo.Worktree()
	wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	commitAt("2026-04-10T10:00:00Z")

	out, code := runCmd(t, dir, "--dirty", "-dirty", "--no-dirty-hash")
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.1-dirty", out)
}

func TestOffBranchWithPrefix(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	wt, _ := repo.Worktree()
	wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	commitAt("2026-04-10T10:00:00Z")

	out, code := runCmd(t, dir, "--prefix", "v0.", "--dirty", "-dirty", "--no-dirty-hash")
	assertEqual(t, 0, code)
	assertEqual(t, "v0.20260410.1-dirty", out)
}

func TestOffBranchNotTraceable(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	mainCommit, _ := repo.CommitObject(headRef.Hash())

	ts, _ := time.Parse(time.RFC3339, "2026-04-10T10:00:00Z")
	sig := object.Signature{Name: "Test", Email: "test@test.com", When: ts}
	orphan := &object.Commit{
		Author:    sig,
		Committer: sig,
		Message:   "orphan",
		TreeHash:  mainCommit.TreeHash,
	}
	obj := repo.Storer.NewEncodedObject()
	if err := orphan.Encode(obj); err != nil {
		t.Fatal(err)
	}
	orphanHash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("orphan"), orphanHash,
	)); err != nil {
		t.Fatal(err)
	}

	wt, _ := repo.Worktree()
	wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("orphan")})

	_, code := runCmd(t, dir, "--dirty", "-dirty")
	assertEqual(t, 3, code)
}

// --- Error cases ---

func TestNotARepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, code := runCmd(t, dir)
	assertEqual(t, 1, code)
}

func TestEmptyRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	_, code := runCmd(t, dir)
	assertEqual(t, 1, code)
}

func TestBranchDetectionFails(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		positional string
	}{
		{"forward", ""},
		{"reverse", "20260410.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			repo, _ := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
				InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("trunk")},
			})
			wt, _ := repo.Worktree()
			ts, _ := time.Parse(time.RFC3339, "2026-04-10T09:00:00Z")
			wt.Commit("c1", &git.CommitOptions{
				AllowEmptyCommits: true,
				Author:            &object.Signature{Name: "Test", Email: "test@test.com", When: ts},
				Committer:         &object.Signature{Name: "Test", Email: "test@test.com", When: ts},
			})

			_, err := Run(&Options{Dir: dir, Target: tc.positional})
			var exitErr *ExitError
			if !errors.As(err, &exitErr) {
				t.Fatal("expected ExitError")
			}
			assertEqual(t, exitError, exitErr.Code)
		})
	}
}

// --- Corrupt repository ---

func TestWalkFirstParentInvalidHash(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)

	_, _, err := walkFirstParent(repo, plumbing.NewHash("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestWalkFirstParentCorruptParent(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-10T10:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	head, _ := repo.CommitObject(headRef.Hash())
	removeObject(t, dir, head.ParentHashes[0])

	_, _, err := walkFirstParent(repo, headRef.Hash())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCheckBranchRelationInvalidTarget(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	branch, _ := detectBranch(repo, "main")

	_, err := checkBranchRelation(repo, plumbing.NewHash("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"), branch, false)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCheckBranchRelationInvalidBranch(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	bogus := branchInfo{name: "main", hash: plumbing.NewHash("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")}

	_, err := checkBranchRelation(repo, headRef.Hash(), bogus, false)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestForwardBranchCheckError(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-10T10:00:00Z")
	commitAt("2026-04-10T11:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	head, _ := repo.CommitObject(headRef.Hash())
	parent, _ := head.Parent(0)
	removeObject(t, dir, headRef.Hash())

	_, code := runCmd(t, dir, parent.Hash.String())
	assertEqual(t, 1, code)
}

func TestReverseCorruptBranchTip(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	removeObject(t, dir, headRef.Hash())

	_, code := runCmd(t, dir, "20260410.1")
	assertEqual(t, 1, code)
}

func TestReverseCorruptParent(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-11T09:00:00Z")
	commitAt("2026-04-12T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	head, _ := repo.CommitObject(headRef.Hash())
	middle, _ := head.Parent(0)
	removeObject(t, dir, middle.Hash)

	_, code := runCmd(t, dir, "20260410.1")
	assertEqual(t, 1, code)
}

func TestMainCorruptRepo(t *testing.T) { //nolint:paralleltest // t.Chdir is incompatible with t.Parallel
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-10T10:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	head, _ := repo.CommitObject(headRef.Hash())
	removeObject(t, dir, head.ParentHashes[0])

	t.Chdir(dir)

	var stdout, stderr strings.Builder
	code := Main([]string{"--branch", "main"}, &stdout, &stderr)
	assertEqual(t, 1, code)
	if stderr.Len() == 0 {
		t.Fatal("expected error output")
	}
}

// --- First-parent / merge behavior ---

func TestMergeFirstParentOnly(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	wt, _ := repo.Worktree()

	wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	commitAt("2026-04-10T10:00:00Z")
	commitAt("2026-04-10T11:00:00Z")

	wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	commitAt("2026-04-10T12:00:00Z")

	out, code := runCmd(t, dir)
	assertEqual(t, 0, code)
	if !strings.HasPrefix(out, "20260410.") {
		t.Fatalf("expected 20260410.N, got %q", out)
	}
}

func TestReverseThroughMerge(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	wt, _ := repo.Worktree()

	wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	commitAt("2026-04-10T10:00:00Z")
	commitAt("2026-04-10T11:00:00Z")
	featureRef, _ := repo.Head()

	wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	commitAt("2026-04-10T12:00:00Z")
	mainRef, _ := repo.Head()
	mainCommit, _ := repo.CommitObject(mainRef.Hash())

	ts, _ := time.Parse(time.RFC3339, "2026-04-10T13:00:00Z")
	sig := object.Signature{Name: "Test", Email: "test@test.com", When: ts}
	merge := &object.Commit{
		Author:       sig,
		Committer:    sig,
		Message:      "merge",
		TreeHash:     mainCommit.TreeHash,
		ParentHashes: []plumbing.Hash{mainRef.Hash(), featureRef.Hash()},
	}
	obj := repo.Storer.NewEncodedObject()
	if err := merge.Encode(obj); err != nil {
		t.Fatal(err)
	}
	mergeHash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), mergeHash,
	)); err != nil {
		t.Fatal(err)
	}

	// First-parent walk: merge(13:00) -> main(12:00) -> base(09:00) = 3.
	// Feature-branch commits (second parent) must not inflate the count.
	hash, code := runCmd(t, dir, "20260410.3")
	assertEqual(t, 0, code)
	assertEqual(t, mergeHash.String(), hash)

	_, code = runCmd(t, dir, "20260410.4")
	assertEqual(t, 1, code)
}

// --- UTC midnight boundary ---

func TestUTCMidnightBoundary(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T23:59:00Z")
	commitAt("2026-04-11T00:01:00Z")

	out, code := runCmd(t, dir)
	assertEqual(t, 0, code)
	assertEqual(t, "20260411.1", out)
}

// --- Strictly increasing versions ---

func TestStrictlyIncreasingVersions(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-10T10:00:00Z")
	commitAt("2026-04-11T09:00:00Z")
	commitAt("2026-04-11T10:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	commit, _ := repo.CommitObject(headRef.Hash())

	var versions []string
	for {
		opts := &Options{Dir: dir, Target: commit.Hash.String(), Branch: "main"}
		v, err := Run(opts)
		if err != nil {
			break
		}
		versions = append([]string{v}, versions...)
		if commit.NumParents() == 0 {
			break
		}
		commit, _ = commit.Parent(0)
	}

	for i := 1; i < len(versions); i++ {
		if versions[i] <= versions[i-1] {
			t.Fatalf("versions not strictly increasing: %s <= %s", versions[i], versions[i-1])
		}
	}
}

// --- Decreasing committer dates ---

func TestDecreasingDatesExits1(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, _ := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	wt, _ := repo.Worktree()

	sig := func(dateStr string) *object.Signature {
		ts, _ := time.Parse(time.RFC3339, dateStr)
		return &object.Signature{Name: "Test", Email: "test@test.com", When: ts}
	}

	wt.Commit("c1", &git.CommitOptions{
		AllowEmptyCommits: true,
		Author:            sig("2026-04-11T09:00:00Z"),
		Committer:         sig("2026-04-11T09:00:00Z"),
	})
	wt.Commit("c2", &git.CommitOptions{
		AllowEmptyCommits: true,
		Author:            sig("2026-04-10T09:00:00Z"),
		Committer:         sig("2026-04-10T09:00:00Z"),
	})

	_, code := runCmd(t, dir)
	assertEqual(t, 1, code)
}

// --- Empty commits ---

func TestEmptyCommitsCounted(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-10T10:00:00Z")

	out, code := runCmd(t, dir)
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.2", out)
}

// --- Committer vs author date ---

func TestUsesCommitterDate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, _ := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	wt, _ := repo.Worktree()

	authorDate, _ := time.Parse(time.RFC3339, "2026-04-09T09:00:00Z")
	committerDate, _ := time.Parse(time.RFC3339, "2026-04-10T09:00:00Z")

	wt.Commit("c1", &git.CommitOptions{
		AllowEmptyCommits: true,
		Author:            &object.Signature{Name: "Test", Email: "test@test.com", When: authorDate},
		Committer:         &object.Signature{Name: "Test", Email: "test@test.com", When: committerDate},
	})

	out, code := runCmd(t, dir)
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.1", out)
}

// --- Reverse lookup ---

func TestReverseBasic(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-10T10:00:00Z")
	commitAt("2026-04-10T11:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	first, _ := repo.CommitObject(headRef.Hash())
	second, _ := first.Parent(0)
	third, _ := second.Parent(0)

	out, code := runCmd(t, dir, "20260410.3")
	assertEqual(t, 0, code)
	assertEqual(t, headRef.Hash().String(), out)

	out, code = runCmd(t, dir, "20260410.2")
	assertEqual(t, 0, code)
	assertEqual(t, second.Hash.String(), out)

	out, code = runCmd(t, dir, "20260410.1")
	assertEqual(t, 0, code)
	assertEqual(t, third.Hash.String(), out)
}

func TestReverseSemverFormat(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()

	out, code := runCmd(t, dir, "--prefix", "0.", "0.20260410.1")
	assertEqual(t, 0, code)
	assertEqual(t, headRef.Hash().String(), out)
}

func TestReverseGoFormat(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()

	out, code := runCmd(t, dir, "--prefix", "v0.", "v0.20260410.1")
	assertEqual(t, 0, code)
	assertEqual(t, headRef.Hash().String(), out)
}

func TestReverseCustomPrefix(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()

	out, code := runCmd(t, dir, "--prefix", "myapp-", "myapp-20260410.1")
	assertEqual(t, 0, code)
	assertEqual(t, headRef.Hash().String(), out)
}

func TestReverseShort(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	expectedShort := headRef.Hash().String()[:shortHashLen]

	out, code := runCmd(t, dir, "--short", "20260410.1")
	assertEqual(t, 0, code)
	assertEqual(t, expectedShort, out)
}

func TestReverseNotFound(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	_, code := runCmd(t, dir, "20260410.5")
	assertEqual(t, 1, code)
}

func TestReverseDateNotInHistory(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	_, code := runCmd(t, dir, "20260501.1")
	assertEqual(t, 1, code)
}

func TestReverseSkipsNewerDates(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-12T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	headCommit, _ := repo.CommitObject(headRef.Hash())
	day1Commit, _ := headCommit.Parent(0)

	out, code := runCmd(t, dir, "20260410.1")
	assertEqual(t, 0, code)
	assertEqual(t, day1Commit.Hash.String(), out)
}

func TestReverseRoundTrip(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-10T10:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()

	version, code := runCmd(t, dir)
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.2", version)

	hash, code := runCmd(t, dir, version)
	assertEqual(t, 0, code)
	assertEqual(t, headRef.Hash().String(), hash)
}

func TestReverseInvalidDate(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	_, code := runCmd(t, dir, "20261301.1")
	assertEqual(t, 1, code)
}

func TestReverseInvalidCount(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	_, code := runCmd(t, dir, "20260410.0")
	assertEqual(t, 1, code)
}

// --- Forward for specific revision ---

func TestSpecificRevision(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-10T10:00:00Z")
	commitAt("2026-04-10T11:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	parent, _ := repo.CommitObject(headRef.Hash())
	parent, _ = parent.Parent(0)

	out, err := Run(&Options{Dir: dir, Target: parent.Hash.String(), Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "20260410.2", out)
}

func TestSpecificRevisionWithPrefix(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()

	out, err := Run(&Options{Dir: dir, Target: headRef.Hash().String(), Prefix: "0.", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "0.20260410.1", out)
}

// --- CLI parsing ---

func TestParseArgsHelp(t *testing.T) {
	t.Parallel()
	_, err := parseArgs([]string{"--help"})
	if !errors.Is(err, errHelp) {
		t.Fatalf("expected errHelp, got %v", err)
	}
}

func TestParseArgsPrefixMissing(t *testing.T) {
	t.Parallel()
	_, err := parseArgs([]string{"--prefix"})
	if err == nil {
		t.Fatal("expected error for missing --prefix argument")
	}
}

func TestParseArgsDirtyMissing(t *testing.T) {
	t.Parallel()
	_, err := parseArgs([]string{"--dirty"})
	if err == nil {
		t.Fatal("expected error for missing --dirty argument")
	}
}

func TestParseArgsBranchMissing(t *testing.T) {
	t.Parallel()
	_, err := parseArgs([]string{"--branch"})
	if err == nil {
		t.Fatal("expected error for missing --branch argument")
	}
}

func TestParseArgsUnknownOption(t *testing.T) {
	t.Parallel()
	_, err := parseArgs([]string{"--bogus"})
	if err == nil {
		t.Fatal("expected error for unknown option")
	}
}

func TestParseArgsSingleDash(t *testing.T) {
	t.Parallel()
	_, err := parseArgs([]string{"-x"})
	if err == nil {
		t.Fatal("expected error for single-dash option")
	}
}

func TestParseArgsAllFlags(t *testing.T) {
	t.Parallel()
	opts, err := parseArgs([]string{
		"--prefix", "v0.",
		"--dirty", "-dirty",
		"--no-dirty-hash",
		"--branch", "develop",
		"--short",
		"abc123",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "v0.", opts.Prefix)
	assertEqual(t, "-dirty", opts.Dirty)
	assertEqual(t, true, opts.NoDirtyHash)
	assertEqual(t, "develop", opts.Branch)
	assertEqual(t, true, opts.Short)
	assertEqual(t, "abc123", opts.Target)
}

// --- Main function ---

func TestMainHelp(t *testing.T) {
	t.Parallel()
	var stdout, stderr strings.Builder
	code := Main([]string{"--help"}, &stdout, &stderr)
	assertEqual(t, 0, code)
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Fatal("expected help output")
	}
}

func TestMainInvalidOption(t *testing.T) {
	t.Parallel()
	var stdout, stderr strings.Builder
	code := Main([]string{"--invalid"}, &stdout, &stderr)
	assertEqual(t, 1, code)
	if !strings.Contains(stderr.String(), "unknown option") {
		t.Fatalf("expected unknown option error, got %q", stderr.String())
	}
}

func TestMainSuccess(t *testing.T) { //nolint:paralleltest // t.Chdir is incompatible with t.Parallel
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	t.Chdir(dir)

	var stdout, stderr strings.Builder
	code := Main([]string{"--branch", "main"}, &stdout, &stderr)
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.1", strings.TrimSpace(stdout.String()))
}

func TestMainError(t *testing.T) { //nolint:paralleltest // t.Chdir is incompatible with t.Parallel
	dir := t.TempDir()
	t.Chdir(dir)

	var stdout, stderr strings.Builder
	code := Main([]string{"--branch", "main"}, &stdout, &stderr)
	assertEqual(t, 1, code)
	if !strings.Contains(stderr.String(), "not a git repository") {
		t.Fatalf("expected repo error, got %q", stderr.String())
	}
}

func TestMainDirtyExitCode(t *testing.T) { //nolint:paralleltest // t.Chdir is incompatible with t.Parallel
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0o644)
	t.Chdir(dir)

	var stdout, stderr strings.Builder
	code := Main([]string{"--branch", "main"}, &stdout, &stderr)
	assertEqual(t, 2, code)
}

func TestMainOffBranchExitCode(t *testing.T) { //nolint:paralleltest // t.Chdir is incompatible with t.Parallel
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	wt, _ := repo.Worktree()
	wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	commitAt("2026-04-10T10:00:00Z")
	t.Chdir(dir)

	var stdout, stderr strings.Builder
	code := Main([]string{"--branch", "main"}, &stdout, &stderr)
	assertEqual(t, 2, code)
}

// --- Short hash ---

func TestShortHashBasic(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()

	short := shortHash(headRef.Hash())
	if len(short) != shortHashLen {
		t.Fatalf("expected %d-char hash, got %q", shortHashLen, short)
	}
	if !strings.HasPrefix(headRef.Hash().String(), short) {
		t.Fatalf("short hash %q is not prefix of %q", short, headRef.Hash().String())
	}

	_ = repo // used only to resolve HEAD
}

// --- ExitError ---

func TestExitErrorMessage(t *testing.T) {
	t.Parallel()
	e := &ExitError{Code: 2, Message: "test error"}
	assertEqual(t, "test error", e.Error())
}

// --- Branch detection ---

func TestDetectBranchLocalMain(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	branch, err := detectBranch(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "main", branch.name)
}

func TestDetectBranchOverride(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	branch, err := detectBranch(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "main", branch.name)
}

func TestDetectBranchOverrideNotFound(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	_, err := detectBranch(repo, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent branch")
	}
}

func TestDetectBranchNone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, _ := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("trunk")},
	})

	wt, _ := repo.Worktree()
	ts, _ := time.Parse(time.RFC3339, "2026-04-10T09:00:00Z")
	wt.Commit("c1", &git.CommitOptions{
		AllowEmptyCommits: true,
		Author:            &object.Signature{Name: "Test", Email: "test@test.com", When: ts},
		Committer:         &object.Signature{Name: "Test", Email: "test@test.com", When: ts},
	})

	_, err := detectBranch(repo, "")
	if err == nil {
		t.Fatal("expected error when no main/master branch")
	}
}

// --- Detect branch: master fallback ---

func TestDetectBranchLocalMaster(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, _ := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("master")},
	})
	wt, _ := repo.Worktree()
	ts, _ := time.Parse(time.RFC3339, "2026-04-10T09:00:00Z")
	wt.Commit("c1", &git.CommitOptions{
		AllowEmptyCommits: true,
		Author:            &object.Signature{Name: "Test", Email: "test@test.com", When: ts},
		Committer:         &object.Signature{Name: "Test", Email: "test@test.com", When: ts},
	})

	branch, err := detectBranch(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "master", branch.name)
}

// --- Specific revision not on branch ---

func TestSpecificRevisionNotOnBranch(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	wt, _ := repo.Worktree()
	wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	commitAt("2026-04-10T10:00:00Z")
	headRef, _ := repo.Head()
	featureHash := headRef.Hash().String()

	wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})

	out, code := runCmd(t, dir, featureHash)
	assertEqual(t, 2, code)
	if !strings.Contains(out, featureHash) {
		t.Fatalf("error should contain revision hash, got %q", out)
	}
}

// --- Invalid revision ---

func TestForwardInvalidRevision(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	_, code := runCmd(t, dir, "not-a-valid-ref")
	assertEqual(t, 1, code)
}

// --- Branch relation with specific hash match ---

func TestCheckBranchRelationExactHash(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	branch, _ := detectBranch(repo, "main")

	check, err := checkBranchRelation(repo, headRef.Hash(), branch, false)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, relationOnBranch, check.relation)
}

func TestCheckBranchRelationHeadNameMismatch(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	commitA := headRef.Hash()

	commitAt("2026-04-10T10:00:00Z")

	wt, _ := repo.Worktree()
	wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})

	mainRef, _ := repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	branch := branchInfo{name: "main", hash: mainRef.Hash()}

	check, err := checkBranchRelation(repo, commitA, branch, true)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, relationOnBranch, check.relation)
}

func TestCheckBranchRelationDivergenceViaBranchWalk(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z") // divergence point

	repo, _ := git.PlainOpen(dir)
	wt, _ := repo.Worktree()

	wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	commitAt("2026-04-10T10:00:00Z")
	featureRef, _ := repo.Head()

	wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	commitAt("2026-04-10T11:00:00Z")
	commitAt("2026-04-10T12:00:00Z")
	commitAt("2026-04-10T13:00:00Z")

	branch, _ := detectBranch(repo, "main")
	check, err := checkBranchRelation(repo, featureRef.Hash(), branch, false)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, relationOffBranch, check.relation)
}

// --- HEAD as explicit target ---

func TestForwardExplicitHEADDirtyCheck(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0o644)

	_, code := runCmd(t, dir, "HEAD")
	assertEqual(t, 2, code)
}

// --- Remote branch detection ---

func TestDetectBranchRemote(t *testing.T) {
	t.Parallel()
	localRepo := cloneTestRepo(t)

	branch, err := detectBranch(localRepo, "")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "main", branch.name)
}

func TestDetectBranchRemoteOverride(t *testing.T) {
	t.Parallel()
	localRepo := cloneTestRepo(t)

	branch, err := detectBranch(localRepo, "main")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "main", branch.name)
}

func TestDetectBranchRemoteSymbolicHEAD(t *testing.T) {
	t.Parallel()
	localRepo := cloneTestRepo(t)

	headRef := plumbing.NewSymbolicReference(
		plumbing.NewRemoteHEADReferenceName("origin"),
		plumbing.NewRemoteReferenceName("origin", "main"),
	)
	err := localRepo.Storer.SetReference(headRef)
	if err != nil {
		t.Fatal(err)
	}

	branch, err := detectBranch(localRepo, "")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "main", branch.name)
}

func TestDetectBranchBrokenOriginHEAD(t *testing.T) {
	t.Parallel()
	localRepo := cloneTestRepo(t)

	localRepo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.NewRemoteHEADReferenceName("origin"),
		plumbing.NewRemoteReferenceName("origin", "nonexistent"),
	))

	branch, err := detectBranch(localRepo, "")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "main", branch.name)
}

func TestDetectBranchOverrideRemoteOnly(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()

	repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewRemoteReferenceName("origin", "develop"),
		headRef.Hash(),
	))

	branch, err := detectBranch(repo, "develop")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "develop", branch.name)
}

// --- Argument terminator ---

func TestDoubleHyphenTerminator(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	out, code := runCmd(t, dir, "--", "20260410.1")
	assertEqual(t, 0, code)

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	assertEqual(t, headRef.Hash().String(), out)
}

func TestDoubleHyphenImplicitHead(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	out, code := runCmd(t, dir, "--")
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.1", out)
}

// --- --short in forward mode ---

func TestShortInForwardModeError(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	out, code := runCmd(t, dir, "--short")
	assertEqual(t, 1, code)
	if !strings.Contains(out, "--short") {
		t.Fatalf("expected error about --short, got %q", out)
	}
}

// --- Shallow clone ---

func TestShallowCloneRejected(t *testing.T) {
	t.Parallel()
	remoteDir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-10T10:00:00Z")

	localDir := t.TempDir()
	_, err := git.PlainClone(localDir, false, &git.CloneOptions{
		URL:   remoteDir,
		Depth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	out, code := runCmd(t, localDir)
	assertEqual(t, 1, code)
	if !strings.Contains(out, "shallow") {
		t.Fatalf("expected shallow clone error, got %q", out)
	}
}

// --- Leading zeros in version ---

func TestReverseLeadingZeroRejected(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	_, code := runCmd(t, dir, "20260410.01")
	assertEqual(t, 1, code)
}

// --- Multiple positional arguments ---

func TestMultiplePositionalArgsError(t *testing.T) {
	t.Parallel()
	_, err := parseArgs([]string{"arg1", "arg2"})
	if err == nil {
		t.Fatal("expected error for multiple positional arguments")
	}
	if !strings.Contains(err.Error(), "unexpected argument") {
		t.Fatalf("expected unexpected argument error, got %v", err)
	}
}

func TestMultiplePositionalArgsAfterDoubleHyphen(t *testing.T) {
	t.Parallel()
	_, err := parseArgs([]string{"--", "arg1", "arg2"})
	if err == nil {
		t.Fatal("expected error for multiple positional arguments after --")
	}
}

// --- Year boundary ---

func TestYearBoundary(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2025-12-31T23:00:00Z")
	commitAt("2026-01-01T01:00:00Z")

	out, code := runCmd(t, dir)
	assertEqual(t, 0, code)
	assertEqual(t, "20260101.1", out)
}

// --- Large N ---

func TestLargeN(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	for i := range 11 {
		commitAt(fmt.Sprintf("2026-04-10T%02d:00:00Z", 9+i))
	}

	out, code := runCmd(t, dir)
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.11", out)
}

func TestLargeNRoundTrip(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	for i := range 11 {
		commitAt(fmt.Sprintf("2026-04-10T%02d:00:00Z", 9+i))
	}

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()

	hash, code := runCmd(t, dir, "20260410.11")
	assertEqual(t, 0, code)
	assertEqual(t, headRef.Hash().String(), hash)
}

// --- Dirty --no-dirty --no-dirty-hash edge case ---

func TestDirtyNoDirtyNoDirtyHash(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0o644)

	_, code := runCmd(t, dir, "--dirty", "-dirty", "--no-dirty", "--no-dirty-hash")
	assertEqual(t, 2, code)
}

// --- MergeBase error ---

func TestForwardMergeBaseError(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z") // c1

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	c1Hash := headRef.Hash()

	wt, _ := repo.Worktree()
	wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	commitAt("2026-04-10T10:00:00Z") // feature commit

	wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	commitAt("2026-04-10T11:00:00Z") // main commit

	wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("feature")})
	removeObject(t, dir, c1Hash)

	_, code := runCmd(t, dir, "--dirty", "-dirty")
	assertEqual(t, 1, code)
}

// --- Bare repo ---

func TestForwardBareRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, true)
	if err != nil {
		t.Fatal(err)
	}

	emptyTree := &object.Tree{}
	treeObj := repo.Storer.NewEncodedObject()
	err = emptyTree.Encode(treeObj)
	if err != nil {
		t.Fatal(err)
	}
	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	if err != nil {
		t.Fatal(err)
	}

	ts, _ := time.Parse(time.RFC3339, "2026-04-10T09:00:00Z")
	sig := object.Signature{Name: "Test", Email: "test@test.com", When: ts}
	commit := &object.Commit{
		Author:    sig,
		Committer: sig,
		Message:   "c1",
		TreeHash:  treeHash,
	}
	commitObj := repo.Storer.NewEncodedObject()
	err = commit.Encode(commitObj)
	if err != nil {
		t.Fatal(err)
	}
	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	if err != nil {
		t.Fatal(err)
	}

	repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), commitHash,
	))
	repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName("main"),
	))

	out, err := forward(repo, &Options{Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "20260410.1", out)
}

// --- MergeBase error with corrupt non-first-parent ---

func TestCheckBranchRelationMergeBaseError(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z") // c1 on main

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	mainCommit, _ := repo.CommitObject(headRef.Hash())

	// Create an off-branch commit chain: f1 → f2.
	// Then remove f1 so MergeBase errors when walking f2's parents.
	ts1, _ := time.Parse(time.RFC3339, "2026-04-10T10:00:00Z")
	sig1 := object.Signature{Name: "Test", Email: "test@test.com", When: ts1}
	f1 := &object.Commit{
		Author:    sig1,
		Committer: sig1,
		Message:   "f1",
		TreeHash:  mainCommit.TreeHash,
	}
	f1Obj := repo.Storer.NewEncodedObject()
	if err := f1.Encode(f1Obj); err != nil {
		t.Fatal(err)
	}
	f1Hash, err := repo.Storer.SetEncodedObject(f1Obj)
	if err != nil {
		t.Fatal(err)
	}

	ts2, _ := time.Parse(time.RFC3339, "2026-04-10T11:00:00Z")
	sig2 := object.Signature{Name: "Test", Email: "test@test.com", When: ts2}
	f2 := &object.Commit{
		Author:       sig2,
		Committer:    sig2,
		Message:      "f2",
		TreeHash:     mainCommit.TreeHash,
		ParentHashes: []plumbing.Hash{f1Hash},
	}
	f2Obj := repo.Storer.NewEncodedObject()
	if encErr := f2.Encode(f2Obj); encErr != nil {
		t.Fatal(encErr)
	}
	f2Hash, err := repo.Storer.SetEncodedObject(f2Obj)
	if err != nil {
		t.Fatal(err)
	}

	branch := branchInfo{name: "main", hash: headRef.Hash()}
	removeObject(t, dir, f1Hash)

	_, err = checkBranchRelation(repo, f2Hash, branch, false)
	if err == nil {
		t.Fatal("expected error from MergeBase")
	}
}

// --- Corrupt index status error ---

func TestForwardCorruptIndexStatusError(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("corrupt"), 0o644)

	_, code := runCmd(t, dir)
	assertEqual(t, 1, code)
}

// --- Helpers ---

func removeObject(t *testing.T, dir string, hash plumbing.Hash) {
	t.Helper()
	hex := hash.String()
	if err := os.Remove(filepath.Join(dir, ".git", "objects", hex[:2], hex[2:])); err != nil {
		t.Fatal(err)
	}
}

func cloneTestRepo(t *testing.T) *git.Repository {
	t.Helper()
	remoteDir := t.TempDir()
	remoteRepo, _ := git.PlainInitWithOptions(remoteDir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	wt, _ := remoteRepo.Worktree()
	ts, _ := time.Parse(time.RFC3339, "2026-04-10T09:00:00Z")
	wt.Commit("c1", &git.CommitOptions{
		AllowEmptyCommits: true,
		Author:            &object.Signature{Name: "Test", Email: "test@test.com", When: ts},
		Committer:         &object.Signature{Name: "Test", Email: "test@test.com", When: ts},
	})

	localDir := t.TempDir()
	localRepo, err := git.PlainClone(localDir, false, &git.CloneOptions{URL: remoteDir})
	if err != nil {
		t.Fatal(err)
	}
	return localRepo
}

func assertEqual[T comparable](t *testing.T, expected, actual T) {
	t.Helper()
	if expected != actual {
		t.Fatalf("expected %v, got %v", expected, actual)
	}
}
