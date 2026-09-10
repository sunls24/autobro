package mail

import (
	"context"
	"errors"
	"strings"
)

var _ IMailAddress = (*many)(nil)

type many struct {
	username       string
	forwardAddress string
}

func (m *many) NewAddress(ctx context.Context, name string) (string, error) {
	if m.username == "" {
		return "", errors.New("manyme username is required")
	}
	return m.username + "." + nameToAddress(name) + "@manyme.com", nil
}

func (m *many) DelAddressByMetadata(ctx context.Context, metadata AddressMetadata) error {
	return nil
}

func (m *many) ForgetAddress(address string) {}

func (m *many) ForwardAddress(ctx context.Context, address string) (string, error) {
	if m.forwardAddress == "" {
		return "", errors.New("manyme forward address is required")
	}
	return m.forwardAddress, nil
}

func (m *many) Metadata(address string) AddressMetadata {
	return AddressMetadata{Email: address, Provider: AddressProviderManyMe}
}

func NewManyMe(username, forwardAddress string) (IMailAddress, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	forwardAddress = strings.TrimSpace(forwardAddress)
	if username == "" {
		return nil, errors.New("MANYME_USERNAME is required")
	}
	if forwardAddress == "" {
		return nil, errors.New("MANYME_FORWARD_ADDRESS is required")
	}
	return &many{
		username:       username,
		forwardAddress: forwardAddress,
	}, nil
}
