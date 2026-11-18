package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
)

// Session owns the browser process and account-scoped browser context for a
// single account attempt. Pages created from Browser belong to this session.
type Session struct {
	browser   *rod.Browser
	launcher  *launcher.Launcher
	closeOnce sync.Once
	closeErr  error
}

const browserCloseTimeout = 5 * time.Second

// NewSession creates a fresh temporary browser profile for one account.
func NewSession(headless bool) (*Session, error) {
	browser, l, err := launchBrowser(headless)
	if err != nil {
		return nil, err
	}
	return &Session{
		browser:  browser,
		launcher: l,
	}, nil
}

// Browser returns the account-scoped browser context owned by the session.
func (s *Session) Browser() *rod.Browser {
	if s == nil {
		return nil
	}
	return s.browser
}

// Close disposes the account browser, process, and temporary profile.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		var closeErr error
		if s.browser != nil {
			ctx, cancel := context.WithTimeout(context.Background(), browserCloseTimeout)
			closeErr = s.browser.Context(ctx).Close()
			cancel()
		}
		if closeErr != nil {
			s.closeErr = errors.Join(s.closeErr, fmt.Errorf("close browser: %w", closeErr))
		}
		if s.launcher != nil {
			// The session owns this browser process. Cleanup waits indefinitely on
			// the launcher exit signal, so terminate the process group explicitly
			// and remove the session-scoped profile without another wait.
			s.launcher.Kill()
			s.closeErr = errors.Join(s.closeErr, cleanupProfile(s.launcher))
		} else {
			s.closeErr = errors.Join(s.closeErr, closeErr)
		}
	})
	return s.closeErr
}

func cleanupProfile(l *launcher.Launcher) error {
	dir := filepath.Clean(l.Get(flags.UserDataDir))
	root := filepath.Clean(launcher.DefaultUserDataDirPrefix)
	if dir == "." || dir == root {
		return errors.New("refusing to remove the browser data root")
	}

	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to remove browser profile outside %q: %q", root, dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove browser profile %q: %w", dir, err)
	}
	return nil
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

func launchBrowser(headless bool) (*rod.Browser, *launcher.Launcher, error) {
	binPath, ok := launcher.LookPath()
	if !ok {
		return nil, nil, errors.New("could not find chrome/chromium executable")
	}
	l := launcher.New().
		Bin(binPath).
		HeadlessNew(headless).
		Set("disable-blink-features", "AutomationControlled")
	u, err := l.Launch()
	if err != nil {
		return nil, nil, fmt.Errorf("launch browser: %w", err)
	}

	browser := rod.New().
		ControlURL(u).
		NoDefaultDevice()
	if err = browser.Connect(); err != nil {
		l.Kill()
		l.Cleanup()
		return nil, nil, fmt.Errorf("connect browser: %w", err)
	}
	return browser, l, nil
}

// pageURL reads the target URL using the caller's context.
func pageURL(ctx context.Context, page *rod.Page) (string, error) {
	target := proto.TargetGetTargetInfo{TargetID: page.TargetID}
	info, err := target.Call(page.Browser().Context(ctx))
	if err != nil {
		return "", err
	}
	return info.TargetInfo.URL, nil
}

// SubmitAndWaitForURLChange runs the action once and returns the normalized starting and last observed URLs.
func SubmitAndWaitForURLChange(ctx context.Context, page *rod.Page, normalize func(string) string, action func() error, checks ...func(*rod.Page) error) (fromURL, lastURL string, err error) {
	rawURL, err := pageURL(ctx, page)
	if err != nil {
		return "", "", err
	}
	fromURL = normalize(rawURL)
	if err := action(); err != nil {
		return fromURL, fromURL, err
	}
	lastURL, err = WaitURLChange(ctx, page, fromURL, normalize, checks...)
	return fromURL, lastURL, err
}

// WaitURLChange observes a navigation from an already normalized URL.
func WaitURLChange(ctx context.Context, page *rod.Page, fromURL string, normalize func(string) string, checks ...func(*rod.Page) error) (string, error) {
	lastURL := fromURL
	for {
		select {
		case <-ctx.Done():
			return lastURL, ctx.Err()
		case <-time.After(time.Second):
		}
		rawURL, err := pageURL(ctx, page)
		if err != nil {
			return lastURL, err
		}
		lastURL = normalize(rawURL)
		if lastURL != "" && lastURL != fromURL {
			return lastURL, nil
		}
		for _, check := range checks {
			if err := check(page); err != nil {
				return lastURL, err
			}
		}
	}
}
