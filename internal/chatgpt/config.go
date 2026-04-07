package chatgpt

import "github.com/caarlos0/env/v11"

type Config struct {
	CpaURL   string `env:"CPA_URL" envDefault:"https://cli.sunls.de"`
	CpaToken string `env:"CPA_TOKEN"`

	SLAPIKeys []string `env:"SL_API_KEY" envSeparator:","`
}

func MustNew() *Config {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		panic(err)
	}
	return &cfg
}
