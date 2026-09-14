package slregister

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrependAPIKeyPreservesExistingKeys(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), ".env.local")
	content := "set -a\nSL_API_KEY=old-one,old-two\nSUNMAIL_API_KEY=mail-key\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := PrependAPIKey(envPath, "new-key"); err != nil {
		t.Fatalf("PrependAPIKey() error = %v", err)
	}
	got, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	want := "set -a\nSL_API_KEY=new-key,old-one,old-two\nSUNMAIL_API_KEY=mail-key\n"
	if string(got) != want {
		t.Fatalf("updated env = %q, want %q", string(got), want)
	}
	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestCheckEnvWritable(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), ".env.local")
	if err := os.WriteFile(envPath, []byte("SL_API_KEY=existing\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := checkEnvWritable(envPath); err != nil {
		t.Fatalf("checkEnvWritable() error = %v", err)
	}
}

func TestCheckEnvWritableRejectsMissingFile(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), ".env.local")
	if err := checkEnvWritable(envPath); err == nil {
		t.Fatal("checkEnvWritable() error = nil, want an error")
	}
}

func TestPrependAPIKeyDoesNotDuplicate(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), ".env.local")
	content := "SL_API_KEY=existing\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := PrependAPIKey(envPath, "existing"); err != nil {
		t.Fatalf("PrependAPIKey() error = %v", err)
	}
	got, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != content {
		t.Fatalf("env changed on duplicate: %q", string(got))
	}
}
