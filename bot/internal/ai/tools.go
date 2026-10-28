package ai

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

func providerHTTPError(resp *http.Response, err error) error {
	if resp == nil {
		return err
	}
	out := &ProviderError{StatusCode: resp.StatusCode, Err: err}
	if retry := strings.TrimSpace(resp.Header.Get("Retry-After")); retry != "" {
		if seconds, parseErr := strconv.Atoi(retry); parseErr == nil && seconds >= 0 {
			out.RetryAfter = time.Duration(seconds) * time.Second
		} else if at, parseErr := http.ParseTime(retry); parseErr == nil {
			out.RetryAfter = time.Until(at)
			if out.RetryAfter < 0 {
				out.RetryAfter = 0
			}
		}
	}
	return out
}
