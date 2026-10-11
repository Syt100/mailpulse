package graph

import (
	"net/http"
	"strconv"
	"time"
)

func retryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if until, err := http.ParseTime(value); err == nil {
		return max(0, until.Sub(now))
	}
	return 0
}
