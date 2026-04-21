package main

import (
	"context"
	"database/sql"
	"log"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/openclaw/clawguard/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	conn, err := sql.Open("pgx", cfg.DatabaseURL())
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	if err := goose.UpContext(context.Background(), conn, "migrations"); err != nil {
		log.Fatal(err)
	}
}
