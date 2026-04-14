// Copyright © 2026 Michael Shields
// SPDX-License-Identifier: MIT

// Package main provides the gitcalver container entrypoint.
package main

import (
	"fmt"
	"os"

	"gitcalver.org/go/internal/gitcalver"
)

func main() {
	if err := os.Chdir("/repo"); err != nil {
		fmt.Fprintf(os.Stderr, "gitcalver: %v\n", err)
		os.Exit(1)
	}
	os.Exit(gitcalver.Main(os.Args[1:], os.Stdout, os.Stderr))
}
