package chatgpt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sunls24/gox"
)

type waitingMail struct{}

func (waitingMail) NewAddress(context.Context, string) (string, error) { return "", nil }
func (waitingMail) DelAddress(context.Context, string) error           { return nil }
func (waitingMail) ForgetAddress(string)                               {}
func (waitingMail) ForwardAddress(context.Context, string) (string, error) {
	return "", nil
}
func (waitingMail) WaitMailCode(context.Context, string) <-chan gox.Result[string] {
	return make(chan gox.Result[string])
}

func TestWaitMailCodeTimeout(t *testing.T) {
	flow := New(WithIMail(waitingMail{}), WithMailCodeTimeout(10*time.Millisecond))
	err := flow.waitMailCode(context.Background(), "forward@example.com", func(string) {}, nil)
	if !errors.Is(err, ErrMailCodeTimeout) {
		t.Fatalf("waitMailCode() error = %v, want ErrMailCodeTimeout", err)
	}
}

func TestWaitMailCodeHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	flow := New(WithIMail(waitingMail{}))
	err := flow.waitMailCode(ctx, "forward@example.com", func(string) {}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitMailCode() error = %v, want context.Canceled", err)
	}
}

func TestWaitMailCodeHonorsContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	flow := New(WithIMail(waitingMail{}), WithMailCodeTimeout(time.Minute))
	err := flow.waitMailCode(ctx, "forward@example.com", func(string) {}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitMailCode() error = %v, want context.DeadlineExceeded", err)
	}
}
