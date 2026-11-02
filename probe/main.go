// Package main implements a zero-dependency healthcheck probe that validates
// Invidious and Invidious Companion availability against their internal HTTP and socket liveness endpoints.
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
	defaultCompanionSocketPath = "/tmp/invidious-companion.sock"
	companionTargetArg         = "companion"
	defaultTimeout             = 3 * time.Second
)

type socketDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

func defaultCreateDialer() socketDialer {
	return &net.Dialer{}
}

func defaultCheckUnixSocket(ctx context.Context, socketPath string) error {
	conn, err := createDialer().DialContext(ctx, "unix", socketPath)
	if err != nil {
		return fmt.Errorf("dialing companion unix socket %q: %w", socketPath, err)
	}
	if closeErr := conn.Close(); closeErr != nil {
		return fmt.Errorf("closing companion unix socket %q: %w", socketPath, closeErr)
	}
	return nil
}

func defaultCreateRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("creating HTTP request: %w", err)
	}
	return req, nil
}

func defaultGetTransport() http.RoundTripper {
	return http.DefaultTransport
}

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
		Transport: getTransport(),
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

var (
	companionSocketPath = defaultCompanionSocketPath
	createDialer        = defaultCreateDialer
	checkUnixSocket     = defaultCheckUnixSocket
	createRequest       = defaultCreateRequest
	getTransport        = defaultGetTransport
	doRequest           = defaultDoRequest
	exitFunc            = os.Exit
)

// ResolveTarget determines whether the healthcheck probe targets the Companion UNIX
// domain socket or falls back to querying the Invidious HTTP statistics endpoint.
func ResolveTarget(args []string) string {
	if len(args) > 1 && strings.TrimSpace(args[1]) == companionTargetArg {
		return companionTargetArg
	}
	return defaultEndpoint
}

// CheckTarget tests service liveness by verifying either UNIX domain socket connectivity
// for the Companion or a successful HTTP status code for the Invidious backend.
func CheckTarget(ctx context.Context, target string) int {
	if target == companionTargetArg {
		if err := checkUnixSocket(ctx, companionSocketPath); err != nil {
			return 1
		}
		return 0
	}

	statusCode, err := doRequest(ctx, target)
	if err != nil || statusCode != http.StatusOK {
		return 1
	}
	return 0
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	exitFunc(CheckTarget(ctx, ResolveTarget(os.Args)))
}
