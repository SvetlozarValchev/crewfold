package daemon

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

func daemonProcessResources() (int64, int64, error) {
	data, rssErr := os.ReadFile("/proc/self/statm")
	var rss int64
	if rssErr == nil {
		fields := strings.Fields(string(data))
		if len(fields) < 2 {
			rssErr = errors.New("/proc/self/statm omitted resident pages")
		} else if pages, err := strconv.ParseInt(fields[1], 10, 64); err != nil {
			rssErr = err
		} else {
			rss = pages * int64(os.Getpagesize())
		}
	}
	entries, fdErr := os.ReadDir("/proc/self/fd")
	if rssErr != nil {
		return rss, int64(len(entries)), rssErr
	}
	if fdErr != nil {
		return rss, 0, fdErr
	}
	return rss, int64(len(entries)), nil
}
