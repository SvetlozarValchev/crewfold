package store

import (
	"sync/atomic"

	"crewfold/internal/domain"

	"github.com/ncruces/go-sqlite3"
)

const sqliteCanonicalEventKnownFunction = "crewfold_event_type_known"

const sqliteCheckEventKnownFunction = "crewfold_check_watch_event_known"

const sqliteSupervisorActionSealActiveFunction = "crewfold_supervisor_action_seal_active"

// registerSQLiteSupervisorActionSealActive is a connection-local construction
// gate. Store transactions raise it only around insertion of the immutable
// recording receipt, while direct SQL on the shared handle observes zero.
func registerSQLiteSupervisorActionSealActive(connection *sqlite3.Conn, active *atomic.Bool) error {
	return connection.CreateFunction(
		sqliteSupervisorActionSealActiveFunction,
		0,
		sqlite3.INNOCUOUS,
		func(ctx sqlite3.Context, _ ...sqlite3.Value) {
			if active.Load() {
				ctx.ResultInt(1)
				return
			}
			ctx.ResultInt(0)
		},
	)
}

// registerSQLiteEventClassifiers exposes the current closed event union to
// indexed operator reads and schema triggers. Consumers cannot skip an event
// that the current binary cannot classify.
func registerSQLiteEventClassifiers(connection *sqlite3.Conn) error {
	if err := connection.CreateFunction(
		sqliteCanonicalEventKnownFunction,
		1,
		sqlite3.DETERMINISTIC|sqlite3.INNOCUOUS,
		func(ctx sqlite3.Context, arguments ...sqlite3.Value) {
			if len(arguments) != 1 || arguments[0].Type() != sqlite3.TEXT {
				ctx.ResultInt(0)
				return
			}
			if domain.KnownEventType(arguments[0].Text()) {
				ctx.ResultInt(1)
				return
			}
			ctx.ResultInt(0)
		},
	); err != nil {
		return err
	}
	return connection.CreateFunction(
		sqliteCheckEventKnownFunction,
		1,
		sqlite3.DETERMINISTIC|sqlite3.INNOCUOUS,
		func(ctx sqlite3.Context, arguments ...sqlite3.Value) {
			if len(arguments) != 1 || arguments[0].Type() != sqlite3.TEXT {
				ctx.ResultInt(0)
				return
			}
			if knownCheckWatchJournalEvent(arguments[0].Text()) {
				ctx.ResultInt(1)
				return
			}
			ctx.ResultInt(0)
		},
	)
}
