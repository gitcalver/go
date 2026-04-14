package gitcalver

import "github.com/go-git/go-git/v5/plumbing"

const shortHashLen = 7

func shortHash(hash plumbing.Hash) string {
	return hash.String()[:shortHashLen]
}
