package toapi

import "github.com/caarlos0/env/v11"

type Config struct {
	SLAPIKeys            []string `env:"SL_API_KEY" envSeparator:","`
	ManyMeUsername       string   `env:"MANYME_USERNAME"`
	ManyMeForwardAddress string   `env:"MANYME_FORWARD_ADDRESS"`
	AccountsURL          string   `env:"TOAPI_ACCOUNTS_URL,required,notEmpty"`
	AccountsToken        string   `env:"TOAPI_ACCOUNTS_TOKEN,required,notEmpty"`
}

func MustNew() *Config {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		panic(err)
	}
	return &cfg
}
