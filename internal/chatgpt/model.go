package chatgpt

import (
	"context"
	"errors"
	"time"
)

type Authenticator interface {
	RegisterOrLogin(context.Context, *Account) (*Account, error)
}

type Account struct {
	Email             string `json:"email"`
	Password          string `json:"password,omitempty"`
	ForwardMail       string `json:"forward_mail"`
	MailProvider      string `json:"mail_provider,omitempty"`
	ProviderAddressID int64  `json:"provider_address_id,omitempty"`
	ProviderOwnerID   int64  `json:"provider_owner_id,omitempty"`
	AccessToken       string `json:"-"`
}

var ErrAccountDeactivated = errors.New("account deactivated")
var ErrMailCodeTimeout = errors.New("mail code timeout")

const (
	timeout                 = 2 * time.Second
	defaultMailCodeInterval = 30 * time.Second
	maxMailCodeResends      = 5

	baseURL     = "https://chatgpt.com/"
	passwordURL = "https://auth.openai.com/create-account/password"
	emailURL    = "https://auth.openai.com/email-verification"
	aboutURL    = "https://auth.openai.com/about-you"
	passkeyURL  = "https://auth.openai.com/create-account-enroll-passkey"
)
