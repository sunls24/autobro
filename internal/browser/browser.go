package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
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
	if err = removeStaleSingletonFiles(dataDir); err != nil {
		return nil, err
	}

	return newBro(headless, dataDir)
}

func removeStaleSingletonFiles(dataDir string) error {
	lockPath := filepath.Join(dataDir, "SingletonLock")
	lock, err := os.Readlink(lockPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read browser singleton lock: %w", err)
	}

	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("get hostname: %w", err)
	}
	pidText, ok := strings.CutPrefix(lock, hostname+"-")
	if !ok {
		return nil
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		return nil
	}
	if err = syscall.Kill(pid, 0); err == nil || !errors.Is(err, syscall.ESRCH) {
		return nil
	}

	for _, name := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket"} {
		path := filepath.Join(dataDir, name)
		if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale browser singleton file %q: %w", path, err)
		}
	}
	return nil
}

func NewTemp(headless bool) (*rod.Browser, error) {
	return newBro(headless, "")
}

func MustBackgroundPage(b *rod.Browser) *rod.Page {
	page, err := b.Page(proto.TargetCreateTarget{
		URL:        "",
		Background: true,
	})
	if err != nil {
		panic(err)
	}
	return page
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

func WaitURLChange(ctx context.Context, page *rod.Page, action func(), checks ...func(*rod.Page) error) (string, error) {
	nowURL := page.MustInfo().URL
	_ = rod.Try(action)
	count := 0
	for {
		select {
		case <-ctx.Done():
			return nowURL, ctx.Err()
		default:
			time.Sleep(time.Second)
			count++
			newURL := page.MustInfo().URL
			if newURL != "" && newURL != nowURL {
				return newURL, nil
			}
			for _, check := range checks {
				if err := check(page); err != nil {
					return nowURL, err
				}
			}
			if count >= 6 {
				_ = rod.Try(action)
				count = 0
			}
		}
	}
}

func MustWaitURLChange(ctx context.Context, page *rod.Page, action func(), checks ...func(*rod.Page) error) string {
	newURL, err := WaitURLChange(ctx, page, action, checks...)
	if err != nil {
		panic(err)
	}
	return newURL
}
