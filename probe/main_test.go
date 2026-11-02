package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	errMockSocket    = errors.New("simulated unix socket failure")
	errMockClose     = errors.New("simulated response body close error")
	errMockRead      = errors.New("simulated response body read error")
	errMockRequest   = errors.New("simulated request creation failure")
	errMockTransport = errors.New("simulated transport failure")
)

type mockRoundTripper struct {
	roundTripFunc func(req *http.Request) (*http.Response, error)
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.roundTripFunc(req)
}

var currentMockTransport http.RoundTripper

func mockTransportProvider() http.RoundTripper {
	return currentMockTransport
}

type closeFailingReader struct {
	io.Reader
}

func (closeFailingReader) Close() error {
	return errMockClose
}

type readFailingReader struct{}

func (readFailingReader) Read(_ []byte) (int, error) {
	return 0, errMockRead
}

func (readFailingReader) Close() error {
	return nil
}

func absentContext() context.Context {
	return nil
}

func mockFailingCreateRequest(_ context.Context, _, _ string, _ io.Reader) (*http.Request, error) {
	return nil, errMockRequest
}

func mockFailingCheckUnixSocket(_ context.Context, _ string) error {
	return errMockSocket
}

func mockSuccessCheckUnixSocket(_ context.Context, _ string) error {
	return nil
}

func mockFailingDoRequest(_ context.Context, _ string) (int, error) {
	return 0, errMockTransport
}

func mockSuccessDoRequest(_ context.Context, _ string) (int, error) {
	return http.StatusOK, nil
}

type mockErrorCloserConn struct {
	net.Conn
}

func (mockErrorCloserConn) Close() error {
	return errMockClose
}

type mockErrorCloserDialer struct{}

func (mockErrorCloserDialer) DialContext(_ context.Context, _, _ string) (net.Conn, error) {
	return mockErrorCloserConn{}, nil
}

func mockErrorCloserCreateDialer() socketDialer {
	return mockErrorCloserDialer{}
}

func TestResolveTarget(t *testing.T) {
	tests := []struct {
		name      string
		want      string
		extraArgs []string
		noArgs    bool
	}{
		{
			name:   "empty args falls back to default endpoint",
			noArgs: true,
			want:   defaultEndpoint,
		},
		{
			name:      "binary name only falls back to default endpoint",
			extraArgs: nil,
			want:      defaultEndpoint,
		},
		{
			name:      "blank argument falls back to default endpoint",
			extraArgs: []string{"   \t\n  "},
			want:      defaultEndpoint,
		},
		{
			name:      "companion argument selects companion target",
			extraArgs: []string{companionTargetArg},
			want:      companionTargetArg,
		},
		{
			name:      "arbitrary argument falls back to default endpoint",
			extraArgs: []string{"unrecognized-target"},
			want:      defaultEndpoint,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var args []string
			if !tc.noArgs {
				args = append([]string{"/usr/local/bin/probe"}, tc.extraArgs...)
			}
			if got := ResolveTarget(args); got != tc.want {
				t.Errorf("ResolveTarget(%v) = %q, want %q", args, got, tc.want)
			}
		})
	}
}

func TestCheckTarget(t *testing.T) {
	origCheckSocket := checkUnixSocket
	origDoRequest := doRequest
	defer func() {
		checkUnixSocket = origCheckSocket
		doRequest = origDoRequest
	}()

	tests := []struct {
		socketFunc func(context.Context, string) error
		reqFunc    func(context.Context, string) (int, error)
		name       string
		target     string
		wantCode   int
	}{
		{
			name:       "companion socket success returns zero",
			target:     companionTargetArg,
			socketFunc: mockSuccessCheckUnixSocket,
			wantCode:   0,
		},
		{
			name:       "companion socket failure returns one",
			target:     companionTargetArg,
			socketFunc: mockFailingCheckUnixSocket,
			wantCode:   1,
		},
		{
			name:     "http endpoint success returns zero",
			target:   "http://probe.example.com:3000/api/v1/stats",
			reqFunc:  mockSuccessDoRequest,
			wantCode: 0,
		},
		{
			name:     "http endpoint failure returns one",
			target:   "http://probe.example.net:3000/api/v1/stats",
			reqFunc:  mockFailingDoRequest,
			wantCode: 1,
		},
		{
			name:   "http endpoint non-200 status returns one",
			target: "http://probe.example.org:3000/api/v1/stats",
			reqFunc: func(_ context.Context, _ string) (int, error) {
				return http.StatusServiceUnavailable, nil
			},
			wantCode: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.socketFunc != nil {
				checkUnixSocket = tc.socketFunc
			}
			if tc.reqFunc != nil {
				doRequest = tc.reqFunc
			}

			if got := CheckTarget(context.Background(), tc.target); got != tc.wantCode {
				t.Errorf("CheckTarget(%q) = %d, want %d", tc.target, got, tc.wantCode)
			}
		})
	}
}

func TestDefaultCreateDialer(t *testing.T) {
	d := defaultCreateDialer()
	if d == nil {
		t.Fatal("expected non-nil dialer, got nil")
	}
}

func TestDefaultCheckUnixSocket(t *testing.T) {
	tempDir := t.TempDir()
	socketPath := filepath.Join(tempDir, "c.sock")

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", socketPath)
	if err != nil {
		t.Fatalf("lc.Listen() error: %v", err)
	}

	if err := defaultCheckUnixSocket(context.Background(), socketPath); err != nil {
		t.Errorf("defaultCheckUnixSocket(active) error = %v", err)
	}

	if closeErr := ln.Close(); closeErr != nil {
		t.Errorf("closing listener: %v", closeErr)
	}

	if err := defaultCheckUnixSocket(context.Background(), socketPath); err == nil {
		t.Error("defaultCheckUnixSocket(closed) expected error, got nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := defaultCheckUnixSocket(ctx, socketPath); err == nil {
		t.Error("defaultCheckUnixSocket(canceled context) expected error, got nil")
	}

	origDialer := createDialer
	createDialer = mockErrorCloserCreateDialer
	t.Cleanup(func() {
		createDialer = origDialer
	})

	if err := defaultCheckUnixSocket(context.Background(), socketPath); !errors.Is(err, errMockClose) {
		t.Errorf("defaultCheckUnixSocket(close failure) error = %v, want %v", err, errMockClose)
	}
}

func TestDefaultCreateRequest(t *testing.T) {
	if _, err := defaultCreateRequest(context.Background(), http.MethodGet, "http://probe.example.net:3000/api/v1/stats", http.NoBody); err != nil {
		t.Fatalf("defaultCreateRequest() error = %v", err)
	}

	if _, err := defaultCreateRequest(absentContext(), http.MethodGet, "http://absent.example.net:3000/api/v1/stats", http.NoBody); err == nil {
		t.Error("defaultCreateRequest() expected an error for an absent context")
	}
}

func TestDefaultGetTransport(t *testing.T) {
	if got := defaultGetTransport(); got != http.DefaultTransport {
		t.Errorf("defaultGetTransport() = %v, want %v", got, http.DefaultTransport)
	}
}

func TestDefaultDoRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthy" {
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte("OK")); err != nil {
				t.Errorf("w.Write error: %v", err)
			}
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	statusCode, err := defaultDoRequest(context.Background(), server.URL+"/healthy")
	if err != nil || statusCode != http.StatusOK {
		t.Fatalf("defaultDoRequest(healthy) = %d, err = %v", statusCode, err)
	}

	statusCode, err = defaultDoRequest(context.Background(), server.URL+"/missing")
	if err != nil || statusCode != http.StatusNotFound {
		t.Fatalf("defaultDoRequest(missing) = %d, err = %v", statusCode, err)
	}
}

func TestDefaultDoRequest_RequestCreationFailure(t *testing.T) {
	originalCreateRequest := createRequest
	createRequest = mockFailingCreateRequest
	defer func() {
		createRequest = originalCreateRequest
	}()

	if _, err := defaultDoRequest(context.Background(), "http://req.example.org:3000/api/v1/stats"); !errors.Is(err, errMockRequest) {
		t.Errorf("defaultDoRequest() error = %v, want %v", err, errMockRequest)
	}
}

func TestDefaultDoRequest_CloseAndReadFailures(t *testing.T) {
	originalTransport := getTransport
	getTransport = mockTransportProvider
	defer func() {
		getTransport = originalTransport
	}()

	tests := []struct {
		body    io.ReadCloser
		wantErr error
		name    string
	}{
		{
			body:    closeFailingReader{Reader: strings.NewReader("OK")},
			wantErr: errMockClose,
			name:    "body close error propagates",
		},
		{
			body:    readFailingReader{},
			wantErr: errMockRead,
			name:    "body drain error propagates",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			currentMockTransport = &mockRoundTripper{
				roundTripFunc: func(_ *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       tc.body,
					}, nil
				},
			}

			if _, err := defaultDoRequest(context.Background(), "http://io.example.org:3000/api/v1/stats"); !errors.Is(err, tc.wantErr) {
				t.Errorf("defaultDoRequest() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestDefaultDoRequest_InvalidURL(t *testing.T) {
	tests := []struct {
		urlStr string
		name   string
	}{
		{
			urlStr: "http://invalid-domain-with-space :3000",
			name:   "invalid URL with space",
		},
		{
			urlStr: "ftp://127.0.0.1:3000/api/v1/stats",
			name:   "unsupported URL protocol scheme",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := defaultDoRequest(context.Background(), tc.urlStr); err == nil {
				t.Errorf("defaultDoRequest(%q) expected error, got nil", tc.urlStr)
			}
		})
	}
}

func TestDefaultDoRequest_ExecutionFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := defaultDoRequest(ctx, "http://canceled.example.org:3000/api/v1/stats"); err == nil {
		t.Error("defaultDoRequest(canceled) expected error, got nil")
	}
}

var recordedExitCode = -1

func mockExitRecorder(code int) {
	recordedExitCode = code
}

func TestMain_Execution(t *testing.T) {
	originalExit := exitFunc
	originalDoRequest := doRequest
	originalCheckSocket := checkUnixSocket
	originalArgs := os.Args
	defer func() {
		exitFunc = originalExit
		doRequest = originalDoRequest
		checkUnixSocket = originalCheckSocket
		os.Args = originalArgs
	}()

	tests := []struct {
		reqFunc    func(context.Context, string) (int, error)
		socketFunc func(context.Context, string) error
		name       string
		extraArgs  []string
		wantCode   int
	}{
		{
			name:     "healthy invidious endpoint exits with zero",
			reqFunc:  mockSuccessDoRequest,
			wantCode: 0,
		},
		{
			name:     "failing invidious endpoint exits with one",
			reqFunc:  mockFailingDoRequest,
			wantCode: 1,
		},
		{
			name:       "healthy companion target exits with zero",
			socketFunc: mockSuccessCheckUnixSocket,
			extraArgs:  []string{companionTargetArg},
			wantCode:   0,
		},
		{
			name:       "failing companion target exits with one",
			socketFunc: mockFailingCheckUnixSocket,
			extraArgs:  []string{companionTargetArg},
			wantCode:   1,
		},
	}

	exitFunc = mockExitRecorder
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doRequest = tc.reqFunc
			checkUnixSocket = tc.socketFunc
			os.Args = append([]string{"probe"}, tc.extraArgs...)
			recordedExitCode = -1

			main()

			if recordedExitCode != tc.wantCode {
				t.Errorf("main() recorded exit code = %d, want %d", recordedExitCode, tc.wantCode)
			}
		})
	}
}
