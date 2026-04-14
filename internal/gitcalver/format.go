// Copyright © 2026 Michael Shields
// SPDX-License-Identifier: MIT

package gitcalver

import "strconv"

func formatVersion(prefix, date string, n int, dirtyStr string, hash string) string {
	version := prefix + date + "." + strconv.Itoa(n)
	if dirtyStr != "" {
		version += dirtyStr
		if hash != "" {
			version += "." + hash
		}
	}
	return version
}
