package db

import "embed"

// MigrationsFS holds all goose migration files embedded at compile time.
//
//go:embed migrations/*.sql
var MigrationsFS embed.FS
