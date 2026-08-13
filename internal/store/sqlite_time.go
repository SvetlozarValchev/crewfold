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
	if err := connection.CreateFunction(
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
	); err != nil {
		return err
	}
	return connection.CreateFunction(
		"crewfold_timestamp_canonical",
		1,
		sqlite3.DETERMINISTIC|sqlite3.INNOCUOUS,
		func(ctx sqlite3.Context, arguments ...sqlite3.Value) {
			if len(arguments) != 1 || arguments[0].Type() != sqlite3.TEXT {
				ctx.ResultInt(0)
				return
			}
			value := arguments[0].Text()
			parsed, err := time.Parse(time.RFC3339Nano, value)
			if err == nil && parsed.Location() == time.UTC && parsed.Format(time.RFC3339Nano) == value {
				ctx.ResultInt(1)
				return
			}
			ctx.ResultInt(0)
		},
	)
}
