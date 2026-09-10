package mail

import (
	"context"
	"strings"

	"github.com/sunls24/gox"
)

type AddressMetadata struct {
	Email     string
	Provider  string
	AddressID int64
	OwnerID   int64
}

type IMailAddress interface {
	NewAddress(ctx context.Context, name string) (string, error)
	DelAddressByMetadata(ctx context.Context, metadata AddressMetadata) error
	ForgetAddress(address string)
	ForwardAddress(ctx context.Context, address string) (string, error)
	Metadata(address string) AddressMetadata
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

func normalizeAddress(address string) string {
	return strings.ToLower(strings.TrimSpace(address))
}
