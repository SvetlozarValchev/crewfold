//go:build linux

package loadtest

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func platformEnvironment() Environment {
	result := Environment{Kernel: linuxKernel(), CPU: linuxCPU(), MemoryBytes: linuxMemoryBytes()}
	return result
}

func linuxKernel() string {
	var name unix.Utsname
	if err := unix.Uname(&name); err != nil {
		return "unknown"
	}
	return byteArrayString(name.Release[:])
}

func linuxCPU() string {
	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return "unknown"
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, 1024*1024))
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), ":")
		if found && (strings.TrimSpace(key) == "model name" || strings.TrimSpace(key) == "Hardware") {
			return strings.TrimSpace(value)
		}
	}
	return "unknown"
}

func linuxMemoryBytes() uint64 {
	var info unix.Sysinfo_t
	if err := unix.Sysinfo(&info); err != nil {
		return 0
	}
	return info.Totalram * uint64(info.Unit)
}

func peakRSSBytes() uint64 {
	file, err := os.Open("/proc/self/status")
	if err != nil {
		return 0
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, 1024*1024))
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), ":")
		if !found || strings.TrimSpace(key) != "VmHWM" {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) != 2 || fields[1] != "kB" {
			return 0
		}
		kilobytes, parseErr := strconv.ParseUint(fields[0], 10, 64)
		if parseErr != nil {
			return 0
		}
		return kilobytes * 1024
	}
	return 0
}

func byteArrayString(value []byte) string {
	if index := strings.IndexByte(string(value), 0); index >= 0 {
		value = value[:index]
	}
	return string(value)
}
