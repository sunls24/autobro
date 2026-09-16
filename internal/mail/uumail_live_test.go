package mail

// uumail_live_test.go 面向真实 Uumail API 的冒烟测试。
// 默认跳过；设置 UUMAIL_LIVE=1 并提供 UUMAIL_ACCOUNTS 与 SUNMAIL_API_KEY 时执行：
// 覆盖懒登录/会话校验、别名创建、转发地址解析与删除的完整客户端链路。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUumailLiveSmoke(t *testing.T) {
	if os.Getenv("UUMAIL_LIVE") != "1" {
		t.Skip("设置 UUMAIL_LIVE=1 启用真实 API 冒烟测试")
	}
	accounts := strings.Split(os.Getenv("UUMAIL_ACCOUNTS"), ",")
	apiKey := os.Getenv("SUNMAIL_API_KEY")
	if len(accounts) == 0 || accounts[0] == "" || apiKey == "" {
		t.Fatal("缺少 UUMAIL_ACCOUNTS 或 SUNMAIL_API_KEY")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// go test 的工作目录是包目录；会话缓存必须指向仓库根的真实文件，
	// 与 toapi/uumailregister 共用同一份，否则会散落副本且测不到真实登录链路。
	provider, err := NewUumailWithConfig(ctx, UumailConfig{
		Accounts:      accounts,
		SunMailAPIKey: apiKey,
		SessionPath:   filepath.Join("..", "..", "uumail_sessions.json"),
	})
	if err != nil {
		t.Fatalf("NewUumailWithConfig() error = %v", err)
	}

	address, err := provider.NewAddress(ctx, "Live Smoke")
	if err != nil {
		t.Fatalf("NewAddress() error = %v", err)
	}
	t.Logf("别名已创建：%s", address)

	forward, err := provider.ForwardAddress(ctx, address)
	if err != nil {
		t.Fatalf("ForwardAddress() error = %v", err)
	}
	t.Logf("转发地址：%s", forward)

	if err := provider.DelAddressByMetadata(ctx, provider.Metadata(address)); err != nil {
		t.Fatalf("DelAddressByMetadata() error = %v", err)
	}
	t.Logf("别名已删除：%s", address)
}
