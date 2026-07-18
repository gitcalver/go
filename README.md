# gitcalver

[![Go Reference](https://pkg.go.dev/badge/gitcalver.org/go.svg)](https://pkg.go.dev/gitcalver.org/go)

A Go implementation of [GitCalVer](https://gitcalver.org), which derives
calendar-based version numbers from git history.

Each commit on the default branch gets a unique, strictly increasing version of
the form `YYYYMMDD.N`, where `N` is the number of commits on that UTC date.

See the [GitCalVer specification](https://gitcalver.org) for full details.

## Installation

No external dependencies are required; git history is read directly using
[go-git](https://github.com/go-git/go-git).

```sh
go install gitcalver.org/go/cmd/gitcalver@latest
```

Or build from source:

```sh
make build
```

A container image is also available. Mount the repository at `/repo`:

```sh
docker run --rm -v "$PWD:/repo" gitcalver
```

## Usage

```
gitcalver [OPTIONS] [REVISION | VERSION]
```

With no arguments, outputs the version for HEAD:

```sh
$ gitcalver
20260411.3
```

An omitted target checks the workspace. An explicit revision, including
`HEAD`, calculates that commit’s version without considering workspace changes.

### Version prefix

Use `--prefix` to prepend a string to the version number, e.g.:

| Use case | Command                      | Example output     |
|----------|------------------------------|--------------------|
| Default  | `gitcalver`                  | `20260411.3`       |
| SemVer   | `gitcalver --prefix "0."`    | `0.20260411.3`     |
| Go       | `gitcalver --prefix "v0."`   | `v0.20260411.3`    |

### Dirty workspace

By default, gitcalver exits with status 2 if the workspace has uncommitted
changes. Use `--dirty STRING` to produce a version instead; the output will
include the given string and a short commit hash
(e.g. `--dirty "-dirty"` produces `20260411.3-dirty.abc1234`).

Use `--no-dirty-hash` with `--dirty` to suppress the hash suffix.
Use `--no-dirty` to explicitly refuse dirty versions (overrides `--dirty`).

Dirty versions are a convenience and are not necessarily unique.

### Reverse lookup

Pass a version number instead of a revision to get the corresponding commit hash:

```sh
$ gitcalver 20260411.3
a1b2c3d4e5f6...

$ gitcalver --short --prefix "0." 0.20260411.3
a1b2c3d
```

If the version was generated with `--prefix`, pass the same `--prefix` for reverse lookup.

Dirty versions cannot be reversed.

### Options

| Option              | Description                                    |
|---------------------|------------------------------------------------|
| `--prefix PREFIX`   | Literal string prepended to version            |
| `--dirty STRING`    | Enable dirty versions; append STRING.HASH      |
| `--no-dirty`        | Refuse dirty versions (overrides `--dirty`)    |
| `--no-dirty-hash`   | Suppress .HASH suffix (requires `--dirty`)     |
| `--branch BRANCH`   | Override the branch whose first-parent chain defines versions |
| `--remote REMOTE`   | Select the cached remote used for branch detection; never fetches |
| `--short`           | Output the first seven object-ID characters in reverse mode |
| `--version`         | Show the gitcalver build version                   |
| `--help`            | Show help                                      |

### Exit codes

| Code | Meaning                                                     |
|------|-------------------------------------------------------------|
| 0    | Success                                                     |
| 1    | Invalid input or repository state                           |
| 2    | Dirty workspace or off selected branch without `--dirty`    |
| 3    | Complete history proves the target is on an unrelated chain |
| 4    | Local history is insufficient to prove the result           |

## History requirements

Calculations are always offline. Shallow and partial clones work when their
local commit objects prove the selected first-parent relationship, anchor, and
complete relevant UTC date block. Missing promised commits return exit code 4;
GitCalVer never fetches them during calculation.
