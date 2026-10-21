//go:build !horizon_distribution_test

package distribution

import (
	"net/http"
	"time"
)

func defaultHTTPClient() *http.Client { return &http.Client{Timeout: 5 * time.Minute} }
