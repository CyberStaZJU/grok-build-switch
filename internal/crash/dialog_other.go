//go:build !windows

package crash

func showErrorDialog(title, message string) {}
func showInfoDialog(title, message string)  {}
