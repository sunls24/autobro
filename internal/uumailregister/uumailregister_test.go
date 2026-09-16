package uumailregister

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSetEnvListValue(t *testing.T) {
	t.Parallel()

	t.Run("追加不存在的 key", func(t *testing.T) {
		path := writeEnvFile(t, "SUNMAIL_API_KEY=sk\n")
		if err := SetEnvListValue(path, "UUMAIL_ACCOUNTS", "b@chato.eu.org"); err != nil {
			t.Fatalf("SetEnvListValue() error = %v", err)
		}
		assertEnvContent(t, path, "SUNMAIL_API_KEY=sk\nUUMAIL_ACCOUNTS=b@chato.eu.org\n")
	})

	t.Run("前插去重到已有列表", func(t *testing.T) {
		path := writeEnvFile(t, "UUMAIL_ACCOUNTS=a@chato.eu.org,b@chato.eu.org\n")
		if err := SetEnvListValue(path, "UUMAIL_ACCOUNTS", "c@chato.eu.org"); err != nil {
			t.Fatalf("SetEnvListValue() error = %v", err)
		}
		assertEnvContent(t, path, "UUMAIL_ACCOUNTS=c@chato.eu.org,a@chato.eu.org,b@chato.eu.org\n")

		if err := SetEnvListValue(path, "UUMAIL_ACCOUNTS", "c@chato.eu.org"); err != nil {
			t.Fatalf("SetEnvListValue() error = %v", err)
		}
		assertEnvContent(t, path, "UUMAIL_ACCOUNTS=c@chato.eu.org,a@chato.eu.org,b@chato.eu.org\n")
	})

	t.Run("保留 export 前缀并展开引号值", func(t *testing.T) {
		path := writeEnvFile(t, "export UUMAIL_ACCOUNTS=\"a@chato.eu.org\"\n")
		if err := SetEnvListValue(path, "UUMAIL_ACCOUNTS", "b@chato.eu.org"); err != nil {
			t.Fatalf("SetEnvListValue() error = %v", err)
		}
		assertEnvContent(t, path, "export UUMAIL_ACCOUNTS=b@chato.eu.org,a@chato.eu.org\n")
	})

	t.Run("拒绝非法值", func(t *testing.T) {
		path := writeEnvFile(t, "")
		if err := SetEnvListValue(path, "UUMAIL_ACCOUNTS", "a@x.org,b@y.org"); err == nil {
			t.Fatal("含逗号的值应被拒绝")
		}
	})
}

func TestUumailUsername(t *testing.T) {
	t.Parallel()

	pattern := regexp.MustCompile(`^[a-zA-Z0-9_-]{3,20}$`)
	for _, name := range []string{"Emma Smith", "Liam Noah", "X"} {
		username := uumailUsername(name)
		if !pattern.MatchString(username) {
			t.Fatalf("uumailUsername(%q) = %q，不满足 3-20 位 [a-zA-Z0-9_-]", name, username)
		}
	}
}

func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env.local")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入 env 文件失败：%v", err)
	}
	return path
}

func assertEnvContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 env 文件失败：%v", err)
	}
	if string(data) != want {
		t.Fatalf("env 内容 = %q，want %q", string(data), want)
	}
	if !strings.Contains(string(data), "UUMAIL_ACCOUNTS=") {
		t.Fatalf("env 内容缺少 UUMAIL_ACCOUNTS：%q", string(data))
	}
}
