package store

import (
	"errors"
	"time"

	"github.com/ncruces/go-sqlite3"
)

const (
	sqliteTimestampKeyFunction = "crewfold_timestamp_key"
	sqliteTimestampKeyFormat   = "2006-01-02T15:04:05.000000000Z"
)

var errInvalidSQLiteTimestamp = errors.New("crewfold_timestamp_key: expected a valid RFC3339Nano timestamp")

func registerSQLiteTimestampKey(connection *sqlite3.Conn) error {
	return connection.CreateFunction(
		sqliteTimestampKeyFunction,
		1,
		sqlite3.DETERMINISTIC|sqlite3.INNOCUOUS,
		func(ctx sqlite3.Context, arguments ...sqlite3.Value) {
			if len(arguments) != 1 || arguments[0].Type() != sqlite3.TEXT {
				ctx.ResultError(errInvalidSQLiteTimestamp)
				return
			}
			parsed, err := time.Parse(time.RFC3339Nano, arguments[0].Text())
			if err != nil {
				ctx.ResultError(errInvalidSQLiteTimestamp)
				return
			}
			utc := parsed.UTC()
			if utc.Year() < 0 || utc.Year() > 9999 {
				ctx.ResultError(errInvalidSQLiteTimestamp)
				return
			}
			ctx.ResultText(utc.Format(sqliteTimestampKeyFormat))
		},
	)
}
