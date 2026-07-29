//go:build darwin

package singleinstance

import "testing"

func TestAcquireDetectsAndReleasesLock(t *testing.T) {
	key := t.Name() + t.TempDir()
	first, running, err := Acquire(key)
	if err != nil || running {
		t.Fatalf("first Acquire() = (%v, %v, %v)", first, running, err)
	}
	second, running, err := Acquire(key)
	if err != nil || !running || second != nil {
		t.Fatalf("second Acquire() = (%v, %v, %v)", second, running, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, running, err := Acquire(key)
	if err != nil || running {
		t.Fatalf("Acquire() after Close = (%v, %v, %v)", third, running, err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
}
