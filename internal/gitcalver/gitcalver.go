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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	cfgformat "github.com/go-git/go-git/v5/plumbing/format/config"
	"github.com/go-git/go-git/v5/plumbing/object"
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

	// hexObjectIDLength is the length of a SHA-1 object ID in hex. A longer
	// SHA-256 ID starts with as many hex digits.
	hexObjectIDLength = 40

	// headReadLimit is how much of HEAD git reads when it tests for a git
	// directory.
	headReadLimit = 255
)

var versionRe = regexp.MustCompile(`^(\d{8})\.([1-9]\d*)$`)

var (
	errInvalidGitFile = errors.New("invalid .git file")
	errEmptyCommonDir = errors.New("empty commondir file")
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
	return &compatStorage{filesystem.NewStorage(fs, cache.NewObjectLRUDefault())}
}

// Module gives submodules the same treatment as the superproject.
func (s *compatStorage) Module(name string) (storage.Storer, error) {
	moduleFS, err := dotgit.New(s.Filesystem()).Module(name)
	if err != nil {
		return nil, err
	}
	return newCompatStorage(moduleFS), nil
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
	repo, err := git.Open(storer, osfs.New(dirs.worktreeDir))
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
		unprovable := &ExitError{exitIncompleteHistory, "local history cannot prove workspace state"}
		if state.workspace == workspaceUnreachable {
			return "", unprovable
		}
		if state.worktree != nil {
			status, statusErr := state.worktree.Status()
			if statusErr != nil {
				return "", unprovable
			}
			workspaceDirty = !status.IsClean()
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
			return namedGitDir(dotGit)
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
