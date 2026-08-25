// Copyright © 2026 Michael Shields
// SPDX-License-Identifier: MIT

package gitcalver

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/memory"
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

func TestPrefixValidationAndReverseRequirement(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	_, code := runCmd(t, dir, "--prefix", "release/\n0.")
	assertEqual(t, 1, code)
	_, code = runCmd(t, dir, "--prefix", "v0.", "20260410.1")
	assertEqual(t, 1, code)
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

func TestOffBranchDirtyAnchorUsesCohortCount(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-08T09:00:00Z") // base, older day

	repo, _ := git.PlainOpen(dir)
	baseRef, _ := repo.Head()
	base, _ := repo.CommitObject(baseRef.Hash())

	m1 := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{baseRef.Hash()}, "2026-04-10T09:00:00Z")
	s1 := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{baseRef.Hash()}, "2026-04-10T10:00:00Z")
	merge := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{m1, s1}, "2026-04-10T11:00:00Z")
	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), merge,
	)); err != nil {
		t.Fatal(err)
	}

	// The off-chain target hangs off the merge, so its anchor is the merge
	// itself, whose cohort {merge, m1, s1} = 3 differs from the naive
	// first-parent count of 2: a first-parent regression in anchor
	// versioning would surface here as 20260410.2-dirty.
	f1 := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{merge}, "2026-04-10T12:00:00Z")

	out, code := runCmd(t, dir, "--dirty", "-dirty", f1.String())
	assertEqual(t, 0, code)
	wantPrefix := "20260410.3-dirty."
	if !strings.HasPrefix(out, wantPrefix) {
		t.Fatalf("expected prefix %q, got %q", wantPrefix, out)
	}
	hashPart := strings.TrimPrefix(out, wantPrefix)
	if !strings.HasPrefix(f1.String(), hashPart) {
		t.Fatalf("hash should be prefix of target %s, got %q", f1, hashPart)
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

	wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	_, code = runCmd(t, dir, orphanHash.String())
	assertEqual(t, 3, code)
}

// --- Error cases ---

func TestNotARepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, code := runCmd(t, dir)
	assertEqual(t, 1, code)
	_, code = runCmd(t, dir, "20260410.1")
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
	_, code = runCmd(t, dir, "20260410.1")
	assertEqual(t, 1, code)
}

func TestRepositoryOpenDetection(t *testing.T) {
	t.Parallel()
	t.Run("parent repository", func(t *testing.T) {
		t.Parallel()
		dir, commitAt := testRepo(t)
		commitAt("2026-04-10T09:00:00Z")
		nested := filepath.Join(dir, "nested")
		if err := os.Mkdir(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		out, code := runCmd(t, nested)
		assertEqual(t, 0, code)
		assertEqual(t, "20260410.1", out)
	})
	t.Run("invalid metadata", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, code := runCmd(t, dir)
		assertEqual(t, 1, code)
	})
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

func TestCohortCountInvalidHash(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	history, _ := newHistory(repo)

	_, _, err := cohortCount(history, plumbing.NewHash("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCohortCountCorruptParent(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-10T10:00:00Z")

	repo, _ := git.PlainOpen(dir)
	history, _ := newHistory(repo)
	headRef, _ := repo.Head()
	head, _ := repo.CommitObject(headRef.Hash())
	removeObject(t, dir, head.ParentHashes[0])

	_, _, err := cohortCount(history, headRef.Hash())
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
	assertEqual(t, 4, code)
}

func TestReverseCorruptBranchTip(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	removeObject(t, dir, headRef.Hash())

	_, code := runCmd(t, dir, "20260410.1")
	assertEqual(t, 4, code)
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
	assertEqual(t, 4, code)
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
	assertEqual(t, 4, code)
	if stderr.Len() == 0 {
		t.Fatal("expected error output")
	}
}

func TestSelectedBranchTipMissing(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	missing := plumbing.NewHash("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("broken"), missing,
	)); err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{"HEAD", "20260410.1"} {
		_, code := runCmd(t, dir, "--branch", "broken", target)
		assertEqual(t, 4, code)
	}
}

func TestIncompleteMetadataErrors(t *testing.T) {
	t.Parallel()
	t.Run("graft", func(t *testing.T) {
		t.Parallel()
		dir, commitAt := testRepo(t)
		commitAt("2026-04-10T09:00:00Z")
		if err := os.Mkdir(filepath.Join(dir, ".git", "info"), 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
		path := filepath.Join(dir, ".git", "info", "grafts")
		if err := os.WriteFile(path, []byte("graft\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, code := runCmd(t, dir)
		assertEqual(t, 4, code)
	})
	t.Run("unreadable shallow data", func(t *testing.T) {
		t.Parallel()
		dir, commitAt := testRepo(t)
		commitAt("2026-04-10T09:00:00Z")
		if err := os.Mkdir(filepath.Join(dir, ".git", "shallow"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, code := runCmd(t, dir)
		assertEqual(t, 4, code)
	})
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

	// The merge commit's date cohort is itself plus every same-date commit
	// reachable through any parent: base(09:00), main(12:00) through the
	// first parent, and both feature commits(10:00, 11:00) through the
	// second parent — 5 total, not the first-parent-only count of 3.
	out, code := runCmd(t, dir)
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.5", out)

	hash, code := runCmd(t, dir, "20260410.5")
	assertEqual(t, 0, code)
	assertEqual(t, mergeHash.String(), hash)

	// The sequence is sparse: the first-parent block's members are
	// base(.1), main(.2), and merge(.5); .3 and .4 were never assigned.
	for _, gap := range []string{"20260410.3", "20260410.4"} {
		_, code = runCmd(t, dir, gap)
		assertEqual(t, 1, code)
	}
}

func TestReverseSparseGapsBothSidesExactHits(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-08T09:00:00Z") // base, older day: a boundary, not counted

	repo, _ := git.PlainOpen(dir)
	baseRef, _ := repo.Head()
	base, _ := repo.CommitObject(baseRef.Hash())

	c1 := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{baseRef.Hash()}, "2026-04-10T09:00:00Z")

	s1 := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{baseRef.Hash()}, "2026-04-10T10:00:00Z")
	s2 := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{s1}, "2026-04-10T11:00:00Z")
	s3 := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{s2}, "2026-04-10T12:00:00Z")
	s4 := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{s3}, "2026-04-10T13:00:00Z")

	c2 := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{c1, s4}, "2026-04-10T14:00:00Z")

	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), c2,
	)); err != nil {
		t.Fatal(err)
	}

	// Block members are c1 (cohort {c1} = 1) and c2 (cohort {c2, c1, s1..s4}
	// = 6); .2 through .5 were never assigned to any commit.
	out, code := runCmd(t, dir, "20260410.1")
	assertEqual(t, 0, code)
	assertEqual(t, c1.String(), out)

	out, code = runCmd(t, dir, "20260410.6")
	assertEqual(t, 0, code)
	assertEqual(t, c2.String(), out)

	for _, gap := range []string{"20260410.2", "20260410.3", "20260410.4", "20260410.5"} {
		_, code = runCmd(t, dir, gap)
		assertEqual(t, 1, code)
	}
}

// --- Date cohort: pruned-walk semantics (0.3) ---

func TestIncidentTopologyVersionNeverDecreases(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-08T09:00:00Z") // base, older day: a boundary

	repo, _ := git.PlainOpen(dir)
	baseRef, _ := repo.Head()
	base, _ := repo.CommitObject(baseRef.Hash())

	m1 := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{baseRef.Hash()}, "2026-04-10T09:00:00Z")
	m2 := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{m1}, "2026-04-10T10:00:00Z")
	m3 := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{m2}, "2026-04-10T11:00:00Z")
	m4 := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{m3}, "2026-04-10T12:00:00Z")

	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), m4,
	)); err != nil {
		t.Fatal(err)
	}

	// main's own tip version before the incident: a plain 4-commit block.
	before, err := Run(&Options{Dir: dir, Target: m4.String(), Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "20260410.4", before)

	// feature branches from base independently of main's own commits.
	f1 := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{baseRef.Hash()}, "2026-04-10T13:00:00Z")

	// Merge main into feature (feature's own tip f1 is first parent), then
	// fast-forward main onto the merge. This is the reparenting that made a
	// first-parent-only count regress: main's own commits (m1..m4) leave the
	// first-parent chain and are only reachable through the second parent.
	merge := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{f1, m4}, "2026-04-10T14:00:00Z")
	if refErr := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), merge,
	)); refErr != nil {
		t.Fatal(refErr)
	}

	// Every commit is still reachable, and the date cohort only grows: the
	// version after the reparenting merge must be strictly greater, matching
	// the hand-computed cohort {merge, f1, m4, m3, m2, m1} = 6.
	after, err := Run(&Options{Dir: dir, Target: merge.String(), Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "20260410.6", after)
}

func TestSameDaySecondParentCounted(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	base, _ := repo.CommitObject(headRef.Hash())

	side := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{headRef.Hash()}, "2026-04-10T10:00:00Z")
	merge := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{headRef.Hash(), side}, "2026-04-10T11:00:00Z")

	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), merge,
	)); err != nil {
		t.Fatal(err)
	}

	// base + side + merge, all same day: cohort of 3.
	out, code := runCmd(t, dir, merge.String())
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.3", out)
}

func TestCrossDayMergeNotCounted(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-08T09:00:00Z") // base, older day

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	base, _ := repo.CommitObject(headRef.Hash())

	// The second-parent branch stays on the older day.
	side := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{headRef.Hash()}, "2026-04-08T10:00:00Z")
	// The first-parent branch (main) advances to a new day.
	main1 := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{headRef.Hash()}, "2026-04-10T09:00:00Z")
	merge := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{main1, side}, "2026-04-10T10:00:00Z")

	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), merge,
	)); err != nil {
		t.Fatal(err)
	}

	// merge + main1 only; base and side are on the older day and are
	// pruned, not counted, even though side is reachable through the
	// second parent.
	out, code := runCmd(t, dir, merge.String())
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.2", out)
}

func TestSameDateBehindOlderNotCounted(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z") // q: shares the target's date but is buried

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	q, _ := repo.CommitObject(headRef.Hash())

	// p is q's child and target's parent, dated *before* its own parent q --
	// buried clock skew that the pruned walk must never even look at, since
	// p itself is older than the target and prunes the walk right there.
	p := writeCommit(t, repo, q.TreeHash, []plumbing.Hash{headRef.Hash()}, "2026-04-09T09:00:00Z")
	target := writeCommit(t, repo, q.TreeHash, []plumbing.Hash{p}, "2026-04-10T10:00:00Z")

	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), target,
	)); err != nil {
		t.Fatal(err)
	}

	out, code := runCmd(t, dir, target.String())
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.1", out)
}

func TestBuriedFutureDateTolerated(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2027-01-01T09:00:00Z") // q: wildly future-dated, buried behind an older commit

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	q, _ := repo.CommitObject(headRef.Hash())

	p := writeCommit(t, repo, q.TreeHash, []plumbing.Hash{headRef.Hash()}, "2026-04-09T09:00:00Z")
	target := writeCommit(t, repo, q.TreeHash, []plumbing.Hash{p}, "2026-04-10T09:00:00Z")

	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), target,
	)); err != nil {
		t.Fatal(err)
	}

	// p prunes the walk before it ever reaches q, so q's wildly future date
	// is never read and never rejected.
	out, code := runCmd(t, dir, target.String())
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.1", out)
}

func TestNearCohortFutureDateErrors(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z") // base, same day as the target

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	base, _ := repo.CommitObject(headRef.Hash())

	// skewed is a parent of an already-counted commit, dated *after* it --
	// clock skew directly adjacent to the cohort, which the pruned walk
	// must still catch even though it arrives through a second parent.
	skewed := writeCommit(t, repo, base.TreeHash, nil, "2026-04-11T09:00:00Z")
	target := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{headRef.Hash(), skewed}, "2026-04-10T10:00:00Z")

	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), target,
	)); err != nil {
		t.Fatal(err)
	}

	_, code := runCmd(t, dir, target.String())
	assertEqual(t, 1, code)
}

// TestChainD2D2D1D2ForwardOKReverseDecreasing pins a regression: along a
// first-parent chain dated D2/D2/D1/D2 (newest to oldest), the root is
// buried behind an older D1 boundary and is oddly dated D2 again -- later
// than its own child. Forward at the tip must succeed (the pruned walk
// prunes at the D1 commit and never reaches the buried root), but reverse
// lookup for D1 walks the unchanged first-parent block-delimiting chain past
// that same D1 commit to the root and must still die with a decreasing-date
// error there.
func TestChainD2D2D1D2ForwardOKReverseDecreasing(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T06:00:00Z") // root: D2, buried skew (dated after its own child)

	repo, _ := git.PlainOpen(dir)
	rootRef, _ := repo.Head()
	root, _ := repo.CommitObject(rootRef.Hash())

	d1 := writeCommit(t, repo, root.TreeHash, []plumbing.Hash{rootRef.Hash()}, "2026-04-09T09:00:00Z") // D1
	d2 := writeCommit(t, repo, root.TreeHash, []plumbing.Hash{d1}, "2026-04-10T09:00:00Z")             // D2
	tip := writeCommit(t, repo, root.TreeHash, []plumbing.Hash{d2}, "2026-04-10T10:00:00Z")            // D2

	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), tip,
	)); err != nil {
		t.Fatal(err)
	}

	out, code := runCmd(t, dir, tip.String())
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.2", out)

	_, code = runCmd(t, dir, "20260409.1")
	assertEqual(t, 1, code)
}

func TestRootBlockWholeHistoryOneDate(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z") // true root

	repo, _ := git.PlainOpen(dir)
	headRef, _ := repo.Head()
	base, _ := repo.CommitObject(headRef.Hash())

	side := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{headRef.Hash()}, "2026-04-10T10:00:00Z")
	tip := writeCommit(t, repo, base.TreeHash, []plumbing.Hash{headRef.Hash(), side}, "2026-04-10T11:00:00Z")

	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), tip,
	)); err != nil {
		t.Fatal(err)
	}

	// Every commit in the repository shares one date, including the true
	// root (zero recorded parents); the whole history is one cohort.
	out, code := runCmd(t, dir, tip.String())
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.3", out)

	out, code = runCmd(t, dir, "20260410.3")
	assertEqual(t, 0, code)
	assertEqual(t, tip.String(), out)
}

// shallowSetter marks specific commits as shallow boundaries directly,
// giving tests exact control over where the boundary falls independent of
// git's real depth-based shallow-clone algorithm.
type shallowSetter interface {
	SetShallow(commits []plumbing.Hash) error
}

func setShallow(t *testing.T, repo *git.Repository, hashes ...plumbing.Hash) {
	t.Helper()
	setter, ok := repo.Storer.(shallowSetter)
	if !ok {
		t.Fatal("storer does not support SetShallow")
	}
	if err := setter.SetShallow(hashes); err != nil {
		t.Fatal(err)
	}
}

func TestShallowSecondParentSameDateExitsIncomplete(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-08T09:00:00Z") // r: older root

	repo, _ := git.PlainOpen(dir)
	rRef, _ := repo.Head()
	r, _ := repo.CommitObject(rRef.Hash())

	// f0 is same-date as the merge and reached only through the second
	// parent; it is the shallow boundary, so its own parent r is
	// unresolvable for completeness purposes even though r is physically
	// present in this repository.
	f0 := writeCommit(t, repo, r.TreeHash, []plumbing.Hash{rRef.Hash()}, "2026-04-10T09:00:00Z")
	m1 := writeCommit(t, repo, r.TreeHash, []plumbing.Hash{rRef.Hash()}, "2026-04-10T10:00:00Z")
	merge := writeCommit(t, repo, r.TreeHash, []plumbing.Hash{m1, f0}, "2026-04-10T11:00:00Z")

	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), merge,
	)); err != nil {
		t.Fatal(err)
	}
	setShallow(t, repo, f0)

	out, code := runCmd(t, dir, merge.String())
	assertEqual(t, 4, code)
	if !strings.Contains(out, "history") {
		t.Fatalf("expected incomplete-history error, got %q", out)
	}
}

func TestReverseShallowSecondParentSameDateExitsIncomplete(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-08T09:00:00Z") // r: older root

	repo, _ := git.PlainOpen(dir)
	rRef, _ := repo.Head()
	r, _ := repo.CommitObject(rRef.Hash())

	f0 := writeCommit(t, repo, r.TreeHash, []plumbing.Hash{rRef.Hash()}, "2026-04-10T09:00:00Z")
	m1 := writeCommit(t, repo, r.TreeHash, []plumbing.Hash{rRef.Hash()}, "2026-04-10T10:00:00Z")
	merge := writeCommit(t, repo, r.TreeHash, []plumbing.Hash{m1, f0}, "2026-04-10T11:00:00Z")

	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), merge,
	)); err != nil {
		t.Fatal(err)
	}
	setShallow(t, repo, f0)

	// The first-parent block-delimiting walk (merge -> m1 -> r) never
	// touches f0, so it succeeds; m1's own cohort is {m1} = 1. Only once
	// selection reaches merge's own cohort does f0's shallow boundary
	// surface, turning a would-be "not found" into "incomplete history".
	out, code := runCmd(t, dir, "20260410.2")
	assertEqual(t, 4, code)
	if !strings.Contains(out, "history") {
		t.Fatalf("expected incomplete-history error, got %q", out)
	}
}

func TestShallowOlderDatedBoundaryOK(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-08T09:00:00Z") // r: ancient root

	repo, _ := git.PlainOpen(dir)
	rRef, _ := repo.Head()
	r, _ := repo.CommitObject(rRef.Hash())

	// old is dated before the target and is a shallow boundary; since it is
	// strictly older, the walk prunes it without ever needing its own
	// parents, so the boundary is harmless.
	old := writeCommit(t, repo, r.TreeHash, []plumbing.Hash{rRef.Hash()}, "2026-04-09T09:00:00Z")
	target := writeCommit(t, repo, r.TreeHash, []plumbing.Hash{old}, "2026-04-10T09:00:00Z")

	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), target,
	)); err != nil {
		t.Fatal(err)
	}
	setShallow(t, repo, old)

	out, code := runCmd(t, dir, target.String())
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.1", out)
}

func TestShallowMarkedTrueRootSameDateOK(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z") // root, same date as the target

	repo, _ := git.PlainOpen(dir)
	rootRef, _ := repo.Head()
	root, _ := repo.CommitObject(rootRef.Hash())
	target := writeCommit(t, repo, root.TreeHash, []plumbing.Hash{rootRef.Hash()}, "2026-04-10T10:00:00Z")
	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), target,
	)); err != nil {
		t.Fatal(err)
	}

	// Real depth-limited clones list depth-cut roots in the shallow file.
	// A true root hides nothing, so a same-date root carrying a shallow
	// mark must still be countable, not exit 4.
	setShallow(t, repo, rootRef.Hash())

	out, code := runCmd(t, dir, target.String())
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.2", out)
}

func TestReverseShallowCloneIncomplete(t *testing.T) {
	t.Parallel()
	remoteDir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-10T10:00:00Z")

	localDir := t.TempDir()
	if _, err := git.PlainClone(localDir, false, &git.CloneOptions{
		URL:   remoteDir,
		Depth: 1,
	}); err != nil {
		t.Fatal(err)
	}

	out, code := runCmd(t, localDir, "20260410.1")
	assertEqual(t, 4, code)
	if !strings.Contains(out, "history") {
		t.Fatalf("expected incomplete-history error, got %q", out)
	}
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
	_, code = runCmd(t, dir, "20260410.1")
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
	expectedShort := headRef.Hash().String()[:objectIDPrefixLen]

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
	_, code = runCmd(t, dir, "20260410.999999999999999999999999999999999999")
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

func TestAnnotatedTagRevision(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	head, _ := repo.Head()
	when, _ := time.Parse(time.RFC3339, "2026-04-10T10:00:00Z")
	tag := &object.Tag{
		Name:       "annotated",
		Tagger:     object.Signature{Name: "Test", Email: "test@test.com", When: when},
		Message:    "annotated tag",
		TargetType: plumbing.CommitObject,
		Target:     head.Hash(),
	}
	encoded := repo.Storer.NewEncodedObject()
	if err := tag.Encode(encoded); err != nil {
		t.Fatal(err)
	}
	tagHash, err := repo.Storer.SetEncodedObject(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewTagReferenceName("annotated"), tagHash,
	)); err != nil {
		t.Fatal(err)
	}

	out, code := runCmd(t, dir, "annotated")
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.1", out)

	commit, _ := repo.CommitObject(head.Hash())
	_, code = runCmd(t, dir, commit.TreeHash.String())
	assertEqual(t, 1, code)
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
		"--remote", "upstream",
		"--short",
		"--version",
		"abc123",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "v0.", opts.Prefix)
	assertEqual(t, "-dirty", opts.Dirty)
	assertEqual(t, true, opts.NoDirtyHash)
	assertEqual(t, "develop", opts.Branch)
	assertEqual(t, "upstream", opts.Remote)
	assertEqual(t, true, opts.Short)
	assertEqual(t, true, opts.showVersion)
	assertEqual(t, "abc123", opts.Target)
}

func TestParseArgsRemoteErrors(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"--remote"}, {"--remote", ""}} {
		if _, err := parseArgs(args); err == nil {
			t.Fatalf("expected error for %q", args)
		}
	}
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

func TestMainVersion(t *testing.T) {
	t.Parallel()
	var stdout, stderr strings.Builder
	code := Main([]string{"--version"}, &stdout, &stderr)
	assertEqual(t, 0, code)
	assertEqual(t, "gitcalver (development)", strings.TrimSpace(stdout.String()))
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

	short := objectIDPrefix(headRef.Hash())
	if len(short) != objectIDPrefixLen {
		t.Fatalf("expected %d-char hash, got %q", objectIDPrefixLen, short)
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
	assertEqual(t, e, normalizeExitError(e))
	plain := normalizeExitError(errors.New("plain error"))
	assertEqual(t, exitError, plain.Code)
	assertEqual(t, "plain error", plain.Message)
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

// A full ref path is not a branch name: like the reference implementation,
// --branch resolves only refs/heads/NAME and refs/remotes/REMOTE/NAME.
func TestDetectBranchRejectsFullRefOverride(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	repo, _ := git.PlainOpen(dir)

	for _, tc := range []struct {
		name     string
		override string
	}{
		{"local", "refs/heads/main"},
		{"remote", "refs/remotes/origin/main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := detectBranch(repo, tc.override)
			var exitErr *ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("expected ExitError, got %v", err)
			}
			assertEqual(t, exitError, exitErr.Code)
			assertEqual(t, "branch not found: "+tc.override, exitErr.Error())
		})
	}
}

func TestDetectBranchEmptyRemote(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	_, err := detectBranch(repo, "", "")
	if err == nil {
		t.Fatal("expected an empty-remote error")
	}
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

func TestCheckBranchRelationNotTraceable(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	mainRef, _ := repo.Head()
	mainCommit, _ := repo.CommitObject(mainRef.Hash())
	orphan := writeCommit(t, repo, mainCommit.TreeHash, nil, "2026-04-11T09:00:00Z")

	check, err := checkBranchRelation(
		repo,
		orphan,
		branchInfo{name: "main", hash: mainRef.Hash()},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, relationNotTraceable, check.relation)
}

func TestCheckBranchRelationUnreadableShallowData(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	head, _ := repo.Head()
	branch := branchInfo{name: "main", hash: head.Hash()}
	if err := os.Mkdir(filepath.Join(dir, ".git", "shallow"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := checkBranchRelation(repo, head.Hash(), branch, false); err == nil {
		t.Fatal("expected shallow metadata error")
	}
}

// --- HEAD as explicit target ---

func TestForwardExplicitHEADDirtyCheck(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0o644)

	_, code := runCmd(t, dir, "HEAD")
	assertEqual(t, 0, code)
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

func TestShallowCloneIncompleteDateBlock(t *testing.T) {
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
	assertEqual(t, 4, code)
	if !strings.Contains(out, "history") {
		t.Fatalf("expected incomplete-history error, got %q", out)
	}
}

func TestMissingPromisorCommitIsIncomplete(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-11T09:00:00Z")

	repo, _ := git.PlainOpen(dir)
	head, _ := repo.Head()
	commit, _ := repo.CommitObject(head.Hash())
	removeObject(t, dir, commit.ParentHashes[0])

	configPath := filepath.Join(dir, ".git", "config")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	configText := strings.Replace(string(data), "repositoryformatversion = 0", "repositoryformatversion = 1", 1)
	configText += "\n[extensions]\n\tpartialClone = blocked\n"
	if err = os.WriteFile(configPath, []byte(configText), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err = openRepository(dir, false); err != nil {
		t.Fatalf("open partial repository: %T: %v", err, err)
	}
	_, code := runCmd(t, dir, "HEAD")
	assertEqual(t, 4, code)
}

func TestPartialCloneStorageErrors(t *testing.T) {
	t.Parallel()
	base := memory.NewStorage()
	storerWithoutExtensions := &partialCloneStorer{Storer: base}
	cfg, err := storerWithoutExtensions.Config()
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, false, cfg.Raw.HasSection("extensions"))

	storer := &partialCloneStorer{Storer: &configErrorStorer{Storer: base}}
	if _, err := storer.Config(); err == nil {
		t.Fatal("expected config error")
	}
	if _, err := openRepositoryIgnoringPartialClone(t.TempDir()); err == nil {
		t.Fatal("expected repository discovery error")
	}
}

func TestPartialCloneLinkedWorktreeStorage(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	linked := filepath.Join(t.TempDir(), "linked")
	cmd := exec.Command("git", "-C", dir, "worktree", "add", "--detach", linked, "HEAD")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v: %s", err, output)
	}

	repo, err := openRepositoryIgnoringPartialClone(linked)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Head(); err != nil {
		t.Fatal(err)
	}
}

func TestGitDirectoryDiscovery(t *testing.T) {
	t.Parallel()
	t.Run("non-directory", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := findGitDirs(path); err == nil {
			t.Fatal("expected non-directory error")
		}
	})
	t.Run("not a repository", func(t *testing.T) {
		t.Parallel()
		if _, err := findGitDirs(t.TempDir()); err == nil {
			t.Fatal("expected discovery error")
		}
	})
	t.Run("invalid git file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("invalid\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := findGitDirs(dir); err == nil {
			t.Fatal("expected invalid .git error")
		}
	})
	t.Run("relative git file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		gitDir := filepath.Join(dir, "metadata")
		if err := os.Mkdir(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: metadata\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		dirs, err := findGitDirs(dir)
		if err != nil {
			t.Fatal(err)
		}
		assertEqual(t, gitDir, dirs.gitDir)
	})
	t.Run("absolute common directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		gitDir := filepath.Join(dir, "metadata")
		commonDir := filepath.Join(dir, "common")
		if err := os.Mkdir(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(commonDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte(commonDir+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		dirs, err := findGitDirs(dir)
		if err != nil {
			t.Fatal(err)
		}
		assertEqual(t, commonDir, dirs.commonDir)
	})
	t.Run("unreadable common directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		gitDir := filepath.Join(dir, "metadata")
		if err := os.Mkdir(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: metadata\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(gitDir, "commondir"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := findGitDirs(dir); err == nil {
			t.Fatal("expected commondir read error")
		}
	})
	t.Run("git file stat error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.Symlink(".git", filepath.Join(dir, ".git")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := gitDirAt(dir); err == nil {
			t.Fatal("expected symlink-loop error")
		}
	})
	t.Run("git file read error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, ".git")
		if err := os.WriteFile(path, []byte("gitdir: metadata\n"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(path, 0o600); err != nil {
				t.Error(err)
			}
		})
		if _, _, err := gitDirAt(dir); err == nil {
			t.Fatal("expected .git read error")
		}
	})
}

func TestTargetBranchAnchorDeduplicatesMergeParents(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	repo, _ := git.PlainOpen(dir)
	head, _ := repo.Head()
	commit, _ := repo.CommitObject(head.Hash())
	merge := writeCommit(
		t,
		repo,
		commit.TreeHash,
		[]plumbing.Hash{head.Hash(), head.Hash()},
		"2026-04-10T10:00:00Z",
	)
	history, _ := newHistory(repo)
	anchor := targetBranchAnchor(
		history,
		merge,
		map[plumbing.Hash]int{head.Hash(): 0},
	)
	assertEqual(t, false, anchor.incomplete)
	assertEqual(t, true, anchor.found)
	assertEqual(t, head.Hash(), anchor.hash)

	history.shallow[merge] = struct{}{}
	anchor = targetBranchAnchor(history, merge, nil)
	assertEqual(t, true, anchor.incomplete)
	assertEqual(t, false, anchor.found)
}

func TestTargetBranchAnchorChoosesNewestIntersection(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	repo, _ := git.PlainOpen(dir)
	baseRef, _ := repo.Head()
	base, _ := repo.CommitObject(baseRef.Hash())

	commitAt("2026-04-11T09:00:00Z")
	newerRef, _ := repo.Head()
	newer, _ := repo.CommitObject(newerRef.Hash())
	commitAt("2026-04-12T09:00:00Z")
	tipRef, _ := repo.Head()

	offChain := writeCommit(
		t,
		repo,
		newer.TreeHash,
		[]plumbing.Hash{newer.Hash},
		"2026-04-13T09:00:00Z",
	)
	target := writeCommit(
		t,
		repo,
		newer.TreeHash,
		[]plumbing.Hash{base.Hash, offChain},
		"2026-04-14T09:00:00Z",
	)
	history, _ := newHistory(repo)
	selectedChain, err := selectedBranchPositions(
		history,
		tipRef.Hash(),
		target,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, false, selectedChain.incomplete)
	assertEqual(t, false, selectedChain.targetOnBranch)

	anchor := targetBranchAnchor(history, target, selectedChain.positions)
	assertEqual(t, false, anchor.incomplete)
	assertEqual(t, true, anchor.found)
	assertEqual(t, newer.Hash, anchor.hash)
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
	assertEqual(t, 4, code)
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
	enablePartialClone(t, filepath.Join(dir, "config"))

	state, err := validateRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	out, err := forward(state, &Options{Branch: "main"})
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
	assertEqual(t, 4, code)
}

// --- Helpers ---

type configErrorStorer struct {
	storage.Storer
}

func (*configErrorStorer) Config() (*config.Config, error) {
	return nil, errors.New("config error")
}

func writeCommit(
	t *testing.T,
	repo *git.Repository,
	tree plumbing.Hash,
	parents []plumbing.Hash,
	date string,
) plumbing.Hash {
	t.Helper()
	when, err := time.Parse(time.RFC3339, date)
	if err != nil {
		t.Fatal(err)
	}
	signature := object.Signature{Name: "Test", Email: "test@test.com", When: when}
	commit := &object.Commit{
		Author:       signature,
		Committer:    signature,
		Message:      "commit",
		TreeHash:     tree,
		ParentHashes: parents,
	}
	encoded := repo.Storer.NewEncodedObject()
	if err = commit.Encode(encoded); err != nil {
		t.Fatal(err)
	}
	hash, err := repo.Storer.SetEncodedObject(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func enablePartialClone(t *testing.T, configPath string) {
	t.Helper()
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("\n[extensions]\n\tpartialClone = blocked\n")...)
	if err = os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

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
