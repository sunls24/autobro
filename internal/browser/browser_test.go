package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveStaleSingletonFiles(t *testing.T) {
	dataDir := t.TempDir()
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket"} {
		target := "stale"
		if name == "SingletonLock" {
			target = fmt.Sprintf("%s-%d", hostname, 99999999)
		}
		if err = os.Symlink(target, filepath.Join(dataDir, name)); err != nil {
			t.Fatal(err)
		}
	}

	if err = removeStaleSingletonFiles(dataDir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket"} {
		if _, err = os.Lstat(filepath.Join(dataDir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was not removed", name)
		}
	}
}

func TestRemoveSingletonFilesForRunningProcess(t *testing.T) {
	dataDir := t.TempDir()
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dataDir, "SingletonLock")
	if err = os.Symlink(fmt.Sprintf("%s-%d", hostname, os.Getpid()), lockPath); err != nil {
		t.Fatal(err)
	}

	if err = removeStaleSingletonFiles(dataDir); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(lockPath); err != nil {
		t.Fatalf("running process lock was removed: %v", err)
	}
}
