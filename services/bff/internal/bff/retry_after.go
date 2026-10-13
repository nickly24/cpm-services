package bff

import (
	"net/http"
	"strconv"
	"strings"
)

// Retry-After is the only upstream response header used by automatic delivery.
// Canonicalization rejects malformed values and never forwards arbitrary headers.
func safeRetryAfter(status int, raw string) string {
	if status != http.StatusRequestTimeout && status != http.StatusTooManyRequests && status < 500 {
		return ""
	}
	if len(raw) > 128 {
		return ""
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		return strconv.FormatUint(seconds, 10)
	}
	if timestamp, err := http.ParseTime(value); err == nil {
		return timestamp.UTC().Format(http.TimeFormat)
	}
	return ""
}
