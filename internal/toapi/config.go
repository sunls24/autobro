package toapi

import "github.com/caarlos0/env/v11"

type Config struct {
	SLAPIKeys            []string `env:"SL_API_KEY" envSeparator:","`
	SunMailAPIKey        string   `env:"SUNMAIL_API_KEY,required,notEmpty"`
	ManyMeUsername       string   `env:"MANYME_USERNAME"`
	ManyMeForwardAddress string   `env:"MANYME_FORWARD_ADDRESS"`
	SceneMintURL         string   `env:"SCENEMINT_URL,required,notEmpty"`
	SceneMintAPIKey      string   `env:"SCENEMINT_API_KEY,required,notEmpty"`
}

func MustNew() *Config {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		panic(err)
	}
	return &cfg
}
