// Copyright © 2026 Michael Shields
// SPDX-License-Identifier: MIT

// Binary gitcalver computes calendar-based version strings from git history.
package main

import (
	"os"

	"gitcalver.org/go/internal/gitcalver"
)

func main() {
	os.Exit(gitcalver.Main(os.Args[1:], os.Stdout, os.Stderr))
}
