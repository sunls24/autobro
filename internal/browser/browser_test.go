package browser

import (
	"testing"
	"time"
)

func TestNewDefault(t *testing.T) {
	b, err := NewDefault(false)
	if err != nil {
		t.Fatal(err)
	}
	page := b.MustPage("https://sunls.de")
	page.MustWaitLoad()
	page.MustWaitStable()
	wait := page.MustWaitNavigation()
	page.MustElement(`a[href="/workspace"]`).MustClick()
	wait()
	time.Sleep(time.Hour)
}
