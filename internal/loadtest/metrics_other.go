//go:build !linux

package loadtest

import "runtime"

func platformEnvironment() Environment {
	return Environment{Kernel: "unknown", CPU: "unknown"}
}

func peakRSSBytes() uint64 {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.Sys
}
