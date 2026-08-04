//go:build !unix

package cliproxy

func processExists(int) bool {
	return true
}
