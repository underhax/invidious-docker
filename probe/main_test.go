package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	errMockTransport = errors.New("simulated transport failure")
	errMockClose     = errors.New("simulated response body close error")
	errMockRead      = errors.New("simulated response body read error")
	errMockRequest   = errors.New("simulated request creation failure")
)

type mockRoundTripper struct {
	roundTripFunc func(req *http.Request) (*http.Response, error)
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.roundTripFunc(req)
}

var currentMockTransport http.RoundTripper

func mockTransportProvider(_ string) http.RoundTripper {
	return currentMockTransport
}

func mockFailingCreateRequest(_ context.Context, _, _ string, _ io.Reader) (*http.Request, error) {
	return nil, errMockRequest
}

func absentContext() context.Context {
	return nil
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

func TestDefaultGetTransport(t *testing.T) {
	if got := defaultGetTransport("probe.example.com"); got == nil {
		t.Fatal("defaultGetTransport(tcp) returned nil")
	}
	if got := defaultGetTransport(unixHostName); got == nil {
		t.Fatal("defaultGetTransport(unix) returned nil")
	}
}

func TestDefaultDialUnix(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := defaultDialUnix(ctx, "tcp", "unix:80"); err == nil {
		t.Fatal("defaultDialUnix() expected error for canceled context")
	}

	tempDir := t.TempDir()
	socketPath := filepath.Join(tempDir, "test.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", socketPath)
	if err != nil {
		t.Fatalf("lc.Listen() error: %v", err)
	}
	defer func() {
		if closeErr := ln.Close(); closeErr != nil {
			t.Errorf("closing listener: %v", closeErr)
		}
	}()

	origPath := companionSocketPath
	companionSocketPath = socketPath
	t.Cleanup(func() {
		companionSocketPath = origPath
	})

	conn, err := defaultDialUnix(context.Background(), "tcp", "unix:80")
	if err != nil {
		t.Fatalf("defaultDialUnix() unexpected error: %v", err)
	}
	if closeErr := conn.Close(); closeErr != nil {
		t.Errorf("conn.Close() error: %v", closeErr)
	}
}

func TestResolveEndpoint(t *testing.T) {
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
			name:      "companion shorthand selects companion endpoint",
			extraArgs: []string{companionTargetArg},
			want:      defaultCompanionEndpoint,
		},
		{
			name:      "companion full URL selects companion endpoint",
			extraArgs: []string{defaultCompanionEndpoint},
			want:      defaultCompanionEndpoint,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var args []string
			if !tc.noArgs {
				args = append([]string{"/usr/local/bin/probe"}, tc.extraArgs...)
			}
			if got := ResolveEndpoint(args); got != tc.want {
				t.Errorf("ResolveEndpoint(%v) = %q, want %q", args, got, tc.want)
			}
		})
	}
}

func TestCheckEndpoint(t *testing.T) {
	tests := []struct {
		mockTripper *mockRoundTripper
		name        string
		wantCode    int
	}{
		{
			mockTripper: &mockRoundTripper{
				roundTripFunc: func(_ *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader("OK")),
					}, nil
				},
			},
			name:     "healthy response returns success code",
			wantCode: 0,
		},
		{
			mockTripper: &mockRoundTripper{
				roundTripFunc: func(_ *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusServiceUnavailable,
						Body:       io.NopCloser(strings.NewReader("unavailable")),
					}, nil
				},
			},
			name:     "service unavailable returns error code",
			wantCode: 1,
		},
		{
			mockTripper: &mockRoundTripper{
				roundTripFunc: func(_ *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusInternalServerError,
						Body:       io.NopCloser(strings.NewReader("internal error")),
					}, nil
				},
			},
			name:     "internal server error returns error code",
			wantCode: 1,
		},
		{
			mockTripper: &mockRoundTripper{
				roundTripFunc: func(_ *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusNotFound,
						Body:       io.NopCloser(strings.NewReader("not found")),
					}, nil
				},
			},
			name:     "not found returns error code",
			wantCode: 1,
		},
		{
			mockTripper: &mockRoundTripper{
				roundTripFunc: func(_ *http.Request) (*http.Response, error) {
					return nil, errMockTransport
				},
			},
			name:     "transport error returns error code",
			wantCode: 1,
		},
	}

	originalTransport := getTransport
	getTransport = mockTransportProvider
	defer func() {
		getTransport = originalTransport
	}()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			currentMockTransport = tc.mockTripper
			got := CheckEndpoint(context.Background(), "http://invidious.example.com:3000/api/v1/stats")
			if got != tc.wantCode {
				t.Errorf("CheckEndpoint() = %d, want %d", got, tc.wantCode)
			}
		})
	}
}

func TestCheckEndpoint_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := CheckEndpoint(ctx, "http://probe.example.org:3000/api/v1/stats")
	if got != 1 {
		t.Errorf("CheckEndpoint() with cancelled context = %d, want 1", got)
	}
}

func mockFailingDoRequest(_ context.Context, _ string) (int, error) {
	return 0, errMockTransport
}

func TestCheckEndpoint_MockedFailure(t *testing.T) {
	originalDoRequest := doRequest
	doRequest = mockFailingDoRequest
	defer func() {
		doRequest = originalDoRequest
	}()

	got := CheckEndpoint(context.Background(), "http://health.example:3000/api/v1/stats")
	if got != 1 {
		t.Errorf("CheckEndpoint() with mocked transport error = %d, want 1", got)
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

var recordedExitCode = -1

func mockExitRecorder(code int) {
	recordedExitCode = code
}

func mockSuccessDoRequest(_ context.Context, _ string) (int, error) {
	return http.StatusOK, nil
}

func TestMain_Execution(t *testing.T) {
	originalExit := exitFunc
	originalDoRequest := doRequest
	originalArgs := os.Args
	defer func() {
		exitFunc = originalExit
		doRequest = originalDoRequest
		os.Args = originalArgs
	}()

	tests := []struct {
		reqFunc  func(context.Context, string) (int, error)
		name     string
		wantCode int
	}{
		{
			name:     "healthy endpoint exits with zero",
			reqFunc:  mockSuccessDoRequest,
			wantCode: 0,
		},
		{
			name:     "failing endpoint exits with one",
			reqFunc:  mockFailingDoRequest,
			wantCode: 1,
		},
	}

	exitFunc = mockExitRecorder
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doRequest = tc.reqFunc
			os.Args = []string{"probe"}
			recordedExitCode = -1

			main()

			if recordedExitCode != tc.wantCode {
				t.Errorf("main() recorded exit code = %d, want %d", recordedExitCode, tc.wantCode)
			}
		})
	}
}
