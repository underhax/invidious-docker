// Package main implements a zero-dependency healthcheck probe that validates
// Invidious and Invidious Companion availability against their internal HTTP liveness endpoints.
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultEndpoint            = "http://127.0.0.1:3000/api/v1/stats"
	defaultCompanionEndpoint   = "http://unix/companion/healthz"
	defaultCompanionSocketPath = "/tmp/invidious-companion.sock"
	companionTargetArg         = "companion"
	unixHostName               = "unix"
	defaultTimeout             = 3 * time.Second
)

func defaultDialUnix(ctx context.Context, _, _ string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", companionSocketPath)
	if err != nil {
		return nil, fmt.Errorf("dialing companion unix socket: %w", err)
	}
	return conn, nil
}

func defaultGetTransport(host string) http.RoundTripper {
	if host == unixHostName {
		return &http.Transport{
			DialContext: dialUnix,
		}
	}
	return http.DefaultTransport
}

var (
	companionSocketPath = defaultCompanionSocketPath
	dialUnix            = defaultDialUnix
	getTransport        = defaultGetTransport
	exitFunc            = os.Exit
)

func defaultCreateRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("creating HTTP request: %w", err)
	}
	return req, nil
}

var createRequest = defaultCreateRequest

func defaultDoRequest(ctx context.Context, endpoint string) (statusCode int, err error) {
	parsedURL, err := url.Parse(endpoint)
	if err != nil {
		return 0, fmt.Errorf("parsing probe endpoint: %w", err)
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return 0, fmt.Errorf("unsupported probe protocol scheme: %s", parsedURL.Scheme)
	}

	req, err := createRequest(ctx, http.MethodGet, parsedURL.String(), http.NoBody)
	if err != nil {
		return 0, fmt.Errorf("creating probe request: %w", err)
	}

	client := &http.Client{
		Timeout:   defaultTimeout,
		Transport: getTransport(parsedURL.Host),
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("executing probe request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("closing response body: %w", closeErr)
		}
	}()

	if _, drainErr := io.Copy(io.Discard, resp.Body); drainErr != nil {
		return 0, fmt.Errorf("reading response body: %w", drainErr)
	}

	return resp.StatusCode, nil
}

var doRequest = defaultDoRequest

// ResolveEndpoint selects the target healthcheck URL from command-line arguments so
// both Invidious and Invidious Companion can share a single static probe binary.
func ResolveEndpoint(args []string) string {
	if len(args) > 1 {
		switch strings.TrimSpace(args[1]) {
		case companionTargetArg, defaultCompanionEndpoint:
			return defaultCompanionEndpoint
		}
	}
	return defaultEndpoint
}

// CheckEndpoint queries the target endpoint using HTTP GET to verify service liveness.
func CheckEndpoint(ctx context.Context, endpoint string) int {
	statusCode, err := doRequest(ctx, endpoint)
	if err != nil || statusCode != http.StatusOK {
		return 1
	}
	return 0
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	exitFunc(CheckEndpoint(ctx, ResolveEndpoint(os.Args)))
}
