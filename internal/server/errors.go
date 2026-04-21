package server

import (
	"database/sql"
	"errors"
)

func isNotFoundError(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
