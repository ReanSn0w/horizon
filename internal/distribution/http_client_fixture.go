//go:build horizon_distribution_test

package distribution

// This transport exists only in explicitly tagged E2E test builds. Production
// binaries have no environment option for changing the official repository.
import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

type fixtureTransport struct{ base *url.URL }

func (t fixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.base == nil {
		return nil, fmt.Errorf("distribution fixture URL is required in test build")
	}
	if request.URL.Host != "github.com" && request.URL.Host != "api.github.com" {
		return nil, fmt.Errorf("unexpected release host: %s", request.URL.Host)
	}
	clone := request.Clone(request.Context())
	u := *clone.URL
	clone.URL = &u
	clone.URL.Scheme = t.base.Scheme
	clone.URL.Host = t.base.Host
	return http.DefaultTransport.RoundTrip(clone)
}
func defaultHTTPClient() *http.Client {
	endpoint, _ := url.Parse(os.Getenv("HORIZON_TEST_RELEASE_SERVER"))
	if endpoint == nil || endpoint.Host == "" {
		endpoint = nil
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: fixtureTransport{base: endpoint}}
}
