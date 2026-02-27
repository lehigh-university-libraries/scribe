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
    dsn = "hocredit:hocredit@tcp(127.0.0.1:3306)/hocredit?parseTime=true"
  }

  return Config{
    ListenAddr:  listenAddr,
    DatabaseDSN: dsn,
  }
}
