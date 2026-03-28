package pkg

import (
	"os"

	"github.com/rs/zerolog"
)

func NewLogger(service, level string) zerolog.Logger {
	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		lvl = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(lvl)

	return zerolog.New(os.Stdout).
		With().
		Timestamp().
		Str("service", service).
		Logger()
}
