package mail

import (
	"context"

	"github.com/sunls24/gox"
)

type IMailAddress interface {
	NewAddress(ctx context.Context, name string) (string, error)
	DelAddress(ctx context.Context, address string) error
	ForgetAddress(address string)
	ForwardAddress(ctx context.Context, address string) (string, error)
}

type IMailWait interface {
	WaitMailCode(ctx context.Context, address string) <-chan gox.Result[string]
}

type IMail interface {
	IMailAddress
	IMailWait
}

type mail struct {
	IMailAddress
	IMailWait
}

func From(address IMailAddress, wait IMailWait) IMail {
	return &mail{
		IMailAddress: address,
		IMailWait:    wait,
	}
}
