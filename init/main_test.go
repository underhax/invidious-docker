package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	errMockExec     = errors.New("simulated execution failure")
	errMockReadDir  = errors.New("simulated readdir failure")
	errMockReadFile = errors.New("simulated readfile failure")
	errMockListen   = errors.New("simulated tcp listen failure")
	errMockDial     = errors.New("simulated dial failure")
	errMockClose    = errors.New("simulated close failure")
	errMockOpen     = errors.New("simulated open failure")
	errMockWrite    = errors.New("simulated write failure")
	errMockRemove   = errors.New("simulated remove failure")
	errMockScrub    = errors.New("simulated scrub failure")
)

var (
	recordedExitCode     = -1
	capturedArgs         []string
	capturedEnv          []string
	mockDialAttemptCount int
	mockProcDirectory    string
	failingReadTarget    string
	activePipeListener   net.Listener
	activeDialUnixConn   net.Conn
)

type failingCloser struct{}

func (failingCloser) Close() error {
	return errMockClose
}

type pipeDialer struct {
	conn net.Conn
}

func (d *pipeDialer) DialContext(_ context.Context, _, _ string) (net.Conn, error) {
	return d.conn, nil
}

type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{
		conns: make(chan net.Conn, 1),
		done:  make(chan struct{}),
	}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() {
		close(l.done)
	})
	return nil
}

func (*pipeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8282}
}

func mockEphemeralListenTCP(ctx context.Context, _ string) (net.Listener, error) {
	return defaultListenTCP(ctx, "127.0.0.1:0")
}

func mockPipeListenTCP(_ context.Context, _ string) (net.Listener, error) {
	return activePipeListener, nil
}

func mockFailingListenTCP(_ context.Context, _ string) (net.Listener, error) {
	return nil, errMockListen
}

func mockFailingDialUnix(_ context.Context, _ string) (net.Conn, error) {
	return nil, errMockDial
}

func mockConnDialUnix(_ context.Context, _ string) (net.Conn, error) {
	return activeDialUnixConn, nil
}

func mockFailingRemoveFile(_ string) error {
	return errMockRemove
}

func mockExitRecorder(code int) {
	recordedExitCode = code
}

func mockFailingReadDir(_ string) ([]fs.DirEntry, error) {
	return nil, errMockReadDir
}

func mockFailingReadFile(_ string) ([]byte, error) {
	return nil, errMockReadFile
}

func mockSelectiveFailingReadFile(path string) ([]byte, error) {
	if strings.HasSuffix(path, failingReadTarget) {
		return nil, errMockReadFile
	}
	return defaultReadFile(path)
}

func mockSuccessfulExec(_ context.Context, _, _ []string) error {
	return nil
}

func mockFailingExec(_ context.Context, _, _ []string) error {
	return errMockExec
}

func mockSuccessfulCommand(ctx context.Context, _ []string) *exec.Cmd {
	return exec.CommandContext(ctx, "true")
}

func mockFailingCommand(ctx context.Context, _ []string) *exec.Cmd {
	return exec.CommandContext(ctx, "false")
}

func mockNonExistentCommand(ctx context.Context, _ []string) *exec.Cmd {
	return exec.CommandContext(ctx, "/path/that/does/not/exist")
}

func mockTerminatedExec(ctx context.Context, _, _ []string) error {
	return exec.CommandContext(ctx, "sh", "-c", "kill -TERM $$").Run()
}

func mockFailingScrub(_ context.Context, _ int) error {
	return errMockScrub
}

type mockSocketDialer struct {
	err  error
	conn net.Conn
}

func (m *mockSocketDialer) DialContext(_ context.Context, _, _ string) (net.Conn, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.conn, nil
}

var activeMockDialer = &mockSocketDialer{}

func mockCreateDialer() socketDialer {
	return activeMockDialer
}

type mockSuccessConn struct {
	net.Conn
}

func (m *mockSuccessConn) Close() error {
	return nil
}

func mockSuccessDialTCP(_ context.Context, _, _ string) (net.Conn, error) {
	return &mockSuccessConn{}, nil
}

func mockFailingDialTCP(_ context.Context, _, _ string) (net.Conn, error) {
	return nil, errMockDial
}

type mockErrorCloserConn struct {
	net.Conn
}

func (m *mockErrorCloserConn) Close() error {
	return errMockClose
}

func mockErrorCloserDialTCP(_ context.Context, _, _ string) (net.Conn, error) {
	return &mockErrorCloserConn{}, nil
}

func mockRetrySuccessDialTCP(_ context.Context, _, _ string) (net.Conn, error) {
	mockDialAttemptCount++
	if mockDialAttemptCount == 1 {
		return nil, errMockDial
	}
	return &mockSuccessConn{}, nil
}

func mockProcPath(pid int, entry string) string {
	if mockProcDirectory != "" {
		return filepath.Join(mockProcDirectory, strconv.Itoa(pid), entry)
	}
	return defaultProcPath(pid, entry)
}

func mockFailingOpenFile(_ string, _ int, _ os.FileMode) (*os.File, error) {
	return nil, errMockOpen
}

func mockPermissionOpenFile(_ string, _ int, _ os.FileMode) (*os.File, error) {
	return nil, os.ErrPermission
}

func mockPermissionReadFile(_ string) ([]byte, error) {
	return nil, os.ErrPermission
}

func mockFailingWriteMemory(_ *os.File, _ []byte, _ int64) (int, error) {
	return 0, errMockWrite
}

func mockPermissionWriteMemory(_ *os.File, _ []byte, _ int64) (int, error) {
	return 0, os.ErrPermission
}

func mockFailingCloseFile(_ *os.File) error {
	return errMockClose
}

func mockPermissionCloseFile(_ *os.File) error {
	return os.ErrPermission
}

func mockRecordingExec(_ context.Context, args, env []string) error {
	capturedArgs = args
	capturedEnv = env
	return nil
}

func mockNotExistReadDir(_ string) ([]fs.DirEntry, error) {
	return nil, fs.ErrNotExist
}

func makeStatContent(comm, envStart, envEnd string, fieldCount int) string {
	var sb strings.Builder
	sb.WriteString("1234 (")
	sb.WriteString(comm)
	sb.WriteString(")")
	for i := range fieldCount {
		sb.WriteString(" ")
		switch i {
		case statEnvStartIndex:
			sb.WriteString(envStart)
		case statEnvEndIndex:
			sb.WriteString(envEnd)
		default:
			sb.WriteString("0")
		}
	}
	return sb.String()
}

func setupScrubChildFixture(t *testing.T) (pid int, statPath, memPath string) {
	t.Helper()
	tempDir := t.TempDir()
	pid = 4242
	pidDir := filepath.Join(tempDir, strconv.Itoa(pid))
	if err := os.Mkdir(pidDir, 0o750); err != nil {
		t.Fatalf("creating pid dir: %v", err)
	}

	statFilePath := filepath.Join(pidDir, "stat")
	memFilePath := filepath.Join(pidDir, "mem")

	validStatData := makeStatContent("invidious", "10", "20", 49)
	if err := os.WriteFile(filepath.Clean(statFilePath), []byte(validStatData), 0o600); err != nil {
		t.Fatalf("writing stat file: %v", err)
	}

	initialMem := make([]byte, 30)
	for i := range initialMem {
		initialMem[i] = byte(i + 1)
	}
	if err := os.WriteFile(filepath.Clean(memFilePath), initialMem, 0o600); err != nil {
		t.Fatalf("writing mem file: %v", err)
	}

	mockProcDirectory = tempDir
	procPath = mockProcPath
	return pid, statFilePath, memFilePath
}

func setupScrubCleanup(t *testing.T) {
	t.Helper()
	origProcPath := procPath
	origReadFile := readFile
	origOpenFile := openFile
	origWriteMem := writeMemory
	origCloseFile := closeFile
	origDialTCP := dialTCP

	dialTCP = mockSuccessDialTCP

	t.Cleanup(func() {
		procPath = origProcPath
		readFile = origReadFile
		openFile = origOpenFile
		writeMemory = origWriteMem
		closeFile = origCloseFile
		dialTCP = origDialTCP
		mockProcDirectory = ""
	})
}

func TestDefaultReadFile(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "sample.txt")
	expectedContent := []byte("secret_value")

	if err := os.WriteFile(filePath, expectedContent, 0o600); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	data, err := defaultReadFile(filePath)
	if err != nil {
		t.Fatalf("defaultReadFile() unexpected error: %v", err)
	}

	if !bytes.Equal(data, expectedContent) {
		t.Errorf("defaultReadFile() = %q, want %q", data, expectedContent)
	}

	if _, err := defaultReadFile(filepath.Join(tempDir, "nonexistent.txt")); err == nil {
		t.Error("defaultReadFile() expected error on nonexistent file, got nil")
	}
}

func TestDefaultReadDir(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "entry.txt")
	if err := os.WriteFile(filePath, []byte("ok"), 0o600); err != nil {
		t.Fatalf("writing entry file: %v", err)
	}

	entries, err := defaultReadDir(tempDir)
	if err != nil {
		t.Fatalf("defaultReadDir() unexpected error: %v", err)
	}

	if len(entries) != 1 || entries[0].Name() != "entry.txt" {
		t.Errorf("defaultReadDir() unexpected entries: %v", entries)
	}

	if _, err := defaultReadDir(filepath.Join(tempDir, "missing_subdir")); err == nil {
		t.Error("defaultReadDir() expected error on nonexistent directory, got nil")
	}
}

func TestDefaultCloseFile(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "close_test.txt")
	file, err := os.Create(filepath.Clean(filePath))
	if err != nil {
		t.Fatalf("creating test file: %v", err)
	}

	if err := defaultCloseFile(file); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}

	if err := defaultCloseFile(file); err == nil {
		t.Fatal("expected error closing already closed file, got nil")
	}
}

func TestDefaultWriteMemory(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "mem.bin")
	cleanPath := filepath.Clean(filePath)
	file, err := os.OpenFile(cleanPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("creating test file: %v", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("closing file: %v", closeErr)
		}
	}()

	n, err := defaultWriteMemory(file, []byte("payload"), 0)
	if err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}
	if n != 7 {
		t.Fatalf("expected 7 bytes written, got %d", n)
	}

	readOnlyFile, err := os.OpenFile(cleanPath, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("opening read-only file: %v", err)
	}
	defer func() {
		if closeErr := readOnlyFile.Close(); closeErr != nil {
			t.Errorf("closing read-only file: %v", closeErr)
		}
	}()

	if _, err := defaultWriteMemory(readOnlyFile, []byte("data"), 0); err == nil {
		t.Fatal("expected write error on read-only file, got nil")
	}
}

func TestDefaultOpenFile(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "open_test.txt")
	cleanPath := filepath.Clean(filePath)
	if err := os.WriteFile(cleanPath, []byte("sample"), 0o600); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	file, err := defaultOpenFile(cleanPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("unexpected open error: %v", err)
	}
	if closeErr := file.Close(); closeErr != nil {
		t.Fatalf("closing file: %v", closeErr)
	}

	if _, err := defaultOpenFile(filepath.Join(tempDir, "missing_test.txt"), os.O_RDONLY, 0); err == nil {
		t.Fatal("expected error opening missing file, got nil")
	}
}

func TestDefaultCreateDialer(t *testing.T) {
	d := defaultCreateDialer()
	if d == nil {
		t.Fatal("expected non-nil dialer, got nil")
	}
}

func TestDefaultDialTCP(t *testing.T) {
	origDialer := createDialer
	createDialer = mockCreateDialer
	t.Cleanup(func() { createDialer = origDialer })

	activeMockDialer.err = errMockDial
	activeMockDialer.conn = nil
	if _, err := defaultDialTCP(context.Background(), "tcp", "127.0.0.1:9091"); err == nil {
		t.Fatal("expected dial error, got nil")
	}

	activeMockDialer.err = nil
	activeMockDialer.conn = &mockSuccessConn{}
	conn, err := defaultDialTCP(context.Background(), "tcp", "127.0.0.1:9092")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn == nil {
		t.Fatal("expected non-nil conn, got nil")
	}
}

func TestDefaultProcPath(t *testing.T) {
	got := defaultProcPath(42, "stat")
	expected := filepath.Join(defaultProcDir, "42", "stat")
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestResolveReadinessTarget(t *testing.T) {
	tests := []struct {
		name        string
		targetArg   string
		wantNetwork string
		wantAddr    string
	}{
		{
			name:        "default args return invidious tcp target",
			targetArg:   defaultInvidiousBinaryPath,
			wantNetwork: "tcp",
			wantAddr:    net.JoinHostPort(defaultLoopbackAddress, defaultServerPort),
		},
		{
			name:        "companion binary arg returns companion unix socket target",
			targetArg:   defaultCompanionBinaryPath,
			wantNetwork: "unix",
			wantAddr:    defaultCompanionSocketPath,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"init", tc.targetArg}
			gotNet, gotAddr := resolveReadinessTarget(args)
			if gotNet != tc.wantNetwork || gotAddr != tc.wantAddr {
				t.Errorf("resolveReadinessTarget(%v) = (%q, %q), want (%q, %q)", args, gotNet, gotAddr, tc.wantNetwork, tc.wantAddr)
			}
		})
	}
}

func TestParseProcStat(t *testing.T) {
	tests := []struct {
		name         string
		statContent  string
		wantEnvStart int64
		wantEnvEnd   int64
		expectErr    bool
	}{
		{
			name:        "missing_closing_comm_paren",
			statContent: "1234 invidious 0 0 0",
			expectErr:   true,
		},
		{
			name:        "insufficient_fields_after_comm",
			statContent: makeStatContent("invidious", "100", "200", 20),
			expectErr:   true,
		},
		{
			name:        "invalid_env_start_non_number",
			statContent: makeStatContent("invidious", "not_a_number", "200", 49),
			expectErr:   true,
		},
		{
			name:        "invalid_env_end_non_number",
			statContent: makeStatContent("invidious", "100", "not_a_number", 49),
			expectErr:   true,
		},
		{
			name:        "negative_or_zero_env_start",
			statContent: makeStatContent("invidious", "0", "200", 49),
			expectErr:   true,
		},
		{
			name:        "env_start_greater_than_env_end",
			statContent: makeStatContent("invidious", "300", "200", 49),
			expectErr:   true,
		},
		{
			name:        "env_range_exceeds_max_limit",
			statContent: makeStatContent("invidious", "1000", "34554433", 49),
			expectErr:   true,
		},
		{
			name:         "valid_stat_with_nested_comm",
			statContent:  makeStatContent("invidious (worker) proc", "1000", "2000", 49),
			wantEnvStart: 1000,
			wantEnvEnd:   2000,
			expectErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envStart, envEnd, err := parseProcStat(tt.statContent)
			if tt.expectErr {
				if err == nil {
					t.Fatalf("expected error for case %q, got nil", tt.name)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for case %q: %v", tt.name, err)
			}
			if envStart != tt.wantEnvStart || envEnd != tt.wantEnvEnd {
				t.Fatalf("expected (%d, %d), got (%d, %d)", tt.wantEnvStart, tt.wantEnvEnd, envStart, envEnd)
			}
		})
	}
}

func TestWaitForChildReadiness(t *testing.T) {
	origDial := dialTCP
	t.Cleanup(func() { dialTCP = origDial })

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForChildReadiness(canceledCtx, "tcp", "127.0.0.1:3000", time.Second); err == nil {
		t.Fatal("expected error on canceled context, got nil")
	}

	dialTCP = mockSuccessDialTCP
	if err := waitForChildReadiness(context.Background(), "tcp", "127.0.0.1:3001", time.Second); err != nil {
		t.Fatalf("unexpected error on ready socket: %v", err)
	}

	dialTCP = mockErrorCloserDialTCP
	if err := waitForChildReadiness(context.Background(), "tcp", "127.0.0.1:3002", time.Second); err == nil {
		t.Fatal("expected error when socket close fails, got nil")
	}

	dialTCP = mockFailingDialTCP
	if err := waitForChildReadiness(context.Background(), "tcp", "127.0.0.1:3003", -1*time.Second); err != nil {
		t.Fatalf("expected nil when deadline passed, got %v", err)
	}

	ctxWait, cancelWait := context.WithCancel(context.Background())
	time.AfterFunc(2*time.Millisecond, cancelWait)
	if err := waitForChildReadiness(ctxWait, "tcp", "127.0.0.1:3004", 2*time.Second); err == nil {
		t.Fatal("expected error when context canceled during wait, got nil")
	}

	mockDialAttemptCount = 0
	dialTCP = mockRetrySuccessDialTCP
	if err := waitForChildReadiness(context.Background(), "tcp", "127.0.0.1:3005", time.Second); err != nil {
		t.Fatalf("unexpected error on retry dial: %v", err)
	}
}

func TestDefaultScrubChildMemory_ReadinessErrors(t *testing.T) {
	setupScrubCleanup(t)
	pid, _, _ := setupScrubChildFixture(t)

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := defaultScrubChildMemory(canceledCtx, pid); err != nil {
		t.Fatalf("expected nil when readiness context canceled, got %v", err)
	}

	dialTCP = mockErrorCloserDialTCP
	if err := defaultScrubChildMemory(context.Background(), pid); err == nil {
		t.Fatal("expected error on readiness failure, got nil")
	}
}

func TestDefaultScrubChildMemory_ReadAndParseErrors(t *testing.T) {
	setupScrubCleanup(t)
	pid, statFilePath, _ := setupScrubChildFixture(t)

	if err := defaultScrubChildMemory(context.Background(), 9999); err != nil {
		t.Fatalf("expected nil when stat file missing, got %v", err)
	}

	readFile = mockPermissionReadFile
	if err := defaultScrubChildMemory(context.Background(), pid); err != nil {
		t.Fatalf("expected nil on stat read permission error, got %v", err)
	}

	readFile = mockFailingReadFile
	if err := defaultScrubChildMemory(context.Background(), pid); err == nil {
		t.Fatal("expected error on generic stat read error, got nil")
	}
	readFile = defaultReadFile

	if err := os.WriteFile(filepath.Clean(statFilePath), []byte("corrupt stat data"), 0o600); err != nil {
		t.Fatalf("overwriting stat: %v", err)
	}
	if err := defaultScrubChildMemory(context.Background(), pid); err == nil {
		t.Fatal("expected error on corrupt stat data, got nil")
	}
}

func TestDefaultScrubChildMemory_OpenErrors(t *testing.T) {
	setupScrubCleanup(t)
	pid, _, memFilePath := setupScrubChildFixture(t)

	if err := os.Remove(filepath.Clean(memFilePath)); err != nil {
		t.Fatalf("removing mem file: %v", err)
	}
	if err := defaultScrubChildMemory(context.Background(), pid); err != nil {
		t.Fatalf("expected nil when mem file missing, got %v", err)
	}

	openFile = mockPermissionOpenFile
	if err := defaultScrubChildMemory(context.Background(), pid); err != nil {
		t.Fatalf("expected nil on mem open permission error, got %v", err)
	}

	openFile = mockFailingOpenFile
	if err := defaultScrubChildMemory(context.Background(), pid); err == nil {
		t.Fatal("expected error on generic mem open error, got nil")
	}
}

func TestDefaultScrubChildMemory_WriteErrors(t *testing.T) {
	setupScrubCleanup(t)
	pid, _, _ := setupScrubChildFixture(t)

	writeMemory = mockPermissionWriteMemory
	if err := defaultScrubChildMemory(context.Background(), pid); err != nil {
		t.Fatalf("expected nil on mem write permission error, got %v", err)
	}

	writeMemory = mockFailingWriteMemory
	if err := defaultScrubChildMemory(context.Background(), pid); err == nil {
		t.Fatal("expected error on generic mem write error, got nil")
	}
}

func TestDefaultScrubChildMemory_CloseErrors(t *testing.T) {
	setupScrubCleanup(t)
	pid, _, _ := setupScrubChildFixture(t)

	closeFile = mockPermissionCloseFile
	if err := defaultScrubChildMemory(context.Background(), pid); err != nil {
		t.Fatalf("expected nil on mem close permission error, got %v", err)
	}

	closeFile = mockFailingCloseFile
	if err := defaultScrubChildMemory(context.Background(), pid); err == nil {
		t.Fatal("expected error on generic mem close error, got nil")
	}
}

func TestDefaultScrubChildMemory_Success(t *testing.T) {
	setupScrubCleanup(t)
	pid, _, memFilePath := setupScrubChildFixture(t)

	if err := defaultScrubChildMemory(context.Background(), pid); err != nil {
		t.Fatalf("unexpected error on successful scrub: %v", err)
	}

	scrubbedData, err := os.ReadFile(filepath.Clean(memFilePath))
	if err != nil {
		t.Fatalf("reading scrubbed mem file: %v", err)
	}
	for i := range 10 {
		if scrubbedData[i] != byte(i+1) {
			t.Fatalf("expected untouched byte at %d, got %d", i, scrubbedData[i])
		}
	}
	for i := 10; i < 20; i++ {
		if scrubbedData[i] != 0 {
			t.Fatalf("expected zeroed byte at %d, got %d", i, scrubbedData[i])
		}
	}
	for i := 20; i < 30; i++ {
		if scrubbedData[i] != byte(i+1) {
			t.Fatalf("expected untouched byte at %d, got %d", i, scrubbedData[i])
		}
	}
}

func TestDefaultCreateCommand(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		wantCmd  string
		args     []string
		wantArgs []string
	}{
		{
			name:     "empty args invokes default binary without extra arguments",
			args:     nil,
			wantCmd:  defaultBinaryPath,
			wantArgs: []string{defaultBinaryPath},
		},
		{
			name:     "custom flag args are passed directly",
			args:     []string{"--debug"},
			wantCmd:  defaultBinaryPath,
			wantArgs: []string{defaultBinaryPath, "--debug"},
		},
		{
			name:     "companion binary path is executed directly",
			args:     []string{defaultCompanionBinaryPath, "--verbose"},
			wantCmd:  defaultCompanionBinaryPath,
			wantArgs: []string{defaultCompanionBinaryPath, "--verbose"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := defaultCreateCommand(ctx, tt.args)
			if cmd.Path != tt.wantCmd && !slices.Contains(cmd.Args, tt.wantCmd) {
				t.Errorf("unexpected command path: %s", cmd.Path)
			}
			if !slices.Equal(cmd.Args, tt.wantArgs) {
				t.Errorf("defaultCreateCommand() args = %v, want %v", cmd.Args, tt.wantArgs)
			}
		})
	}

	t.Run("cancel command returns nil when process not started", func(t *testing.T) {
		cmd := defaultCreateCommand(ctx, nil)
		if cmd.Cancel == nil {
			t.Fatal("expected Cancel function to be configured")
		}
		if err := cmd.Cancel(); err != nil {
			t.Errorf("cmd.Cancel() unexpected error: %v", err)
		}
	})

	t.Run("cancel command signals running process", func(t *testing.T) {
		cmd := exec.CommandContext(ctx, "sleep", "10")
		cancelFn := cancelCommand(cmd)
		if err := cmd.Start(); err != nil {
			t.Fatalf("cmd.Start() unexpected error: %v", err)
		}
		defer func() {
			if waitErr := cmd.Wait(); waitErr != nil && !IsTermination(ctx, waitErr) {
				t.Errorf("cmd.Wait() unexpected error: %v", waitErr)
			}
		}()
		if err := cancelFn(); err != nil {
			t.Errorf("cancelFn() unexpected error: %v", err)
		}
	})
}

func TestDefaultExec(t *testing.T) {
	ctx := context.Background()
	origCreate := createCommand
	origScrub := scrubChildMemory
	t.Cleanup(func() {
		createCommand = origCreate
		scrubChildMemory = origScrub
	})

	createCommand = mockNonExistentCommand
	if err := defaultExec(ctx, nil, nil); err == nil {
		t.Fatal("expected error executing non-existent binary, got nil")
	}

	createCommand = mockFailingCommand
	if err := defaultExec(ctx, nil, nil); err == nil {
		t.Fatal("expected error when command exits with failure, got nil")
	}

	createCommand = mockSuccessfulCommand
	scrubChildMemory = mockFailingScrub
	if err := defaultExec(ctx, nil, nil); err != nil {
		t.Fatalf("unexpected error executing true binary with failing scrub: %v", err)
	}

	scrubChildMemory = defaultScrubChildMemory
	if err := defaultExec(ctx, []string{"test"}, []string{"FOO=BAR"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareEnvironment_Errors(t *testing.T) {
	origReadDir := readDir
	origReadFile := readFile
	defer func() {
		readDir = origReadDir
		readFile = origReadFile
		failingReadTarget = ""
	}()

	tests := []struct {
		expectedErr error
		setup       func(dir string)
		name        string
		useAbsent   bool
	}{
		{
			expectedErr: errMockReadDir,
			name:        "readDir error propagates",
			setup: func(_ string) {
				readDir = mockFailingReadDir
			},
		},
		{
			expectedErr: nil,
			name:        "missing directory returns base environment",
			setup: func(_ string) {
				readDir = origReadDir
				readFile = origReadFile
			},
			useAbsent: true,
		},
		{
			expectedErr: errMockReadFile,
			name:        "dynamic secret readFile error propagates",
			setup: func(dir string) {
				readDir = origReadDir
				readFile = mockFailingReadFile
				if err := os.WriteFile(filepath.Join(dir, "inv_token"), []byte("value"), 0o600); err != nil {
					t.Fatalf("writing test secret file: %v", err)
				}
			},
		},
		{
			expectedErr: errInvalidSecretKey,
			name:        "invalid secret key returns error",
			setup: func(dir string) {
				readDir = origReadDir
				readFile = origReadFile
				if err := os.WriteFile(filepath.Join(dir, "inv_bad=key"), []byte("val"), 0o600); err != nil {
					t.Fatalf("writing test secret file: %v", err)
				}
			},
		},
		{
			expectedErr: errMockReadFile,
			name:        "db_name read error propagates",
			setup: func(_ string) {
				readDir = origReadDir
				failingReadTarget = fileDBName
				readFile = mockSelectiveFailingReadFile
			},
		},
		{
			expectedErr: errMockReadFile,
			name:        "db_user read error propagates",
			setup: func(_ string) {
				readDir = origReadDir
				failingReadTarget = fileDBUser
				readFile = mockSelectiveFailingReadFile
			},
		},
		{
			expectedErr: errMockReadFile,
			name:        "db_password read error propagates",
			setup: func(_ string) {
				readDir = origReadDir
				failingReadTarget = fileDBPassword
				readFile = mockSelectiveFailingReadFile
			},
		},
		{
			expectedErr: errMockReadFile,
			name:        "proxy_host read error propagates",
			setup: func(_ string) {
				readDir = origReadDir
				failingReadTarget = fileProxyHost
				readFile = mockSelectiveFailingReadFile
			},
		},
		{
			expectedErr: errMockReadFile,
			name:        "proxy_port read error propagates",
			setup: func(_ string) {
				readDir = origReadDir
				failingReadTarget = fileProxyPort
				readFile = mockSelectiveFailingReadFile
			},
		},
		{
			expectedErr: errMockReadFile,
			name:        "proxy_user read error propagates",
			setup: func(_ string) {
				readDir = origReadDir
				failingReadTarget = fileProxyUser
				readFile = mockSelectiveFailingReadFile
			},
		},
		{
			expectedErr: errMockReadFile,
			name:        "proxy_pass read error propagates",
			setup: func(_ string) {
				readDir = origReadDir
				failingReadTarget = fileProxyPass
				readFile = mockSelectiveFailingReadFile
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caseDir := t.TempDir()
			if tt.useAbsent {
				caseDir = filepath.Join(caseDir, "absent_dir")
			}
			tt.setup(caseDir)
			res, err := PrepareEnvironment([]string{"BASE=1"}, caseDir)
			if tt.expectedErr != nil {
				if !errors.Is(err, tt.expectedErr) {
					t.Fatalf("expected error %v, got %v", tt.expectedErr, err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !slices.Contains(res, "BASE=1") {
					t.Errorf("expected base env preserved, got %v", res)
				}
			}
		})
	}
}

func TestPrepareEnvironment_ScanningAndStructuredSecrets(t *testing.T) {
	origReadDir := readDir
	origReadFile := readFile
	defer func() {
		readDir = origReadDir
		readFile = origReadFile
	}()

	readDir = defaultReadDir
	readFile = defaultReadFile

	tests := []struct {
		files       map[string]string
		wantErr     error
		name        string
		baseEnv     []string
		wantEntries []string
	}{
		{
			name:    "dynamic secrets strip prefix and overwrite existing keys",
			baseEnv: []string{"INITIAL=VAL", "SERVER_SECRET_KEY=old", "INVIDIOUS_CAPTCHA_ENABLED=true"},
			files: map[string]string{
				"ignored_file.txt":      "ignore",
				"inv_":                  "empty_key",
				"inv_empty_val":         "   \n\t  ",
				"inv_invidious_hmac":    "hmac_secret_123\n",
				"inv_invidious_admins":  "admin1, admin2\n",
				"inv_server_secret_key": "new_companion_secret",
			},
			wantEntries: []string{
				"INITIAL=VAL",
				"INVIDIOUS_HMAC=hmac_secret_123",
				`INVIDIOUS_ADMINS=["admin1","admin2"]`,
				"SERVER_SECRET_KEY=new_companion_secret",
				"INVIDIOUS_CONFIG={}",
				`INVIDIOUS_INVIDIOUS_COMPANION=[{"private_url":"http://127.0.0.1:8282/companion","builtin_proxy":true}]`,
				"INVIDIOUS_HOST_BINDING=0.0.0.0",
				"INVIDIOUS_PORT=3000",
				"INVIDIOUS_CHECK_TABLES=true",
				"INVIDIOUS_CAPTCHA_ENABLED=false",
				"SERVER_USE_UNIX_SOCKET=true",
				"SERVER_UNIX_SOCKET_PATH=/tmp/invidious-companion.sock",
				"SERVER_BASE_PATH=/companion",
				"CACHE_ENABLED=true",
				"CACHE_DIRECTORY=/var/tmp",
				"HOME=/tmp",
			},
		},
		{
			name: "pre-formatted json admins secret is preserved",
			baseEnv: []string{
				"INVIDIOUS_DEFAULT_USER_PREFERENCES_DARK_MODE=dark",
				"INVIDIOUS_DEFAULT_USER_PREFERENCES_MAX_RESULTS=100",
				"INVIDIOUS_DEFAULT_USER_PREFERENCES_SPEED=1.25",
				"INVIDIOUS_DEFAULT_USER_PREFERENCES_AUTOPLAY=false",
				"INVIDIOUS_DEFAULT_USER_PREFERENCES_CAPTIONS=English",
				"INVIDIOUS_DEFAULT_USER_PREFERENCES_COMMENTS=youtube,reddit",
				`INVIDIOUS_DEFAULT_USER_PREFERENCES_FEED_MENU=["Subscriptions","Playlists"]`,
				"INVIDIOUS_VIDEOJS_GOAL_BUFFER_LENGTH=30",
				"INVIDIOUS_JOBS_CLEAR_EXPIRED_ITEMS_ENABLE=true",
			},
			files: map[string]string{
				"inv_invidious_admins": `["root_admin"]`,
			},
			wantEntries: []string{
				`INVIDIOUS_ADMINS=["root_admin"]`,
				`INVIDIOUS_DEFAULT_USER_PREFERENCES={"dark_mode":"dark","max_results":100,"speed":1.25,"autoplay":false,"captions":["English","",""],"comments":["youtube","reddit"],"feed_menu":["Subscriptions","Playlists"]}`,
				`INVIDIOUS_VIDEOJS={"goal_buffer_length":30}`,
				`INVIDIOUS_JOBS={"clear_expired_items":{"enable":true}}`,
			},
		},
		{
			name:    "database secrets build unix socket database url",
			baseEnv: []string{"EXISTING_VAR=kept"},
			files: map[string]string{
				fileDBName:     "invidious",
				fileDBUser:     "kemal",
				fileDBPassword: "s3cr3t_password",
			},
			wantEntries: []string{
				"INVIDIOUS_DATABASE_URL=postgres://kemal:s3cr3t_password@/invidious?host=/var/run/postgresql/.s.PGSQL.5432",
			},
		},
		{
			name:    "incomplete database secrets return error",
			baseEnv: nil,
			files: map[string]string{
				fileDBName: "invidious",
				fileDBUser: "kemal",
			},
			wantErr: errIncompleteDatabaseSecrets,
		},
		{
			name:    "proxy secrets with credentials build authenticated socks5 url",
			baseEnv: nil,
			files: map[string]string{
				fileProxyHost: "proxy.example.com",
				fileProxyPort: "7890",
				fileProxyUser: "warp_user",
				fileProxyPass: "warp_pass",
			},
			wantEntries: []string{
				"PROXY=socks5://warp_user:warp_pass@proxy.example.com:7890",
			},
		},
		{
			name:    "proxy secrets without credentials build unauthenticated socks5 url",
			baseEnv: nil,
			files: map[string]string{
				fileProxyHost: "proxy.example.net",
				fileProxyPort: "1080",
			},
			wantEntries: []string{
				"PROXY=socks5://proxy.example.net:1080",
			},
		},
		{
			name:    "proxy host without port returns error",
			baseEnv: nil,
			files: map[string]string{
				fileProxyHost: "proxy.example.org",
			},
			wantErr: errIncompleteProxySecrets,
		},
		{
			name:    "proxy user without pass returns error",
			baseEnv: nil,
			files: map[string]string{
				fileProxyHost: "auth.example.org",
				fileProxyPort: "7890",
				fileProxyUser: "only_user",
			},
			wantErr: errIncompleteProxySecrets,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			if err := os.Mkdir(filepath.Join(tempDir, "inv_directory"), 0o700); err != nil {
				t.Fatalf("creating test subdir: %v", err)
			}
			for filename, content := range tc.files {
				if err := os.WriteFile(filepath.Join(tempDir, filename), []byte(content), 0o600); err != nil {
					t.Fatalf("creating test file %s: %v", filename, err)
				}
			}

			res, err := PrepareEnvironment(tc.baseEnv, tempDir)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("PrepareEnvironment() error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("PrepareEnvironment() unexpected error: %v", err)
			}

			for _, entry := range tc.wantEntries {
				if !slices.Contains(res, entry) {
					t.Errorf("missing expected environment entry %q in %v", entry, res)
				}
			}
		})
	}
}

func TestBuildCommandArguments(t *testing.T) {
	tests := []struct {
		name       string
		defaultCmd string
		args       []string
		want       []string
	}{
		{
			name:       "empty input produces empty args when default command is empty",
			defaultCmd: "",
			args:       nil,
			want:       []string{},
		},
		{
			name:       "empty input produces default command when set",
			defaultCmd: "daemon",
			args:       nil,
			want:       []string{"daemon"},
		},
		{
			name:       "custom command arguments are returned directly",
			defaultCmd: "",
			args:       []string{"--config", "/config/config.yaml"},
			want:       []string{"--config", "/config/config.yaml"},
		},
		{
			name:       "explicit binary argument is stripped and returns subcommands",
			defaultCmd: "",
			args:       []string{defaultBinaryPath, "--port", "3000"},
			want:       []string{"--port", "3000"},
		},
		{
			name:       "explicit binary argument alone falls back to empty when default command is empty",
			defaultCmd: "",
			args:       []string{defaultBinaryPath},
			want:       []string{},
		},
		{
			name:       "explicit binary argument alone falls back to default command when set",
			defaultCmd: "start",
			args:       []string{defaultBinaryPath},
			want:       []string{"start"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildCommandArguments(tt.defaultCmd, tt.args)
			if !slices.Equal(got, tt.want) {
				t.Errorf("BuildCommandArguments() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExitCode(t *testing.T) {
	tests := []struct {
		err      error
		name     string
		expected int
	}{
		{
			err:      nil,
			expected: 0,
			name:     "nil error returns zero exit code",
		},
		{
			err:      errors.New("generic failure"),
			expected: 1,
			name:     "generic error returns exit code one",
		},
		{
			err: func() error {
				cmd := exec.CommandContext(t.Context(), "sh", "-c", "exit 42")
				return cmd.Run()
			}(),
			expected: 42,
			name:     "exit error extracts subprocess status code",
		},
		{
			err:      exec.CommandContext(t.Context(), "sh", "-c", "kill -TERM $$").Run(),
			expected: 0,
			name:     "termination signal returns zero exit code",
		},
		{
			err:      exec.CommandContext(t.Context(), "sh", "-c", "kill -INT $$").Run(),
			expected: 0,
			name:     "interrupt signal returns zero exit code",
		},
		{
			err:      exec.CommandContext(t.Context(), "sh", "-c", "kill -ABRT $$").Run(),
			expected: 134,
			name:     "other signal returns 128 plus signal code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExitCode(tt.err); got != tt.expected {
				t.Errorf("ExitCode(%v) = %d, want %d", tt.err, got, tt.expected)
			}
		})
	}
}

func TestIsTermination(t *testing.T) {
	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()

	tests := []struct {
		ctx  context.Context
		err  error
		name string
		want bool
	}{
		{
			ctx:  t.Context(),
			err:  nil,
			name: "nil error with active context",
			want: false,
		},
		{
			ctx:  t.Context(),
			err:  errors.New("standard error"),
			name: "standard error with active context",
			want: false,
		},
		{
			ctx:  canceledCtx,
			err:  context.Canceled,
			name: "canceled context error",
			want: true,
		},
		{
			ctx:  t.Context(),
			err:  exec.CommandContext(t.Context(), "sh", "-c", "kill -TERM $$").Run(),
			name: "subprocess killed by sigterm",
			want: true,
		},
		{
			ctx:  t.Context(),
			err:  exec.CommandContext(t.Context(), "sh", "-c", "kill -INT $$").Run(),
			name: "subprocess killed by sigint",
			want: true,
		},
		{
			ctx:  t.Context(),
			err:  exec.CommandContext(t.Context(), "sh", "-c", "kill -KILL $$").Run(),
			name: "subprocess killed by sigkill",
			want: true,
		},
		{
			ctx:  t.Context(),
			err:  exec.CommandContext(t.Context(), "false").Run(),
			name: "subprocess non-signal failure",
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTermination(tc.ctx, tc.err); got != tc.want {
				t.Errorf("IsTermination() = %v, want %v", got, tc.want)
			}
		})
	}
}

var activePipeDialerConn net.Conn

func mockPipeCreateDialer() socketDialer {
	return &pipeDialer{conn: activePipeDialerConn}
}

func TestDefaultRemoveFile(t *testing.T) {
	tempDir := t.TempDir()
	missingPath := filepath.Join(tempDir, "missing.sock")
	if err := defaultRemoveFile(missingPath); err != nil {
		t.Fatalf("defaultRemoveFile() unexpected error on missing file: %v", err)
	}

	existingPath := filepath.Join(tempDir, "existing.sock")
	if err := os.WriteFile(existingPath, []byte("sock"), 0o600); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	if err := defaultRemoveFile(existingPath); err != nil {
		t.Fatalf("defaultRemoveFile() unexpected error removing file: %v", err)
	}

	nonEmptyDir := filepath.Join(tempDir, "nonempty")
	if err := os.Mkdir(nonEmptyDir, 0o700); err != nil {
		t.Fatalf("creating dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nonEmptyDir, "child"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing child file: %v", err)
	}
	if err := defaultRemoveFile(nonEmptyDir); err == nil {
		t.Fatal("defaultRemoveFile() expected error on non-empty directory, got nil")
	}
}

func TestStartSocketProxy(t *testing.T) {
	pl := newPipeListener()
	clientConn, proxyServerConn := net.Pipe()
	defer closeCloser(clientConn)

	backendClientConn, backendServerConn := net.Pipe()
	defer closeCloser(backendServerConn)

	originalListen := listenTCP
	originalCreateDialer := createDialer
	defer func() {
		listenTCP = originalListen
		createDialer = originalCreateDialer
		activePipeListener = nil
		activePipeDialerConn = nil
	}()

	activePipeListener = pl
	activePipeDialerConn = backendClientConn
	listenTCP = mockPipeListenTCP
	createDialer = mockPipeCreateDialer

	go func() {
		defer closeCloser(backendServerConn)
		buf := make([]byte, 4)
		if _, readErr := io.ReadFull(backendServerConn, buf); readErr != nil {
			return
		}
		if _, writeErr := backendServerConn.Write([]byte("PONG")); writeErr != nil {
			return
		}
	}()

	proxyCloser, err := StartSocketProxy(context.Background(), defaultProxyListenAddr, defaultBridgedCompanionSocketPath)
	if err != nil {
		t.Fatalf("StartSocketProxy() error = %v", err)
	}
	defer closeCloser(proxyCloser)

	pl.conns <- proxyServerConn

	if _, err := clientConn.Write([]byte("PING")); err != nil {
		t.Fatalf("write to proxy: %v", err)
	}

	reply := make([]byte, 4)
	if _, err := io.ReadFull(clientConn, reply); err != nil {
		t.Fatalf("read from proxy: %v", err)
	}

	if !bytes.Equal(reply, []byte("PONG")) {
		t.Errorf("proxy reply = %q, want %q", reply, "PONG")
	}
}

func TestHandleProxyConnection_EdgeCases(t *testing.T) {
	closeCloser(failingCloser{})

	if _, err := defaultListenTCP(context.Background(), "127.0.0.1:-1"); err == nil {
		t.Error("defaultListenTCP() expected error on invalid port, got nil")
	}

	if _, err := defaultDialUnix(context.Background(), "/nonexistent/companion.sock"); err == nil {
		t.Error("defaultDialUnix() expected error on missing socket, got nil")
	}

	t.Run("dial failure closes client connection", func(_ *testing.T) {
		originalDial := dialUnix
		defer func() {
			dialUnix = originalDial
		}()
		dialUnix = mockFailingDialUnix

		c1, c2 := net.Pipe()
		defer closeCloser(c2)

		handleProxyConnection(context.Background(), c1, "/nonexistent/sock")
	})

	t.Run("context cancellation closes active relay", func(_ *testing.T) {
		originalDial := dialUnix
		defer func() {
			dialUnix = originalDial
			activeDialUnixConn = nil
		}()

		b1, b2 := net.Pipe()
		defer closeCloser(b2)

		activeDialUnixConn = b1
		dialUnix = mockConnDialUnix

		c1, c2 := net.Pipe()
		defer closeCloser(c2)

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()

		handleProxyConnection(ctx, c1, "ignored")
	})
}

func TestRun(t *testing.T) {
	origReadDir := readDir
	origReadFile := readFile
	origExecProcess := execProcess
	origListenTCP := listenTCP
	origRemoveFile := removeFile
	defer func() {
		readDir = origReadDir
		readFile = origReadFile
		execProcess = origExecProcess
		listenTCP = origListenTCP
		removeFile = origRemoveFile
	}()

	tempDir := t.TempDir()
	readDir = defaultReadDir
	readFile = defaultReadFile

	capturedArgs = nil
	capturedEnv = nil

	tests := []struct {
		execFunc     func(context.Context, []string, []string) error
		readDirFunc  func(string) ([]fs.DirEntry, error)
		listenFunc   func(context.Context, string) (net.Listener, error)
		removeFunc   func(string) error
		name         string
		useCompanion bool
		expectError  bool
	}{
		{
			name:         "successful invidious execution starts proxy and passes arguments",
			execFunc:     mockRecordingExec,
			readDirFunc:  defaultReadDir,
			listenFunc:   mockEphemeralListenTCP,
			removeFunc:   defaultRemoveFile,
			useCompanion: false,
			expectError:  false,
		},
		{
			name:         "successful companion execution cleans socket and passes arguments",
			execFunc:     mockRecordingExec,
			readDirFunc:  defaultReadDir,
			listenFunc:   mockEphemeralListenTCP,
			removeFunc:   defaultRemoveFile,
			useCompanion: true,
			expectError:  false,
		},
		{
			name:         "companion socket cleanup failure propagates error",
			execFunc:     mockRecordingExec,
			readDirFunc:  defaultReadDir,
			listenFunc:   mockEphemeralListenTCP,
			removeFunc:   mockFailingRemoveFile,
			useCompanion: true,
			expectError:  true,
		},
		{
			name:         "socket proxy listener failure propagates error",
			execFunc:     mockRecordingExec,
			readDirFunc:  defaultReadDir,
			listenFunc:   mockFailingListenTCP,
			removeFunc:   defaultRemoveFile,
			useCompanion: false,
			expectError:  true,
		},
		{
			name:         "environment preparation error propagates failure",
			execFunc:     mockRecordingExec,
			readDirFunc:  mockFailingReadDir,
			listenFunc:   mockEphemeralListenTCP,
			removeFunc:   defaultRemoveFile,
			useCompanion: false,
			expectError:  true,
		},
		{
			name:         "process execution failure propagates error",
			execFunc:     mockFailingExec,
			readDirFunc:  defaultReadDir,
			listenFunc:   mockEphemeralListenTCP,
			removeFunc:   defaultRemoveFile,
			useCompanion: false,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			execProcess = tt.execFunc
			readDir = tt.readDirFunc
			listenTCP = tt.listenFunc
			removeFile = tt.removeFunc

			runArgs := []string{"--dry-run"}
			if tt.useCompanion {
				runArgs = []string{defaultCompanionBinaryPath}
			}

			err := Run(context.Background(), runArgs, tempDir)
			if tt.expectError && err == nil {
				t.Fatalf("Run() expected error, got nil")
			}
			if !tt.expectError && err != nil {
				t.Fatalf("Run() unexpected error: %v", err)
			}
			if !tt.expectError {
				if !reflect.DeepEqual(capturedArgs, runArgs) {
					t.Errorf("Run() args = %v, want %v", capturedArgs, runArgs)
				}
				if len(capturedEnv) == 0 {
					t.Errorf("Run() expected non-empty environment")
				}
			}
		})
	}
}

func TestMain_Success(t *testing.T) {
	origExecProcess := execProcess
	origReadDir := readDir
	origReadFile := readFile
	origListenTCP := listenTCP
	origExitFunc := exitFunc
	origArgs := os.Args
	defer func() {
		execProcess = origExecProcess
		readDir = origReadDir
		readFile = origReadFile
		listenTCP = origListenTCP
		exitFunc = origExitFunc
		os.Args = origArgs
	}()

	execProcess = mockSuccessfulExec
	readDir = mockNotExistReadDir
	listenTCP = mockEphemeralListenTCP
	recordedExitCode = -1
	exitFunc = mockExitRecorder
	os.Args = []string{"init", "--version"}

	main()

	if recordedExitCode != -1 {
		t.Errorf("main() unexpected exit code: %d", recordedExitCode)
	}
}

func TestMain_Failure(t *testing.T) {
	origExecProcess := execProcess
	origReadDir := readDir
	origReadFile := readFile
	origListenTCP := listenTCP
	origExitFunc := exitFunc
	defer func() {
		execProcess = origExecProcess
		readDir = origReadDir
		readFile = origReadFile
		listenTCP = origListenTCP
		exitFunc = origExitFunc
	}()

	readDir = mockFailingReadDir
	listenTCP = mockEphemeralListenTCP
	recordedExitCode = -1
	exitFunc = mockExitRecorder

	main()

	if recordedExitCode != 1 {
		t.Errorf("main() expected exit code 1 on failure, got %d", recordedExitCode)
	}
}

func TestMain_Termination(t *testing.T) {
	origExecProcess := execProcess
	origReadDir := readDir
	origReadFile := readFile
	origListenTCP := listenTCP
	origExitFunc := exitFunc
	defer func() {
		execProcess = origExecProcess
		readDir = origReadDir
		readFile = origReadFile
		listenTCP = origListenTCP
		exitFunc = origExitFunc
	}()

	execProcess = mockTerminatedExec
	readDir = mockNotExistReadDir
	listenTCP = mockEphemeralListenTCP
	recordedExitCode = -1
	exitFunc = mockExitRecorder

	main()

	if recordedExitCode != 0 {
		t.Errorf("main() on termination expected exit code 0, got %d", recordedExitCode)
	}
}
