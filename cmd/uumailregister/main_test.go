package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"
)

func TestBusyAccountCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env.local")
	lock := flock.New(path + ".uu.lock")
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	err := run(path, "", "", false, true)
	if err == nil || !strings.Contains(err.Error(), "已有 UU 账号操作") {
		t.Fatalf("并发操作应在读取配置前返回占用错误，得到 %v", err)
	}
}
