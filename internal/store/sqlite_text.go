package store

import (
	"unicode/utf8"

	"github.com/ncruces/go-sqlite3"
)

const sqliteUTF8ValidFunction = "crewfold_utf8_valid"

func registerSQLiteUTF8Valid(connection *sqlite3.Conn) error {
	return connection.CreateFunction(
		sqliteUTF8ValidFunction,
		1,
		sqlite3.DETERMINISTIC|sqlite3.INNOCUOUS,
		func(ctx sqlite3.Context, arguments ...sqlite3.Value) {
			if len(arguments) != 1 || arguments[0].Type() != sqlite3.TEXT {
				ctx.ResultInt(0)
				return
			}
			if utf8.ValidString(arguments[0].Text()) {
				ctx.ResultInt(1)
				return
			}
			ctx.ResultInt(0)
		},
	)
}
