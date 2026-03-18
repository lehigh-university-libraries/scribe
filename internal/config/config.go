package config

import "os"

type Config struct {
	ListenAddr  string
	DatabaseDSN string
}

func FromEnv() Config {
	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8080"
	}

	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		dsn = "scribe:scribe@tcp(mariadb:3306)/scribe?parseTime=true"
	}

	return Config{
		ListenAddr:  listenAddr,
		DatabaseDSN: dsn,
	}
}
