package store

import (
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // register "pgx" database/sql driver for goose
	"github.com/pressly/goose/v3"

	"flightdeck/migrations"
)

// Migrate runs all pending goose migrations against databaseURL. goose needs a
// database/sql handle, so we open one transiently via the pgx stdlib driver.
func Migrate(databaseURL string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open sql db for migrate: %w", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.Up(db, "."); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}
