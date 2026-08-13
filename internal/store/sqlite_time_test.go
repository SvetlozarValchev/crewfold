package store

import (
	"context"
	"testing"
)

func TestSQLiteCanonicalTimestampFunction(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	for _, test := range []struct {
		name  string
		value any
		want  int
	}{
		{name: "seconds UTC", value: "2026-08-13T04:30:00Z", want: 1},
		{name: "nanoseconds UTC", value: "2026-08-13T04:30:00.000000001Z", want: 1},
		{name: "equivalent offset", value: "2026-08-13T07:30:00+03:00", want: 0},
		{name: "noncanonical trailing zeros", value: "2026-08-13T04:30:00.1000Z", want: 0},
		{name: "overprecision", value: "2026-08-13T04:30:00.0000000001Z", want: 0},
		{name: "invalid", value: "not-a-timestamp", want: 0},
		{name: "integer", value: 7, want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got int
			if err := storage.db.QueryRowContext(context.Background(), `SELECT crewfold_timestamp_canonical(?)`, test.value).Scan(&got); err != nil {
				t.Fatalf("crewfold_timestamp_canonical(%v) error = %v", test.value, err)
			}
			if got != test.want {
				t.Fatalf("crewfold_timestamp_canonical(%v) = %d, want %d", test.value, got, test.want)
			}
		})
	}
}
