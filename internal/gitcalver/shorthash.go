// Copyright © 2026 Michael Shields
// SPDX-License-Identifier: MIT

package gitcalver

import "github.com/go-git/go-git/v5/plumbing"

const objectIDPrefixLen = 7

// objectIDPrefix returns the contract-defined, fixed-width version component.
// It is not Git's repository-dependent unambiguous abbreviation.
func objectIDPrefix(hash plumbing.Hash) string {
	return hash.String()[:objectIDPrefixLen]
}
