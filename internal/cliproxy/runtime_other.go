//go:build !darwin

package cliproxy

import (
	"context"
	"fmt"
)

type UnsupportedKeychain struct{}

func (UnsupportedKeychain) Get(string, string) (string, error) {
	return "", fmt.Errorf("系统钥匙串不受支持")
}
func (UnsupportedKeychain) Set(string, string, string) error {
	return fmt.Errorf("系统钥匙串不受支持")
}

type Runtime struct {
	Paths Paths
	Home  string
}
type Status struct{ Running, Healthy, PortConflict bool }

func (Runtime) Start(context.Context) error   { return fmt.Errorf("LaunchAgent 仅支持 macOS") }
func (Runtime) Stop(context.Context) error    { return fmt.Errorf("LaunchAgent 仅支持 macOS") }
func (Runtime) Restart(context.Context) error { return fmt.Errorf("LaunchAgent 仅支持 macOS") }
func (Runtime) Status(context.Context) (Status, error) {
	return Status{}, fmt.Errorf("LaunchAgent 仅支持 macOS")
}
