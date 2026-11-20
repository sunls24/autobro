package slregister

import (
	"os"
	"path/filepath"
	"testing"

	"autobro/internal/envfile"
)

func TestCheckEnvWritable(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), ".env.local")
	if err := os.WriteFile(envPath, []byte("SL_API_KEY=existing\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := envfile.CheckWritable(envPath); err != nil {
		t.Fatalf("CheckWritable() error = %v", err)
	}
}

func TestCheckEnvWritableRejectsMissingFile(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), ".env.local")
	if err := envfile.CheckWritable(envPath); err == nil {
		t.Fatal("CheckWritable() error = nil, want an error")
	}
}

func TestSetAPIKeyPreservesEnvFormatAndPermissions(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		want    string
	}{
		{"existing key", "set -a\nSL_API_KEY=old-one,old-two\nSUNMAIL_API_KEY=mail-key\n", "set -a\nSL_API_KEY=new-key\nSUNMAIL_API_KEY=mail-key\n"},
		{"quoted export", "# config\r\n  export SL_API_KEY='old-one,old-two'\r\nOTHER=keep\r\n", "# config\r\n  export SL_API_KEY=new-key\r\nOTHER=keep\r\n"},
		{"missing key", "OTHER=keep", "OTHER=keep\nSL_API_KEY=new-key\n"},
		{"empty file", "", "SL_API_KEY=new-key\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".env.local")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := SetAPIKey(path, "new-key"); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.want {
				t.Fatalf("updated env = %q, want %q", got, test.want)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("mode = %o, want 600", info.Mode().Perm())
			}
		})
	}
}
