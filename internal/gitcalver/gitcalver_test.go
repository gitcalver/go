// Copyright © 2026 Michael Shields
// SPDX-License-Identifier: MIT

package gitcalver

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
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

func TestSHA256RepositoryRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gitCLI(t, "init", "--object-format=sha256", dir)
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	gitCLI(t, "-C", dir, "worktree", "add", "--orphan", "-b", "linked", linked)
	bare := filepath.Join(t.TempDir(), "bare.git")
	gitCLI(t, "init", "--bare", "--object-format=sha256", bare)
	partial := t.TempDir()
	gitCLI(t, "init", "--object-format=sha256", partial)
	enablePartialClone(t, filepath.Join(partial, ".git", "config"))

	layouts := repoLayouts{worktree: dir, nested: nested, linked: linked, bare: bare, partial: partial}
	for _, tc := range layouts.all() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, args := range [][]string{nil, {"HEAD"}, {"20260410.1"}} {
				out, code := runCmd(t, tc.dir, args...)
				assertEqual(t, 1, code)
				assertEqual(t, "gitcalver: SHA-256 repositories are not supported", out)
			}
		})
	}
}

func TestExplicitSHA1RepositoryAccepted(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-10T10:00:00Z")
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	gitCLI(t, "-C", dir, "worktree", "add", "--detach", linked, "HEAD")
	bare := filepath.Join(t.TempDir(), "bare.git")
	gitCLI(t, "clone", "--bare", dir, bare)
	partial := filepath.Join(t.TempDir(), "partial")
	gitCLI(t, "clone", dir, partial)
	for _, configPath := range []string{
		filepath.Join(dir, ".git", "config"),
		filepath.Join(bare, "config"),
		filepath.Join(partial, ".git", "config"),
	} {
		setObjectFormat(t, configPath, "1", "sha1")
	}
	enablePartialClone(t, filepath.Join(partial, ".git", "config"))

	layouts := repoLayouts{worktree: dir, nested: nested, linked: linked, bare: bare, partial: partial}
	for _, tc := range layouts.all() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, args := range [][]string{nil, {"HEAD"}} {
				out, code := runCmd(t, tc.dir, args...)
				assertEqual(t, 0, code)
				assertEqual(t, "20260410.2", out)
			}
		})
	}
}

func TestRejectedObjectFormatIsNotSHA256(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, version, format string }{
		{"version 0 sha1", "0", "sha1"},
		{"version 2 sha1", "2", "sha1"},
		{"no version sha1", "", "sha1"},
		{"version 1 SHA1", "1", "SHA1"},
		{"version 1 SHA256", "1", "SHA256"},
		{"version 1 sha512", "1", "sha512"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, commitAt := testRepo(t)
			commitAt("2026-04-10T09:00:00Z")
			setObjectFormat(t, filepath.Join(dir, ".git", "config"), tc.version, tc.format)

			out, code := runCmd(t, dir)
			assertEqual(t, 1, code)
			assertEqual(t, "gitcalver: not a git repository", out)
		})
	}
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
	t.Run("unsupported bare repository inside another repository", func(t *testing.T) {
		t.Parallel()
		dir, commitAt := testRepo(t)
		commitAt("2026-04-10T09:00:00Z")
		bare := filepath.Join(dir, "nested.git")
		gitCLI(t, "init", "--bare", bare)
		gitCLI(t, "-C", bare, "config", "extensions.unsupported", "true")
		out, code := runCmd(t, bare, "HEAD")
		assertEqual(t, 1, code)
		assertEqual(t, "gitcalver: not a git repository", out)
	})
	for _, tc := range []struct {
		name  string
		files map[string]string
	}{
		{"unparseable config", map[string]string{"HEAD": "ref: refs/heads/main\n", "config": "key: value\n"}},
		{"missing common directory", map[string]string{"commondir": "/nonexistent\n"}},
	} {
		t.Run("subdirectory with "+tc.name, func(t *testing.T) {
			t.Parallel()
			dir, commitAt := testRepo(t)
			commitAt("2026-04-10T09:00:00Z")
			sub := filepath.Join(dir, "sub")
			if err := os.Mkdir(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, content := range tc.files {
				if err := os.WriteFile(filepath.Join(sub, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			out, code := runCmd(t, sub, "HEAD")
			assertEqual(t, 0, code)
			assertEqual(t, "20260410.1", out)
		})
	}
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

	if _, err = openRepositoryAt(dir); err != nil {
		t.Fatalf("open partial repository: %T: %v", err, err)
	}
	_, code := runCmd(t, dir, "HEAD")
	assertEqual(t, 4, code)
}

func TestCompatStorageConfig(t *testing.T) {
	t.Parallel()
	t.Run("without extensions", func(t *testing.T) {
		t.Parallel()
		dir, _ := testRepo(t)
		cfg, err := newCompatStorage(osfs.New(filepath.Join(dir, ".git"))).Config()
		if err != nil {
			t.Fatal(err)
		}
		assertEqual(t, false, cfg.Raw.HasSection("extensions"))
	})
	t.Run("unreadable", func(t *testing.T) {
		t.Parallel()
		dir, _ := testRepo(t)
		if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("key: value\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := newCompatStorage(osfs.New(filepath.Join(dir, ".git"))).Config(); err == nil {
			t.Fatal("expected config error")
		}
	})
}

func TestCompatStorageKeepsPrefixLookup(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	enablePartialClone(t, filepath.Join(dir, ".git", "config"))

	repo, err := openRepositoryAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := repo.Storer.(interface {
		HashesWithPrefix(prefix []byte) ([]plumbing.Hash, error)
	}); !ok {
		t.Fatal("storer hides the filesystem prefix lookup")
	}
}

func TestCompatStorageModule(t *testing.T) {
	t.Parallel()
	t.Run("escaping name", func(t *testing.T) {
		t.Parallel()
		dir, _ := testRepo(t)
		_, err := newCompatStorage(osfs.New(filepath.Join(dir, ".git"))).Module("../../escape")
		if err == nil {
			t.Fatal("expected error for a name outside the modules directory")
		}
	})
}

func submoduleRepo(t *testing.T) string {
	t.Helper()
	subDir, subCommitAt := testRepo(t)
	subCommitAt("2026-04-09T09:00:00Z")
	dir, commitAt := testRepo(t)
	gitCLI(t, "-C", dir, "-c", "protocol.file.allow=always", "submodule", "add", subDir, "libs/sub")
	commitAt("2026-04-10T09:00:00Z")
	return dir
}

// advanceSubmodule commits to the submodule's git directory rather than through
// the .git file in its working tree, so the commit lands in the repository
// gitcalver is meant to read even if a run has repointed that file.
func advanceSubmodule(t *testing.T, moduleDir string) {
	t.Helper()
	gitCLI(t, "--git-dir", moduleDir, "-c", "user.name=Test", "-c", "user.email=test@test.com",
		"commit", "--allow-empty", "-m", "advance")
}

func TestSubmoduleWithExtensions(t *testing.T) {
	t.Parallel()
	dir := submoduleRepo(t)
	moduleDir := filepath.Join(dir, ".git", "modules", "libs", "sub")
	moduleConfig := filepath.Join(moduleDir, "config")
	gitCLI(t, "config", "--file", moduleConfig, "core.repositoryformatversion", "1")
	gitCLI(t, "config", "--file", moduleConfig, "extensions.partialClone", "origin")

	out, code := runCmd(t, dir)
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.1", out)

	advanceSubmodule(t, moduleDir)
	out, code = runCmd(t, dir)
	assertEqual(t, 2, code)
	assertEqual(t, "gitcalver: workspace is dirty; use --dirty to allow", out)
}

func TestLinkedWorktreeSubmodule(t *testing.T) {
	t.Parallel()
	dir := submoduleRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	gitCLI(t, "-C", dir, "worktree", "add", "--detach", linked, "HEAD")
	gitCLI(t, "-C", linked, "-c", "protocol.file.allow=always", "submodule", "update", "--init")

	out, code := runCmd(t, linked)
	assertEqual(t, 0, code)
	assertEqual(t, "20260410.1", out)

	advanceSubmodule(t, filepath.Join(dir, ".git", "worktrees", "linked", "modules", "libs", "sub"))
	out, code = runCmd(t, linked)
	assertEqual(t, 2, code)
	assertEqual(t, "gitcalver: workspace is dirty; use --dirty to allow", out)
}

func TestOpenRepository(t *testing.T) {
	t.Parallel()
	dir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	linked := filepath.Join(t.TempDir(), "linked")
	gitCLI(t, "-C", dir, "worktree", "add", "--detach", linked, "HEAD")
	bare := filepath.Join(t.TempDir(), "bare.git")
	gitCLI(t, "clone", "--bare", dir, bare)

	for _, tc := range []struct {
		name, dir string
		bare      bool
	}{
		{"worktree", dir, false},
		{"linked worktree", linked, false},
		{"bare", bare, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo, err := openRepositoryAt(tc.dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = repo.Head(); err != nil {
				t.Fatal(err)
			}
			_, err = repo.Worktree()
			assertEqual(t, tc.bare, errors.Is(err, git.ErrIsBareRepository))
		})
	}

	t.Run("missing common directory", func(t *testing.T) {
		t.Parallel()
		dirs, err := findGitDirs(linked)
		if err != nil {
			t.Fatal(err)
		}
		dirs.commonDir = filepath.Join(t.TempDir(), "missing")
		if _, _, err = openRepository(dirs); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected a not-exist error, got %v", err)
		}
	})
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
	t.Run("missing directory", func(t *testing.T) {
		t.Parallel()
		if _, err := findGitDirs(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected a not-exist error, got %v", err)
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
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		gitDir := filepath.Join(dir, "metadata")
		if err = os.Mkdir(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: metadata\n"), 0o644); err != nil {
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

// --- Repository discovery ---

type discoveryOutcome struct {
	out  string
	code int
}

func TestRepositoryDiscoveryLayouts(t *testing.T) {
	t.Parallel()

	mkdirs := func(elem ...string) string {
		t.Helper()
		path := filepath.Join(elem...)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write := func(text string, elem ...string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(elem...), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// main: two commits on 2026-04-10, so 20260410.2.
	mainDir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-10T10:00:00Z")
	mkdirs(mainDir, ".git", "info")
	write("stray/\nnested.git/\n", mainDir, ".git", "info", "exclude")
	sub := mkdirs(mainDir, "sub")

	// A bare repository nested in the worktree has its own history: 20260411.3.
	nestedSrc, nestedCommitAt := testRepo(t)
	nestedCommitAt("2026-04-11T09:00:00Z")
	nestedCommitAt("2026-04-11T10:00:00Z")
	nestedCommitAt("2026-04-11T11:00:00Z")
	nestedBare := filepath.Join(mainDir, "nested.git")
	gitCLI(t, "clone", "--bare", nestedSrc, nestedBare)

	// A linked worktree checked out one commit back: 20260410.1.
	linked := filepath.Join(t.TempDir(), "linked")
	gitCLI(t, "-C", mainDir, "worktree", "add", "--detach", linked, "HEAD~1")
	linkedSub := mkdirs(linked, "sub")
	brokenLinked := filepath.Join(t.TempDir(), "broken")
	gitCLI(t, "-C", mainDir, "worktree", "add", "--detach", brokenLinked, "HEAD")
	write("/nonexistent\n", mainDir, ".git", "worktrees", "broken", "commondir")

	bare := filepath.Join(t.TempDir(), "bare.git")
	gitCLI(t, "clone", "--bare", mainDir, bare)

	// The bare-repository-plus-worktrees layout: .git is a file naming the
	// bare repository, and the container directory has no working tree. The
	// worktree is dirty because the shared core.bare=true does not make it bare.
	container := t.TempDir()
	gitCLI(t, "clone", "--bare", mainDir, filepath.Join(container, ".bare"))
	write("gitdir: ./.bare\n", container, ".git")
	containerWorktree := filepath.Join(container, "wt")
	gitCLI(t, "-C", filepath.Join(container, ".bare"), "worktree", "add", "--detach", containerWorktree, "HEAD")
	write("untracked\n", containerWorktree, "untracked.txt")

	// core.bare=true on a repository with a .git directory and a dirty tree.
	bareFlagged := filepath.Join(t.TempDir(), "flagged")
	gitCLI(t, "clone", mainDir, bareFlagged)
	gitCLI(t, "-C", bareFlagged, "config", "core.bare", "true")
	write("untracked\n", bareFlagged, "untracked.txt")

	bareUnset := filepath.Join(t.TempDir(), "unset.git")
	gitCLI(t, "clone", "--bare", mainDir, bareUnset)
	gitCLI(t, "config", "--file", filepath.Join(bareUnset, "config"), "--unset", "core.bare")
	bareYes := filepath.Join(t.TempDir(), "yes.git")
	gitCLI(t, "clone", "--bare", mainDir, bareYes)
	gitCLI(t, "config", "--file", filepath.Join(bareYes, "config"), "core.bare", "yes")
	bareFalse := filepath.Join(t.TempDir(), "false.git")
	gitCLI(t, "clone", "--bare", mainDir, bareFalse)
	gitCLI(t, "config", "--file", filepath.Join(bareFalse, "config"), "core.bare", "false")

	// Directories that hold some of what makes a git directory, but not all
	// of it. git passes over each of them and keeps searching upward.
	stray := filepath.Join(mainDir, "stray")
	headRef := "ref: refs/heads/main\n"
	for name, head := range map[string]string{
		"head-only":        headRef,
		"head-objects":     headRef,
		"head-refs":        headRef,
		"garbage-head":     "garbage\n",
		"non-hex-head":     strings.Repeat("z", 40) + "\n",
		"head-outside-ref": "ref: heads/main\n",
	} {
		mkdirs(stray, name)
		write(head, stray, name, "HEAD")
	}
	mkdirs(stray, "head-objects", "objects")
	mkdirs(stray, "head-refs", "refs")
	for _, name := range []string{"garbage-head", "non-hex-head", "head-outside-ref"} {
		mkdirs(stray, name, "objects")
		mkdirs(stray, name, "refs")
	}
	mkdirs(stray, "config-only")
	write("[core]\n\tbare = true\n", stray, "config-only", "config")
	mkdirs(stray, "commondir-only")
	write("../..\n", stray, "commondir-only", "commondir")
	mkdirs(stray, "commondir-without-objects")
	write(headRef, stray, "commondir-without-objects", "HEAD")
	write("../..\n", stray, "commondir-without-objects", "commondir")
	mkdirs(stray, "empty-dot-git", ".git")
	mkdirs(stray, "dot-git-without-refs", ".git", "objects")
	write(headRef, stray, "dot-git-without-refs", ".git", "HEAD")
	mkdirs(stray, "dot-git-garbage-head", ".git", "objects")
	mkdirs(stray, "dot-git-garbage-head", ".git", "refs")
	write("garbage\n", stray, "dot-git-garbage-head", ".git", "HEAD")

	// Things git does treat as a repository. These are empty, so there are
	// no commits to version.
	mkdirs(stray, "empty-bare", "objects")
	mkdirs(stray, "empty-bare", "refs")
	write(headRef, stray, "empty-bare", "HEAD")
	mkdirs(stray, "detached-missing", "objects")
	mkdirs(stray, "detached-missing", "refs")
	write(strings.Repeat("a", 40)+"\n", stray, "detached-missing", "HEAD")

	// git stops at an unreadable gitfile or commondir instead of searching on.
	mkdirs(stray, "bad-gitfile")
	write("not a gitfile\n", stray, "bad-gitfile", ".git")
	mkdirs(stray, "commondir-directory", "commondir")
	write(headRef, stray, "commondir-directory", "HEAD")

	same := func(out string, code int) [2]discoveryOutcome {
		return [2]discoveryOutcome{{out, code}, {out, code}}
	}
	const (
		unprovable = "gitcalver: local history cannot prove workspace state"
		dirty      = "gitcalver: workspace is dirty; use --dirty to allow"
	)
	// Inside the git directory of a repository that has a working tree, the
	// working tree cannot be inspected, so only an explicit target works.
	insideGitDir := func(explicit string) [2]discoveryOutcome {
		return [2]discoveryOutcome{{unprovable, 4}, {explicit, 0}}
	}

	// results are {omitted target, explicit HEAD}.
	for _, tc := range []struct {
		name    string
		dir     string
		args    []string
		results [2]discoveryOutcome
	}{
		{"worktree root", mainDir, nil, same("20260410.2", 0)},
		{"worktree subdirectory", sub, nil, same("20260410.2", 0)},
		{"git directory", filepath.Join(mainDir, ".git"), nil, insideGitDir("20260410.2")},
		{
			"git directory with --dirty",
			filepath.Join(mainDir, ".git"),
			[]string{"--dirty", "-dirty"},
			insideGitDir("20260410.2"),
		},
		{"git refs/heads", filepath.Join(mainDir, ".git", "refs", "heads"), nil, insideGitDir("20260410.2")},
		{"git objects", filepath.Join(mainDir, ".git", "objects"), nil, insideGitDir("20260410.2")},

		{"linked worktree root", linked, nil, same("20260410.1", 0)},
		{"linked worktree subdirectory", linkedSub, nil, same("20260410.1", 0)},
		// The linked worktree's own HEAD, not the main worktree's.
		{
			"linked worktree git directory",
			filepath.Join(mainDir, ".git", "worktrees", "linked"), nil, insideGitDir("20260410.1"),
		},

		{"bare root", bare, nil, same("20260410.2", 0)},
		{"bare refs/heads", filepath.Join(bare, "refs", "heads"), nil, same("20260410.2", 0)},
		{"bare objects", filepath.Join(bare, "objects"), nil, same("20260410.2", 0)},
		{"bare core.bare unset", bareUnset, nil, same("20260410.2", 0)},
		{"bare core.bare yes", bareYes, nil, same("20260410.2", 0)},
		{"bare core.bare false", bareFalse, nil, insideGitDir("20260410.2")},

		// The nested repository wins over the repository around it.
		{"nested bare root", nestedBare, nil, same("20260411.3", 0)},
		{"nested bare refs/heads", filepath.Join(nestedBare, "refs", "heads"), nil, same("20260411.3", 0)},

		{"bare container root", container, nil, same("20260410.2", 0)},
		{"bare container git directory", filepath.Join(container, ".bare"), nil, same("20260410.2", 0)},
		{"bare container worktree", containerWorktree, nil, [2]discoveryOutcome{{dirty, 2}, {"20260410.2", 0}}},
		{
			"bare container worktree git directory",
			filepath.Join(container, ".bare", "worktrees", "wt"), nil, same("20260410.2", 0),
		},
		{"core.bare=true with a .git directory", bareFlagged, nil, same("20260410.2", 0)},

		{"stray HEAD only", filepath.Join(stray, "head-only"), nil, same("20260410.2", 0)},
		{"stray HEAD and objects", filepath.Join(stray, "head-objects"), nil, same("20260410.2", 0)},
		{"stray HEAD and refs", filepath.Join(stray, "head-refs"), nil, same("20260410.2", 0)},
		{"stray config only", filepath.Join(stray, "config-only"), nil, same("20260410.2", 0)},
		{"stray commondir only", filepath.Join(stray, "commondir-only"), nil, same("20260410.2", 0)},
		{"commondir without objects", filepath.Join(stray, "commondir-without-objects"), nil, same("20260410.2", 0)},
		{"garbage HEAD", filepath.Join(stray, "garbage-head"), nil, same("20260410.2", 0)},
		{"non-hex detached HEAD", filepath.Join(stray, "non-hex-head"), nil, same("20260410.2", 0)},
		{"HEAD ref outside refs", filepath.Join(stray, "head-outside-ref"), nil, same("20260410.2", 0)},
		{"empty .git directory", filepath.Join(stray, "empty-dot-git"), nil, same("20260410.2", 0)},
		{".git directory without refs", filepath.Join(stray, "dot-git-without-refs"), nil, same("20260410.2", 0)},
		{".git directory with a garbage HEAD", filepath.Join(stray, "dot-git-garbage-head"), nil, same("20260410.2", 0)},

		{"empty bare repository", filepath.Join(stray, "empty-bare"), nil, same("gitcalver: no commits in repository", 1)},
		{
			"detached HEAD at a missing commit",
			filepath.Join(stray, "detached-missing"), nil,
			same("gitcalver: HEAD commit is missing from local history", 4),
		},
		{"invalid .git file", filepath.Join(stray, "bad-gitfile"), nil, same("gitcalver: not a git repository", 1)},
		{
			"unreadable commondir",
			filepath.Join(stray, "commondir-directory"), nil, same("gitcalver: not a git repository", 1),
		},
		{"linked worktree with a missing common directory", brokenLinked, nil, same("gitcalver: not a git repository", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, code := runCmd(t, tc.dir, tc.args...)
			assertEqual(t, tc.results[0], discoveryOutcome{out, code})
			out, code = runCmd(t, tc.dir, append([]string{"HEAD"}, tc.args...)...)
			assertEqual(t, tc.results[1], discoveryOutcome{out, code})
		})
	}
}

func TestMalformedGitMetadata(t *testing.T) {
	t.Parallel()
	mainDir, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")
	commitAt("2026-04-10T10:00:00Z")

	write := func(text string, elem ...string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(elem...), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// git refuses every worktree command once one of them is damaged, so
	// create them all before damaging any.
	worktrees := t.TempDir()
	for _, name := range []string{"crlf", "empty-commondir", "newline-commondir", "empty-gitdir", "no-space", "indented"} {
		gitCLI(t, "-C", mainDir, "worktree", "add", "--detach", filepath.Join(worktrees, name), "HEAD~1")
	}
	adminDir := func(name string) string {
		return filepath.Join(mainDir, ".git", "worktrees", name)
	}
	write("../..\r\n", adminDir("crlf"), "commondir")
	write("gitdir: "+adminDir("crlf")+"\r\n", worktrees, "crlf", ".git")
	write("", adminDir("empty-commondir"), "commondir")
	write("\n", adminDir("newline-commondir"), "commondir")
	write("gitdir: \n", worktrees, "empty-gitdir", ".git")
	write("gitdir:"+adminDir("no-space")+"\n", worktrees, "no-space", ".git")
	write("  gitdir: "+adminDir("indented")+"\n", worktrees, "indented", ".git")

	stray := filepath.Join(mainDir, "stray")
	if err := os.Mkdir(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	write("ref: refs/heads/main\n", stray, "HEAD")
	write("", stray, "commondir")

	// git passes over a directory whose objects is not a directory.
	objectsFile := filepath.Join(mainDir, "objects-file")
	if err := os.MkdirAll(filepath.Join(objectsFile, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("ref: refs/heads/main\n", objectsFile, "HEAD")
	write("", objectsFile, "objects")

	refused := discoveryOutcome{"gitcalver: not a git repository", 1}
	for _, tc := range []struct {
		name string
		dir  string
		want discoveryOutcome
	}{
		{"CRLF line endings", filepath.Join(worktrees, "crlf"), discoveryOutcome{"20260410.1", 0}},
		{"empty commondir in a linked worktree", filepath.Join(worktrees, "empty-commondir"), refused},
		{"commondir holding only a newline", filepath.Join(worktrees, "newline-commondir"), refused},
		{"gitdir without a path", filepath.Join(worktrees, "empty-gitdir"), refused},
		{"gitdir without a space", filepath.Join(worktrees, "no-space"), refused},
		{"gitdir after whitespace", filepath.Join(worktrees, "indented"), refused},
		{"empty commondir in a stray directory", stray, refused},
		{"objects is a file", objectsFile, discoveryOutcome{"20260410.2", 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, code := runCmd(t, tc.dir, "HEAD")
			assertEqual(t, tc.want, discoveryOutcome{out, code})
		})
	}
}

func makeFIFO(t *testing.T, path string) {
	t.Helper()
	if output, err := exec.Command("mkfifo", path).CombinedOutput(); err != nil {
		t.Fatalf("mkfifo: %v: %s", err, output)
	}
}

func TestGitEntryNotRegular(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeFIFO(t, filepath.Join(dir, ".git"))

	// Reading a FIFO blocks until something writes to it.
	result := make(chan error, 1)
	go func() {
		_, _, err := gitDirAt(dir)
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, errInvalidGitFile) {
			t.Fatalf("expected an invalid .git file error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gitDirAt blocked reading a .git FIFO")
	}
}

func TestValidHEADNotRegular(t *testing.T) {
	t.Parallel()
	head := filepath.Join(t.TempDir(), "HEAD")
	makeFIFO(t, head)

	// Reading a FIFO blocks until something writes to it.
	result := make(chan bool, 1)
	go func() { result <- validHEAD(head) }()
	select {
	case valid := <-result:
		assertEqual(t, false, valid)
	case <-time.After(5 * time.Second):
		t.Fatal("validHEAD blocked reading a HEAD FIFO")
	}
}

func TestGitEntryWorkspace(t *testing.T) {
	t.Parallel()
	source, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	// Git applies core.bare through a .git entry only when the config has a
	// repositoryformatversion.
	unversioned := filepath.Join(t.TempDir(), "unversioned")
	gitCLI(t, "clone", source, unversioned)
	unversionedConfig := filepath.Join(unversioned, ".git", "config")
	gitCLI(t, "config", "--file", unversionedConfig, "core.bare", "true")
	gitCLI(t, "config", "--file", unversionedConfig, "--unset", "core.repositoryformatversion")

	// A .git file that names its own directory makes it the git directory and
	// the working tree, so the repository's own files are untracked.
	selfNamed := filepath.Join(t.TempDir(), "self-named")
	gitCLI(t, "clone", "--bare", source, selfNamed)
	gitCLI(t, "config", "--file", filepath.Join(selfNamed, "config"), "core.bare", "false")
	if err := os.WriteFile(filepath.Join(selfNamed, ".git"), []byte("gitdir: .\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirty := discoveryOutcome{"gitcalver: workspace is dirty; use --dirty to allow", 2}
	for _, tc := range []struct{ name, dir string }{
		{"core.bare without a format version", unversioned},
		{"git file naming its own directory", selfNamed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := os.WriteFile(filepath.Join(tc.dir, "untracked.txt"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			out, code := runCmd(t, tc.dir)
			assertEqual(t, dirty, discoveryOutcome{out, code})
			out, code = runCmd(t, tc.dir, "HEAD")
			assertEqual(t, discoveryOutcome{"20260410.1", 0}, discoveryOutcome{out, code})
		})
	}
}

func TestSymlinkedWorkingDirectory(t *testing.T) {
	t.Parallel()
	outer, outerCommitAt := testRepo(t)
	outerCommitAt("2026-04-01T09:00:00Z")
	inner, innerCommitAt := testRepo(t)
	innerCommitAt("2026-04-02T09:00:00Z")
	innerCommitAt("2026-04-02T10:00:00Z")
	sub := filepath.Join(inner, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(outer, "link")
	if err := os.Symlink(sub, link); err != nil {
		t.Fatal(err)
	}

	out, code := runCmd(t, link, "HEAD")
	assertEqual(t, 0, code)
	assertEqual(t, "20260402.2", out)
}

func TestGitBool(t *testing.T) {
	t.Parallel()
	// Words, and integers as strtoimax reads them with base 0, where a unit
	// multiplies the number and the product must fit a C int.
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"true", true},
		{"TRUE", true},
		{"Yes", true},
		{"oN", true},
		{"false", false},
		{"No", false},
		{"OFF", false},

		{"1", true},
		{"2", true},
		{"-1", true},
		{"+1", true},
		{"0", false},
		{"-0", false},
		{"00", false},
		{"0x0", false},
		{"0x1", true},
		{"0XfF", true},
		{"01", true},
		{"07", true},
		{" 1", true},
		{"\t\v\f\r\n1", true},

		{"1k", true},
		{"1K", true},
		{"1m", true},
		{"1G", true},
		{"0x1g", true},
		{"0k", false},
		{"-0g", false},
		{"2147483647", true},
		{"-2147483648", true},
		{"2097151k", true},
		{"-2097152k", true},
		{"2047m", true},
		{"-2g", true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Parallel()
			got, err := gitBool(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			assertEqual(t, tc.want, got)
		})
	}

	for _, value := range []string{
		"maybe", "t", "tru", "true false", " true",
		"08", "0x", "0b1", "0o1", "1_0", "1.0", "1e3", "1 ", "-",
		"1kb", "1gg", "1 k", "k", "0xk",
		"2147483648", "-2147483649", "0xffffffff", "99999999999999999999",
		"2097152k", "-2097153k", "2048m", "2g", "-3g",
		// go-git reads a key written without "=", which git takes as true, as
		// an empty value, which git takes as false.
		"",
	} {
		t.Run("invalid "+value, func(t *testing.T) {
			t.Parallel()
			if _, err := gitBool(value); !errors.Is(err, errBadBool) {
				t.Fatalf("expected a bad boolean error, got %v", err)
			}
		})
	}
}

func TestCoreBareSettings(t *testing.T) {
	t.Parallel()
	source, commitAt := testRepo(t)
	commitAt("2026-04-10T09:00:00Z")

	for _, tc := range []struct {
		name, settings, out string
		code                int
	}{
		{"nonzero integer", "\tbare = 2\n", "20260410.1", 0},
		{"hexadecimal", "\tbare = 0x1\n", "20260410.1", 0},
		{"malformed", "\tbare = maybe\n", "gitcalver: not a git repository", 1},
		{"malformed before a valid setting", "\tbare = maybe\n\tbare = true\n", "gitcalver: not a git repository", 1},
		{"without a value", "\tbare\n", "gitcalver: not a git repository", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			checkout := filepath.Join(t.TempDir(), "checkout")
			gitCLI(t, "clone", source, checkout)
			// Untracked, so the workspace is dirty unless core.bare takes it away.
			if err := os.WriteFile(filepath.Join(checkout, "untracked.txt"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			bare := filepath.Join(t.TempDir(), "bare.git")
			gitCLI(t, "clone", "--bare", source, bare)

			for _, layout := range []struct{ name, dir, config string }{
				{"checkout", checkout, filepath.Join(checkout, ".git", "config")},
				{"bare", bare, filepath.Join(bare, "config")},
			} {
				config := "[core]\n\trepositoryformatversion = 0\n" + tc.settings
				if err := os.WriteFile(layout.config, []byte(config), 0o644); err != nil {
					t.Fatal(err)
				}
				t.Run(layout.name, func(t *testing.T) {
					out, code := runCmd(t, layout.dir)
					assertEqual(t, discoveryOutcome{tc.out, tc.code}, discoveryOutcome{out, code})
				})
			}
		})
	}
}

func TestValidHEAD(t *testing.T) {
	t.Parallel()

	oid := strings.Repeat("a", 40)
	write := func(text string) func(string) error {
		return func(head string) error { return os.WriteFile(head, []byte(text), 0o600) }
	}
	link := func(target string) func(string) error {
		return func(head string) error { return os.Symlink(target, head) }
	}
	for _, tc := range []struct {
		name  string
		setup func(head string) error
		want  bool
	}{
		{"symbolic ref", write("ref: refs/heads/main\n"), true},
		{"symbolic ref without newline", write("ref: refs/heads/main"), true},
		{"no space after colon", write("ref:refs/heads/main\n"), true},
		{"git whitespace after colon", write("ref:\t\r\n refs/heads/main\n"), true},
		{"ref outside refs", write("ref: heads/main\n"), false},
		{"vertical tab after colon", write("ref:\vrefs/heads/main\n"), false},
		{"form feed after colon", write("ref:\frefs/heads/main\n"), false},
		{"non-breaking space after colon", write("ref:\u00a0refs/heads/main\n"), false},
		{"refs/ ends at byte 255", write("ref:" + strings.Repeat(" ", 246) + "refs/heads/main\n"), true},
		{"refs/ cut off at byte 255", write("ref:" + strings.Repeat(" ", 247) + "refs/heads/main\n"), false},
		{"detached object ID", write(oid + "\n"), true},
		{"upper-case object ID", write(strings.ToUpper(oid)), true},
		{"object ID followed by text", write(oid + " detached\n"), true},
		{"short object ID", write(oid[:39] + "\n"), false},
		{"non-hex object ID", write(strings.Repeat("z", 40) + "\n"), false},
		{"empty file", write(""), false},
		{"missing", func(string) error { return nil }, false},
		{"directory", func(head string) error { return os.Mkdir(head, 0o755) }, false},
		{"unreadable file", func(head string) error {
			return os.WriteFile(head, []byte("ref: refs/heads/main\n"), 0o000)
		}, false},
		{"symlink into refs", link("refs/heads/main"), true},
		{"symlink outside refs", link("./refs/heads/main"), false},
		{"symlink to endless file", link("/dev/zero"), false},
		{"symlink to file holding a ref", func(head string) error {
			other := filepath.Join(filepath.Dir(head), "other")
			if err := os.WriteFile(other, []byte("ref: refs/heads/main\n"), 0o600); err != nil {
				return err
			}
			return os.Symlink(other, head)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			head := filepath.Join(t.TempDir(), "HEAD")
			if err := tc.setup(head); err != nil {
				t.Fatal(err)
			}
			assertEqual(t, tc.want, validHEAD(head))
		})
	}
}

// --- The dirty check never writes to the repository ---

// snapshotTree maps every path under dir to its mode and a hash of its content,
// or to its link target.
func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, linkErr := os.Readlink(path)
			snapshot[rel] = "link " + target
			return linkErr
		case info.IsDir():
			snapshot[rel] = "dir " + info.Mode().String()
		default:
			data, readErr := os.ReadFile(path)
			sum := sha256.Sum256(data)
			snapshot[rel] = fmt.Sprintf("%s %x", info.Mode(), sum)
			return readErr
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func describeTreeChanges(before, after map[string]string) string {
	var lines []string
	for _, path := range slices.Sorted(maps.Keys(after)) {
		if old, ok := before[path]; !ok {
			lines = append(lines, "+ "+path)
		} else if old != after[path] {
			lines = append(lines, "~ "+path)
		}
	}
	for _, path := range slices.Sorted(maps.Keys(before)) {
		if _, ok := after[path]; !ok {
			lines = append(lines, "- "+path)
		}
	}
	return strings.Join(lines, "\n")
}

// submoduleRepos is a clone, super, of a repository whose main branch has two
// submodules, "sub" and "other", neither initialized in the clone. root holds
// everything the fixture creates, so a snapshot of it covers super and any
// linked worktree made from it. upstream holds the submodules' repositories,
// which every fixture shares.
type submoduleRepos struct {
	root, super, upstream string
	git                   func(dir string, args ...string)
}

func gitInDir(t *testing.T) func(dir string, args ...string) {
	t.Helper()
	return func(dir string, args ...string) {
		t.Helper()
		gitCLI(t, append([]string{
			"-C", dir,
			"-c", "user.name=Test", "-c", "user.email=test@test.com",
			"-c", "protocol.file.allow=always",
		}, args...)...)
	}
}

// newSubmoduleOrigin makes the repository that submoduleRepos clones, and the
// repositories of its submodules.
func newSubmoduleOrigin(t *testing.T) (origin, upstream string) {
	t.Helper()
	run := gitInDir(t)
	root := t.TempDir()
	upstream = filepath.Join(root, "upstream")
	origin = filepath.Join(root, "origin")
	gitCLI(t, "init", "-q", "-b", "main", origin)
	run(origin, "commit", "-q", "--allow-empty", "-m", "base")
	for _, name := range []string{"sub", "other"} {
		repo := filepath.Join(upstream, name)
		gitCLI(t, "init", "-q", "-b", "main", repo)
		run(repo, "commit", "-q", "--allow-empty", "-m", name)
		run(origin, "submodule", "add", "-q", repo, name)
	}
	run(origin, "commit", "-q", "-m", "add submodules")
	return origin, upstream
}

func newSubmoduleRepos(t *testing.T, origin, upstream string) submoduleRepos {
	t.Helper()
	run := gitInDir(t)
	root := t.TempDir()
	super := filepath.Join(root, "super")
	run(root, "clone", "-q", origin, super)
	return submoduleRepos{root: root, super: super, upstream: upstream, git: run}
}

func TestDirtyCheckLeavesRepositoryUnchanged(t *testing.T) {
	t.Parallel()
	origin, upstream := newSubmoduleOrigin(t)
	mustWrite := func(t *testing.T, path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		// setup returns the directory to run gitcalver in.
		setup func(t *testing.T, r submoduleRepos) string
		want  int
	}{
		{"submodules not initialized", func(_ *testing.T, r submoduleRepos) string {
			return r.super
		}, 0},
		{"submodule initialized but not cloned", func(_ *testing.T, r submoduleRepos) string {
			r.git(r.super, "submodule", "init", "sub")
			return r.super
		}, 0},
		{"every submodule initialized but not cloned", func(_ *testing.T, r submoduleRepos) string {
			r.git(r.super, "submodule", "init")
			return r.super
		}, 0},
		{"initialized but not cloned, workspace dirty", func(t *testing.T, r submoduleRepos) string {
			t.Helper()
			r.git(r.super, "submodule", "init", "sub")
			mustWrite(t, filepath.Join(r.super, "untracked.txt"), "x")
			return r.super
		}, 2},
		{"initialized, empty modules directory", func(t *testing.T, r submoduleRepos) string {
			t.Helper()
			r.git(r.super, "submodule", "init", "sub")
			if err := os.MkdirAll(filepath.Join(r.super, ".git", "modules", "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			return r.super
		}, 0},
		{"initialized, unparsable URL", func(_ *testing.T, r submoduleRepos) string {
			r.git(r.super, "submodule", "init", "sub")
			r.git(r.super, "config", "submodule.sub.url", "http://[::1")
			return r.super
		}, 0},
		{"initialized in a linked worktree", func(_ *testing.T, r submoduleRepos) string {
			linked := filepath.Join(r.root, "linked")
			r.git(r.super, "worktree", "add", "-q", "--detach", linked, "HEAD")
			r.git(linked, "submodule", "init", "sub")
			return linked
		}, 0},
		{"one submodule cloned, one initialized only", func(_ *testing.T, r submoduleRepos) string {
			r.git(r.super, "submodule", "init")
			r.git(r.super, "submodule", "update", "sub")
			return r.super
		}, 0},
		{"cloned", func(_ *testing.T, r submoduleRepos) string {
			r.git(r.super, "submodule", "update", "--init", "sub")
			return r.super
		}, 0},
		{"cloned at a new commit", func(_ *testing.T, r submoduleRepos) string {
			r.git(r.super, "submodule", "update", "--init", "sub")
			r.git(filepath.Join(r.super, "sub"), "commit", "-q", "--allow-empty", "-m", "newer")
			return r.super
		}, 2},
		{"cloned, directory removed", func(t *testing.T, r submoduleRepos) string {
			t.Helper()
			r.git(r.super, "submodule", "update", "--init", "sub")
			if err := os.RemoveAll(filepath.Join(r.super, "sub")); err != nil {
				t.Fatal(err)
			}
			return r.super
		}, 2},
		{"deinitialized", func(_ *testing.T, r submoduleRepos) string {
			r.git(r.super, "submodule", "update", "--init", "sub")
			r.git(r.super, "submodule", "deinit", "-q", "sub")
			return r.super
		}, 0},
		{"repository inside the submodule directory", func(_ *testing.T, r submoduleRepos) string {
			r.git(r.super, "submodule", "init", "sub")
			r.git(r.root, "clone", "-q", filepath.Join(r.upstream, "sub"), filepath.Join(r.super, "sub"))
			return r.super
		}, 4},
		{"gitfile of a removed module repository", func(t *testing.T, r submoduleRepos) string {
			t.Helper()
			r.git(r.super, "submodule", "update", "--init", "sub")
			if err := os.RemoveAll(filepath.Join(r.super, ".git", "modules")); err != nil {
				t.Fatal(err)
			}
			return r.super
		}, 4},
		{"malformed .gitmodules", func(t *testing.T, r submoduleRepos) string {
			t.Helper()
			mustWrite(t, filepath.Join(r.super, ".gitmodules"), "[submodule \"sub\"\n")
			return r.super
		}, 4},
		{"submodule name escaping the git directory", func(t *testing.T, r submoduleRepos) string {
			t.Helper()
			gitmodules := filepath.Join(r.super, ".gitmodules")
			data, err := os.ReadFile(gitmodules)
			if err != nil {
				t.Fatal(err)
			}
			mustWrite(t, gitmodules, strings.Replace(string(data), `[submodule "sub"]`, `[submodule "../../sub"]`, 1))
			config, err := os.OpenFile(filepath.Join(r.super, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer config.Close()
			if _, err = config.WriteString("[submodule \"../../sub\"]\n\turl = /nonexistent\n"); err != nil {
				t.Fatal(err)
			}
			return r.super
		}, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repos := newSubmoduleRepos(t, origin, upstream)
			dir := tc.setup(t, repos)

			before := snapshotTree(t, repos.root)
			out, code := runCmd(t, dir)
			if changes := describeTreeChanges(before, snapshotTree(t, repos.root)); changes != "" {
				t.Errorf("dirty check wrote to the repository:\n%s", changes)
			}
			if code != tc.want {
				t.Errorf("exit code %d, want %d: %s", code, tc.want, out)
			}
		})
	}
}

// --- Helpers ---

func openRepositoryAt(dir string) (*git.Repository, error) {
	dirs, err := findGitDirs(dir)
	if err != nil {
		return nil, err
	}
	repo, _, err := openRepository(dirs)
	return repo, err
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

// gitCLI runs git without the caller's environment: GIT_* variables inherited
// from a hook point git at another repository, and global configuration such
// as hooks changes what fixture commands do. It creates SHA-1 repositories with
// the files ref format, the only formats go-git reads, unless told otherwise.
// Background maintenance is off: it takes locks such as objects/maintenance.lock
// after a command returns, which snapshot comparisons would see as writes.
func gitCLI(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "GIT_") })
	cmd.Env = append(cmd.Env,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=maintenance.auto",
		"GIT_CONFIG_VALUE_0=false",
		"GIT_DEFAULT_HASH=sha1",
		"GIT_DEFAULT_REF_FORMAT=files",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

// setObjectFormat writes through --file: git refuses to run inside a
// repository with some of these version and format combinations.
func setObjectFormat(t *testing.T, configPath, version, format string) {
	t.Helper()
	gitCLI(t, "config", "--file", configPath, "extensions.objectformat", format)
	if version != "" {
		gitCLI(t, "config", "--file", configPath, "core.repositoryformatversion", version)
	}
}

type repoLayouts struct {
	worktree, nested, linked, bare, partial string
}

type repoLayout struct {
	name, dir string
}

func (l repoLayouts) all() []repoLayout {
	return []repoLayout{
		{"worktree", l.worktree},
		{"nested", l.nested},
		{"linked worktree", l.linked},
		{"bare", l.bare},
		{"partial clone", l.partial},
	}
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
