// Copyright © 2026 Michael Shields
// SPDX-License-Identifier: MIT

// Package gitcalver computes calendar-based version strings from git history.
package gitcalver

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	cfgformat "github.com/go-git/go-git/v5/plumbing/format/config"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
	"github.com/go-git/go-git/v5/storage/memory"
)

const (
	exitError             = 1
	exitDirty             = 2
	exitWrongBranch       = 3
	exitIncompleteHistory = 4

	dateFormat = "20060102"

	// hexObjectIDLength is the length of a SHA-1 object ID in hex. A longer
	// SHA-256 ID starts with as many hex digits.
	hexObjectIDLength = 40

	// headReadLimit is how much of HEAD git reads when it tests for a git
	// directory.
	headReadLimit = 255
)

var versionRe = regexp.MustCompile(`^(\d{8})\.([1-9]\d*)$`)

var (
	errInvalidGitFile      = errors.New("invalid .git file")
	errEmptyCommonDir      = errors.New("empty commondir file")
	errSubmoduleRepository = errors.New("submodule repository is not under .git/modules")
	errSubmoduleSymlink    = errors.New("submodule path is a symbolic link")
	errSubmoduleNotInit    = errors.New("populated submodule is not initialized")
)

var errBadBool = errors.New("bad boolean config value")

// gitIntRe is the integer grammar of strtoimax with base 0, which git applies
// to a boolean that is not a word: white space, a sign, a hexadecimal, octal,
// or decimal number, and a unit.
var gitIntRe = regexp.MustCompile(`^[ \t-\r]*([+-]?(?:0[xX][0-9a-fA-F]+|0[0-7]*|[1-9][0-9]*))([kKmMgG]?)$`)

// gitIntBits is how many bits a number may have before its unit multiplies it
// by 1<<10, 1<<20, or 1<<30 into a C int.
var gitIntBits = map[string]int{"": 32, "k": 22, "m": 12, "g": 2}

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
	repo      *git.Repository
	history   *history
	headHash  plumbing.Hash
	worktree  *git.Worktree
	workspace workspaceKind
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

	repo, workspace, err := openRepository(dirs)
	if errors.Is(err, git.ErrSHA256NotSupported) {
		return nil, &ExitError{exitError, "SHA-256 repositories are not supported"}
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
		repo:      repo,
		history:   h,
		headHash:  headRef.Hash(),
		worktree:  worktree,
		workspace: workspace,
	}, nil
}

// compatStorage presents a repository to go-git v5 as one it can open: git
// accepts configurations go-git rejects, such as partial clones and an explicit
// sha1 object format, while SHA-256 repositories need a clear refusal.
// Embedding *filesystem.Storage keeps the optional methods go-git finds by type
// assertion, such as HashesWithPrefix, which makes short-hash lookup fast.
type compatStorage struct {
	*filesystem.Storage
}

func newCompatStorage(fs billy.Filesystem) *compatStorage {
	// An alternates file names absolute paths, which a filesystem rooted at the
	// git directory cannot reach.
	return &compatStorage{filesystem.NewStorageWithOptions(
		fs, cache.NewObjectLRUDefault(), filesystem.Options{AlternatesFS: osfs.New("/")},
	)}
}

// Module gives submodules the same treatment as the superproject. A submodule
// that was never cloned has no module directory, and go-git would create one
// while reading the status. Git counts such a submodule clean, so it gets an
// in-memory repository with an unborn HEAD, which go-git opens without writing.
func (s *compatStorage) Module(name string) (storage.Storer, error) {
	moduleFS, err := dotgit.New(s.Filesystem()).Module(name)
	if err != nil {
		return nil, err
	}
	module := newCompatStorage(moduleFS)
	if _, err = module.Reference(plumbing.HEAD); errors.Is(err, plumbing.ErrReferenceNotFound) {
		uncloned := memory.NewStorage()
		uncloned.ReferenceStorage[plumbing.HEAD] = plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.Master)
		return uncloned, nil
	}
	return module, nil
}

type submoduleTree struct {
	path     string
	repo     *git.Repository
	worktree *git.Worktree
}

// worktreeDirty reports whether the working tree has changes. go-git's status
// compares a submodule's HEAD with the index but never looks inside its working
// tree, so worktreeDirty also descends into every populated submodule, nested
// ones included. Like git status, which fails when it cannot read part of the
// tree, it returns an error for such a part even when another part is dirty.
func worktreeDirty(repo *git.Repository, worktree *git.Worktree) (bool, error) {
	idx, err := repo.Storer.Index()
	if err != nil {
		return false, err
	}
	submodules, replaced, err := populatedSubmodules(worktree, idx)
	if err != nil {
		return false, err
	}
	status, err := worktree.Status()
	if err != nil {
		return false, err
	}
	dirty := replaced || !status.IsClean() || hasUnmerged(idx)
	for _, submodule := range submodules {
		submoduleDirty, dirtyErr := worktreeDirty(submodule.repo, submodule.worktree)
		if dirtyErr != nil {
			return false, fmt.Errorf("submodule %s: %w", submodule.path, dirtyErr)
		}
		dirty = dirty || submoduleDirty
	}
	return dirty, nil
}

// hasUnmerged reports an unresolved merge conflict, which go-git's status does
// not count as a change. A merged entry has stage 0; go-git's index.Merged is 1,
// the ancestor stage of a conflict.
func hasUnmerged(idx *index.Index) bool {
	return slices.ContainsFunc(idx.Entries, func(entry *index.Entry) bool {
		return entry.Stage != 0
	})
}

// populatedSubmodules returns each submodule whose directory holds a .git
// entry, which is how git tells a populated submodule from an empty one, and
// whether a submodule's path has been replaced by something other than a
// directory, which is a change. Only a path with a gitlink in the index is a
// submodule, whatever .gitmodules lists. A populated submodule that go-git has
// no module repository for would read as clean, which is wrong when its
// directory holds a repository of its own: an embedded one, or a gitfile that
// points nowhere. Git refuses a symbolic link in place of a submodule.
func populatedSubmodules(
	worktree *git.Worktree, idx *index.Index,
) (populated []submoduleTree, replaced bool, err error) {
	submodules, err := worktree.Submodules()
	if err != nil || len(submodules) == 0 {
		return nil, false, err
	}
	// go-git lists submodules in map order.
	slices.SortFunc(submodules, func(a, b *git.Submodule) int {
		return strings.Compare(a.Config().Path, b.Config().Path)
	})
	for _, submodule := range submodules {
		dir := submodule.Config().Path
		if entry, entryErr := idx.Entry(dir); entryErr != nil || entry.Mode != filemode.Submodule {
			continue
		}
		submoduleRepo, repoErr := submodule.Repository()
		if repoErr != nil && !errors.Is(repoErr, git.ErrSubmoduleNotInitialized) {
			return nil, false, fmt.Errorf("submodule %s: %w", dir, repoErr)
		}
		info, present, statErr := lstatPresent(worktree.Filesystem, dir)
		if statErr != nil {
			return nil, false, fmt.Errorf("submodule %s: %w", dir, statErr)
		}
		if !present {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, false, fmt.Errorf("submodule %s: %w", dir, errSubmoduleSymlink)
		}
		if !info.IsDir() {
			replaced = true
			continue
		}
		_, hasGit, statErr := lstatPresent(worktree.Filesystem, path.Join(dir, ".git"))
		if statErr != nil {
			return nil, false, fmt.Errorf("submodule %s: %w", dir, statErr)
		}
		if !hasGit {
			continue
		}
		if repoErr != nil {
			return nil, false, fmt.Errorf("submodule %s: %w", dir, errSubmoduleNotInit)
		}
		submoduleWorktree, _ := submoduleRepo.Worktree() //nolint:errcheck // the repository was opened with a working tree
		// The stand-in for a submodule that was never cloned is not a compatStorage.
		module, isModule := submoduleRepo.Storer.(*compatStorage)
		if !isModule || !namesModule(submoduleWorktree.Filesystem.Root(), module) {
			return nil, false, fmt.Errorf("submodule %s: %w", dir, errSubmoduleRepository)
		}
		if err = checkReadable(module.Filesystem(), "info/exclude"); err != nil {
			return nil, false, fmt.Errorf("submodule %s: %w", dir, err)
		}
		submoduleWorktree.Filesystem = excludeFS{submoduleWorktree.Filesystem, module.Filesystem()}
		populated = append(populated, submoduleTree{dir, submoduleRepo, submoduleWorktree})
	}
	return populated, replaced, nil
}

// lstatPresent is Lstat for a path that may be absent: one that does not exist,
// or lies below something that is not a directory, is not an error.
func lstatPresent(fsys billy.Filesystem, name string) (info os.FileInfo, present bool, err error) {
	info, err = fsys.Lstat(name)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil, false, nil
	}
	return info, err == nil, err
}

// excludeFS serves a repository's exclude file to go-git, which looks for it at
// .git/info/exclude in the working tree. A linked worktree or a submodule has
// only a gitfile there, and the file lives in the git directory the gitfile
// names. Served from there it ranks below the .gitignore files, as in git;
// Worktree.Excludes would rank above them.
type excludeFS struct {
	billy.Filesystem

	gitDir billy.Filesystem
}

func (f excludeFS) Open(name string) (billy.File, error) {
	if filepath.ToSlash(name) == ".git/info/exclude" {
		return f.gitDir.Open("info/exclude")
	}
	return f.Filesystem.Open(name)
}

// checkReadable reports a file that exists but cannot be opened. go-git treats
// an exclude file like that as absent.
func checkReadable(fsys billy.Filesystem, name string) error {
	file, err := fsys.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return file.Close()
}

// namesModule reports whether the .git entry in dir leads to the module
// repository go-git reads for the submodule. Git follows the entry itself, so a
// gitfile that names another repository gets that repository's status.
func namesModule(dir string, module *compatStorage) bool {
	dirs, _, err := gitDirAt(dir)
	if err != nil {
		return false
	}
	named, namedErr := os.Stat(dirs.gitDir)
	read, readErr := os.Stat(module.Filesystem().Root())
	return namedErr == nil && readErr == nil && os.SameFile(named, read)
}

func (s *compatStorage) Config() (*config.Config, error) {
	cfg, err := s.Storage.Config()
	if err != nil {
		return nil, err
	}
	if cfg.Raw == nil || !cfg.Raw.HasSection("extensions") {
		return cfg, nil
	}

	// go-git v5 leaves cfg.Extensions.ObjectFormat empty when reading a config,
	// so the raw options are the only place the object format appears.
	extensions := cfg.Raw.Section("extensions")
	format := cfgformat.ObjectFormat(extensions.Option("objectformat"))
	if format == cfgformat.SHA256 {
		return nil, git.ErrSHA256NotSupported
	}
	// Filesystem storage reads a fresh Config value on every call. Removing
	// extensions only from that in-memory value lets go-git inspect the
	// already-local object database without altering the repository or fetching.
	extensions.RemoveOption("partialClone")
	// go-git rejects every objectformat extension, though sha1 is the format it
	// reads. Git honors the extension only at repository format version 1 and
	// rejects it at version 0.
	if format == cfgformat.SHA1 &&
		cfg.Raw.Section("core").Option("repositoryformatversion") == cfgformat.Version_1 {
		extensions.RemoveOption("objectformat")
	}
	return cfg, nil
}

type workspaceKind int

const (
	workspaceNone        workspaceKind = iota // bare repository
	workspaceHere                             // working tree at gitDirectories.worktreeDir
	workspaceUnreachable                      // working tree exists but not from the search directory
)

// workspaceOf follows git's own rules. A repository found through a .git entry
// is bare only when its own config says core.bare is true, which linked
// worktrees (no config of their own) never do. Git reads that setting through
// a .git entry only from a config that has a repositoryformatversion. A
// repository found as the git directory itself is bare unless core.bare is
// explicitly false, in which case its working tree is out of reach.
func workspaceOf(dirs gitDirectories, cfg *config.Config) (workspaceKind, error) {
	core := cfg.Raw.Section("core")
	// Git rejects a malformed core.bare even when a later setting overrides it.
	settings := core.OptionAll("bare")
	isBare := false
	for _, setting := range settings {
		var err error
		if isBare, err = gitBool(setting); err != nil {
			return workspaceNone, fmt.Errorf("core.bare: %w", err)
		}
	}
	if dirs.inGitDir {
		if isBare || len(settings) == 0 {
			return workspaceNone, nil
		}
		return workspaceUnreachable, nil
	}
	if isBare && dirs.gitDir == dirs.commonDir && core.HasOption("repositoryformatversion") {
		return workspaceNone, nil
	}
	return workspaceHere, nil
}

// gitBool reads a boolean as git_config_bool does. Words are true or false in
// any case. Any other value must be an integer that fits a C int after its
// unit scales it, and is true unless it is zero.
func gitBool(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "true", "yes", "on":
		return true, nil
	case "false", "no", "off":
		return false, nil
	case "":
		// Git reads a key written without "=" as true but "key =" as false,
		// and go-git's decoder reports both as an empty value.
		return false, fmt.Errorf("%w: empty", errBadBool)
	}
	match := gitIntRe.FindStringSubmatch(value)
	if match == nil {
		return false, fmt.Errorf("%w: %q", errBadBool, value)
	}
	number, err := strconv.ParseInt(match[1], 0, gitIntBits[strings.ToLower(match[2])])
	if err != nil {
		return false, fmt.Errorf("%w: %q", errBadBool, value)
	}
	return number != 0, nil
}

func openRepository(dirs gitDirectories) (*git.Repository, workspaceKind, error) {
	repoFS := osfs.New(dirs.gitDir)
	if dirs.commonDir != dirs.gitDir {
		if _, err := os.Stat(dirs.commonDir); err != nil {
			return nil, workspaceNone, err
		}
		repoFS = dotgit.NewRepositoryFilesystem(repoFS, osfs.New(dirs.commonDir))
	}
	storer := newCompatStorage(repoFS)
	cfg, err := storer.Config()
	if err != nil {
		return nil, workspaceNone, err
	}
	workspace, err := workspaceOf(dirs, cfg)
	if err != nil {
		return nil, workspaceNone, err
	}
	if workspace != workspaceHere {
		repo, openErr := git.Open(storer, nil)
		return repo, workspace, openErr
	}
	commonFS := osfs.New(dirs.commonDir)
	if err = checkReadable(commonFS, "info/exclude"); err != nil {
		return nil, workspaceNone, err
	}
	repo, err := git.Open(storer, excludeFS{osfs.New(dirs.worktreeDir), commonFS})
	return repo, workspace, err
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
	if !targetSet && !offBranch {
		const unprovable = "local history cannot prove workspace state"
		if state.workspace == workspaceUnreachable {
			return "", &ExitError{exitIncompleteHistory, unprovable}
		}
		if state.worktree != nil {
			var statusErr error
			if workspaceDirty, statusErr = worktreeDirty(state.repo, state.worktree); statusErr != nil {
				return "", &ExitError{exitIncompleteHistory, unprovable + ": " + statusErr.Error()}
			}
		}
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

	date, count, err := cohortCount(state.history, anchor)
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

// cohortCount computes a commit's version date and N: the size of its date
// cohort, the set of commits reachable from it through any parent whose UTC
// committer date equals its own. It is a pruned BFS over all parents: a
// same-date parent is counted and traversed; a strictly older parent is
// counted as a boundary and not traversed past; a strictly newer parent
// means committer dates are not monotonic, which is rejected outright. Every
// commit the walk traverses shares startHash's date, and a parent is
// classified by its own date alone, so the classification cannot depend on
// which cohort member discovered it first.
func cohortCount(history *history, startHash plumbing.Hash) (string, int, error) {
	target, err := history.commit(startHash)
	if err != nil {
		return "", 0, &ExitError{exitIncompleteHistory, "local history ended inside the target date block"}
	}

	date := target.Committer.When.UTC().Format(dateFormat)
	visited := map[plumbing.Hash]struct{}{startHash: {}}
	cohort := []*object.Commit{target}

	for i := 0; i < len(cohort); i++ {
		parents, parentsErr := history.parents(cohort[i])
		if parentsErr != nil {
			return "", 0, &ExitError{
				exitIncompleteHistory,
				"local history ended inside the " + date + " date block",
			}
		}
		for _, parent := range parents {
			if _, seen := visited[parent.Hash]; seen {
				continue
			}
			visited[parent.Hash] = struct{}{}

			parentDate := parent.Committer.When.UTC().Format(dateFormat)
			switch {
			case parentDate == date:
				cohort = append(cohort, parent)
			case parentDate > date:
				return "", 0, dateWentBackwards(parentDate, date)
			default:
				// Strictly older: a boundary. Not counted, not traversed
				// past.
			}
		}
	}

	return date, len(cohort), nil
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

	targetHash, err := selectReverseCandidate(state.history, candidates, n, opts.Target)
	if err != nil {
		return "", err
	}
	if opts.Short {
		return objectIDPrefix(targetHash), nil
	}
	return targetHash.String(), nil
}

// selectReverseCandidate finds the date-block member whose date cohort has
// exactly n commits. candidates is newest-first, as collected by the
// first-parent block walk; cohort size strictly increases oldest-to-newest
// within a block, so this checks oldest-to-newest and stops as soon as the
// cohort size reaches or passes n. Sequences are sparse, because a cohort
// can grow by more than one through a second parent, so a miss is "not
// found" — never the nearest match.
func selectReverseCandidate(
	history *history, candidates []plumbing.Hash, n int, version string,
) (plumbing.Hash, error) {
	notFound := &ExitError{exitError, "version not found: " + version}

	for i := len(candidates) - 1; i >= 0; i-- {
		_, count, err := cohortCount(history, candidates[i])
		if err != nil {
			return plumbing.ZeroHash, err
		}
		switch {
		case count == n:
			return candidates[i], nil
		case count > n:
			return plumbing.ZeroHash, notFound
		}
	}

	return plumbing.ZeroHash, notFound
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
	inGitDir    bool // the search ended in the git directory itself, with no .git entry naming it
}

// findGitDirs follows git's discovery rules, so gitcalver reads the repository
// that git does. Like git it searches upward from the physical directory, not
// from a symlinked path whose parents belong to another repository.
func findGitDirs(dir string) (gitDirectories, error) {
	abs, _ := filepath.Abs(dir) //nolint:errcheck // all supported repository paths are local filesystem paths
	start, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return gitDirectories{}, os.ErrNotExist
	}
	if !isDir(start) {
		return gitDirectories{}, os.ErrNotExist
	}

	for current := start; ; current = filepath.Dir(current) {
		dirs, found, findErr := gitDirAt(current)
		if findErr != nil {
			return gitDirectories{}, findErr
		}
		if found {
			dirs.worktreeDir = current
			return dirs, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return gitDirectories{}, os.ErrNotExist
}

func commonDirOf(gitDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(gitDir, "commondir")) //nolint:gosec // repository metadata path
	if errors.Is(err, os.ErrNotExist) {
		return filepath.Clean(gitDir), nil
	}
	if err != nil {
		return "", err
	}
	common := strings.TrimRight(string(data), "\r\n")
	if common == "" {
		return "", errEmptyCommonDir
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDir, common)
	}
	return filepath.Clean(common), nil
}

func gitDirAt(dir string) (gitDirectories, bool, error) {
	dotGit := filepath.Join(dir, ".git")
	info, err := os.Stat(dotGit)
	if err == nil {
		if info.IsDir() {
			// Like git, pass over a .git directory that is not a git directory.
			dirs, found, dirErr := gitDirIfValid(dotGit)
			if found || dirErr != nil {
				dirs.inGitDir = false
				return dirs, found, dirErr
			}
			return gitDirIfValid(dir)
		}
		if !info.Mode().IsRegular() {
			return gitDirectories{}, false, errInvalidGitFile
		}
		data, readErr := os.ReadFile(dotGit) //nolint:gosec // repository metadata path
		if readErr != nil {
			return gitDirectories{}, false, readErr
		}
		value, ok := strings.CutPrefix(string(data), "gitdir: ")
		if !ok {
			return gitDirectories{}, false, errInvalidGitFile
		}
		gitDir := strings.TrimRight(value, "\r\n")
		if gitDir == "" {
			return gitDirectories{}, false, errInvalidGitFile
		}
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(dir, gitDir)
		}
		return namedGitDir(filepath.Clean(gitDir))
	}
	if !errors.Is(err, os.ErrNotExist) {
		return gitDirectories{}, false, err
	}

	return gitDirIfValid(dir)
}

func namedGitDir(gitDir string) (gitDirectories, bool, error) {
	commonDir, err := commonDirOf(gitDir)
	if err != nil {
		return gitDirectories{}, false, err
	}
	return gitDirectories{gitDir: gitDir, commonDir: commonDir}, true, nil
}

// gitDirIfValid applies git's is_git_directory test: a valid HEAD, plus
// objects/ and refs/ in the common directory, which a linked worktree's git
// directory names in its commondir file.
func gitDirIfValid(dir string) (gitDirectories, bool, error) {
	if !validHEAD(filepath.Join(dir, "HEAD")) {
		return gitDirectories{}, false, nil
	}
	common, err := commonDirOf(dir)
	if err != nil {
		return gitDirectories{}, false, err
	}
	for _, name := range []string{"objects", "refs"} {
		if !isDir(filepath.Join(common, name)) {
			return gitDirectories{}, false, nil
		}
	}
	return gitDirectories{gitDir: dir, commonDir: common, inGitDir: true}, true, nil
}

func isDir(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// validHEAD applies git's validate_headref. Like git, it reads only the start of
// the file, so a huge or endless HEAD in an ancestor directory cannot stall the
// search.
func validHEAD(file string) bool {
	info, err := os.Lstat(file)
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, linkErr := os.Readlink(file)
		return linkErr == nil && strings.HasPrefix(target, "refs/")
	}
	if !info.Mode().IsRegular() {
		return false
	}
	data, err := readStart(file, headReadLimit)
	if err != nil {
		return false
	}
	text := string(data)
	if ref, ok := strings.CutPrefix(text, "ref:"); ok {
		return strings.HasPrefix(strings.TrimLeft(ref, " \t\n\r"), "refs/")
	}
	if len(text) < hexObjectIDLength {
		return false
	}
	_, decodeErr := hex.DecodeString(text[:hexObjectIDLength])
	return decodeErr == nil
}

func readStart(file string, limit int64) (data []byte, err error) {
	f, err := os.Open(file) //nolint:gosec // repository metadata path
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	return io.ReadAll(io.LimitReader(f, limit))
}
