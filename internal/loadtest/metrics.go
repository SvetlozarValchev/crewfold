package loadtest

import (
	"os"
	"runtime"
	"sort"
	"time"
)

func timing(name string, samples []time.Duration) Timing {
	values := append([]time.Duration(nil), samples...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	result := Timing{Name: name, Repetitions: len(values)}
	if len(values) == 0 {
		return result
	}
	result.P50Microseconds = durationMicroseconds(nearestRank(values, 50))
	result.P95Microseconds = durationMicroseconds(nearestRank(values, 95))
	result.P99Microseconds = durationMicroseconds(nearestRank(values, 99))
	result.MaxMicroseconds = durationMicroseconds(values[len(values)-1])
	return result
}

func nearestRank(sorted []time.Duration, percentile int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	index := (percentile*len(sorted) + 99) / 100
	if index < 1 {
		index = 1
	}
	return sorted[index-1]
}

func durationMicroseconds(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	// Preserve a positive observation that rounded below one microsecond.
	microseconds := value.Microseconds()
	if microseconds == 0 {
		return 1
	}
	return microseconds
}

func currentEnvironment() Environment {
	environment := platformEnvironment()
	environment.GOOS = runtime.GOOS
	environment.GOARCH = runtime.GOARCH
	environment.GoVersion = runtime.Version()
	// Early operational failures occur before Store.Health can provide the
	// runtime value. Keep those failed reports schema-valid without pretending
	// that SQLite was observed.
	environment.SQLiteVersion = "unavailable"
	environment.LogicalCPUs = runtime.NumCPU()
	return environment
}

func openFDCount() int {
	directory, err := os.Open("/proc/self/fd")
	if err != nil {
		return -1
	}
	defer directory.Close()
	count := 0
	for {
		names, readErr := directory.Readdirnames(128)
		count += len(names)
		if readErr != nil {
			break
		}
	}
	// Do not count the descriptor used to enumerate the directory itself.
	if count > 0 {
		count--
	}
	return count
}
