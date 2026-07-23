//go:build !windows && !darwin

package singleinstance

type Lock struct{}

func Acquire(key string) (*Lock, bool, error) {
	return &Lock{}, false, nil
}

func (l *Lock) Close() error { return nil }
