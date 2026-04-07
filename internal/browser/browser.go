package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

// NewDefault 默认使用本机 chrome/chromium 在 ~/.config/rod/data 目录启动浏览器
func NewDefault(headless bool) (*rod.Browser, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dataDir := filepath.Join(home, ".config", "rod", "data")
	if err = os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir %q: %w", dataDir, err)
	}

	return newBro(headless, dataDir)
}

func NewTemp(headless bool) (*rod.Browser, error) {
	return newBro(headless, "")
}

func newBro(headless bool, dataDir string) (*rod.Browser, error) {
	binPath, ok := launcher.LookPath()
	if !ok {
		return nil, errors.New("could not find chrome/chromium executable")
	}
	l := launcher.New().
		Bin(binPath).
		HeadlessNew(headless).
		Set("disable-blink-features", "AutomationControlled")
	if dataDir != "" {
		l.UserDataDir(dataDir)
	}
	u, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}

	browser := rod.New().
		ControlURL(u).
		NoDefaultDevice()
	if err = browser.Connect(); err != nil {
		return nil, fmt.Errorf("connect browser: %w", err)
	}
	return browser, nil
}

func WaitURLChange(ctx context.Context, page *rod.Page, action func()) (string, error) {
	nowURL := page.MustInfo().URL
	action()
	for {
		select {
		case <-ctx.Done():
			return nowURL, ctx.Err()
		default:
			time.Sleep(time.Second)
			newURL := page.MustInfo().URL
			if newURL == nowURL {
				continue
			}
			return newURL, nil
		}
	}
}

func MustWaitURLChange(ctx context.Context, page *rod.Page, action func()) string {
	newURL, err := WaitURLChange(ctx, page, action)
	if err != nil {
		panic(err)
	}
	return newURL
}
