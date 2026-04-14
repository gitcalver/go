// Copyright © 2026 Michael Shields
// SPDX-License-Identifier: MIT

package gitcalver

import (
	"errors"
	"fmt"
	"io"
)

const usageText = `Usage: gitcalver [options] [REVISION | VERSION]

Compute a gitcalver version for a git commit, or find the commit for a version.

Options:
  --prefix PREFIX     Prepend PREFIX to the version string
  --dirty STRING      Allow dirty workspace; append STRING.HASH to the version
  --no-dirty          Refuse dirty versions (overrides --dirty)
  --no-dirty-hash     Suppress the .HASH suffix (requires --dirty)
  --branch BRANCH     Base branch name (e.g. "main"); overrides auto-detection
  --short             Output short commit hash (reverse mode only)
  --help              Show this help
`

var (
	errHelp          = errors.New("help requested")
	errUnknownOption = errors.New("unknown option")
	errPrefixArg     = errors.New("--prefix requires an argument")
	errDirtyArg      = errors.New("--dirty requires an argument")
	errDirtyEmpty    = errors.New("--dirty must not be empty")
	errNoDirtyHash   = errors.New("--no-dirty-hash requires --dirty")
	errBranchArg     = errors.New("--branch requires an argument")
	errUnexpectedArg = errors.New("unexpected argument")
)

// Main is the entry point called from cmd/gitcalver. Returns an exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args)
	if err != nil {
		if errors.Is(err, errHelp) {
			fmt.Fprint(stdout, usageText) //nolint:errcheck // write to stdout is non-actionable
			return 0
		}
		fmt.Fprintf(stderr, "gitcalver: %s\n", err) //nolint:errcheck // write to stderr is non-actionable
		return 1
	}

	result, err := Run(opts)
	if err != nil {
		var exitErr *ExitError
		if errors.As(err, &exitErr) {
			fmt.Fprintf(stderr, "gitcalver: %s\n", exitErr.Message) //nolint:errcheck // write to stderr is non-actionable
			return exitErr.Code
		}
		fmt.Fprintf(stderr, "gitcalver: %s\n", err) //nolint:errcheck // write to stderr is non-actionable
		return 1
	}

	fmt.Fprintln(stdout, result) //nolint:errcheck // write to stdout is non-actionable
	return 0
}

func parseArgs(args []string) (*Options, error) {
	opts := &Options{}

	noDirty := false
	dirtyWasSet := false
	endOfOpts := false

	for i := 0; i < len(args); i++ {
		arg := args[i]

		if endOfOpts {
			if opts.Target != "" {
				return nil, fmt.Errorf("%w: %s", errUnexpectedArg, arg)
			}
			opts.Target = arg
			continue
		}

		switch arg {
		case "--":
			endOfOpts = true
		case "--prefix":
			if i+1 >= len(args) {
				return nil, errPrefixArg
			}
			i++
			opts.Prefix = args[i]
		case "--dirty":
			if i+1 >= len(args) {
				return nil, errDirtyArg
			}
			i++
			if args[i] == "" {
				return nil, errDirtyEmpty
			}
			opts.Dirty = args[i]
			dirtyWasSet = true
		case "--no-dirty":
			noDirty = true
		case "--no-dirty-hash":
			opts.NoDirtyHash = true
		case "--branch":
			if i+1 >= len(args) {
				return nil, errBranchArg
			}
			i++
			opts.Branch = args[i]
		case "--short":
			opts.Short = true
		case "--help":
			return nil, errHelp
		default:
			if len(arg) > 0 && arg[0] == '-' {
				return nil, fmt.Errorf("%w: %s", errUnknownOption, arg)
			}
			if opts.Target != "" {
				return nil, fmt.Errorf("%w: %s", errUnexpectedArg, arg)
			}
			opts.Target = arg
		}
	}

	if noDirty {
		opts.Dirty = ""
	}

	if opts.NoDirtyHash && !dirtyWasSet {
		return nil, errNoDirtyHash
	}

	return opts, nil
}
