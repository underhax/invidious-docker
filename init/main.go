// Package main implements a minimal static initialization binary that discovers
// Docker secret files, constructs database and proxy connection strings, bridges
// loopback companion TCP requests to a UNIX domain socket, and scrubs child
// environment memory after service readiness.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultInvidiousBinaryPath        = "/invidious/invidious"
	defaultCompanionBinaryPath        = "/app/invidious_companion"
	defaultBinaryPath                 = defaultInvidiousBinaryPath
	defaultCommand                    = ""
	defaultSecretsDir                 = "/run/secrets"
	defaultProxyListenAddr            = "127.0.0.1:8282"
	defaultCompanionSocketPath        = "/tmp/invidious-companion.sock"
	defaultBridgedCompanionSocketPath = "/var/run/companion/invidious-companion.sock"
	defaultWaitDelay                  = 5 * time.Second
	secretPrefix                      = "inv_"
)

const (
	fileDBName     = "db_name"
	fileDBUser     = "db_user"
	fileDBPassword = "db_password"
	fileProxyHost  = "proxy_host"
	fileProxyPort  = "proxy_port"
	fileProxyUser  = "proxy_user"
	fileProxyPass  = "proxy_pass"
)

const (
	envDatabaseURL          = "INVIDIOUS_DATABASE_URL"
	envInvidiousConfig      = "INVIDIOUS_CONFIG"
	envInvidiousCompanion   = "INVIDIOUS_INVIDIOUS_COMPANION"
	envInvidiousAdmins      = "INVIDIOUS_ADMINS"
	envPreferences          = "INVIDIOUS_DEFAULT_USER_PREFERENCES"
	envVideoJS              = "INVIDIOUS_VIDEOJS"
	envJobs                 = "INVIDIOUS_JOBS"
	envHostBinding          = "INVIDIOUS_HOST_BINDING"
	envInvidiousPort        = "INVIDIOUS_PORT"
	envCheckTables          = "INVIDIOUS_CHECK_TABLES"
	envCaptchaEnabled       = "INVIDIOUS_CAPTCHA_ENABLED"
	envServerUseUnixSocket  = "SERVER_USE_UNIX_SOCKET"
	envServerUnixSocketPath = "SERVER_UNIX_SOCKET_PATH"
	envServerBasePath       = "SERVER_BASE_PATH"
	envCacheEnabled         = "CACHE_ENABLED"
	envCacheDirectory       = "CACHE_DIRECTORY"
	envProxy                = "PROXY"
	envSSLCertFile          = "SSL_CERT_FILE"
	envSSLCertDir           = "SSL_CERT_DIR"
	envHome                 = "HOME"

	prefixPreferences = "INVIDIOUS_DEFAULT_USER_PREFERENCES_"
	prefixVideoJS     = "INVIDIOUS_VIDEOJS_"
	prefixJobs        = "INVIDIOUS_JOBS_"
	suffixJobEnable   = "_ENABLE"
)

const (
	defaultPostgresHost       = "/var/run/postgresql"
	defaultInvidiousConfig    = "{}"
	defaultInvidiousCompanion = `[{"private_url":"http://127.0.0.1:8282/companion","builtin_proxy":true}]`
	defaultBindAddress        = "0.0.0.0"
	defaultLoopbackAddress    = "127.0.0.1"
	defaultServerPort         = "3000"
	defaultServerBasePath     = "/companion"
	defaultCacheDirectory     = "/var/tmp"
	defaultSSLCertFile        = "/etc/ssl/certs/ca-certificates.crt"
	defaultSSLCertDir         = "/etc/ssl/certs"
	defaultHomeDir            = "/tmp"
	defaultBoolTrue           = "true"
	defaultBoolFalse          = "false"
	defaultProcDir            = "/proc"
	defaultReadinessTimeout   = 30 * time.Second
	childReadinessInterval    = 50 * time.Millisecond
	maxEnvRangeSize           = 32 * 1024 * 1024
	minStatFieldsAfterComm    = 49
	statEnvStartIndex         = 47
	statEnvEndIndex           = 48
)

var (
	errInvalidSecretKey          = errors.New("invalid characters in secret environment key")
	errIncompleteDatabaseSecrets = errors.New("database secrets db_name, db_user, and db_password must be provided together")
	errIncompleteProxySecrets    = errors.New("incomplete proxy secrets configuration")
)

func defaultReadFile(path string) ([]byte, error) {
	cleanPath := filepath.Clean(path)
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("reading secret file %q: %w", cleanPath, err)
	}
	return data, nil
}

func defaultReadDir(dirname string) ([]fs.DirEntry, error) {
	cleanDir := filepath.Clean(dirname)
	entries, err := os.ReadDir(cleanDir)
	if err != nil {
		return nil, fmt.Errorf("reading secrets directory %q: %w", cleanDir, err)
	}
	return entries, nil
}

func defaultCloseFile(f *os.File) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing file: %w", err)
	}
	return nil
}

func defaultWriteMemory(f *os.File, b []byte, off int64) (int, error) {
	n, err := f.WriteAt(b, off)
	if err != nil {
		return n, fmt.Errorf("writing memory: %w", err)
	}
	return n, nil
}

func defaultOpenFile(name string, flag int, perm os.FileMode) (*os.File, error) {
	cleanPath := filepath.Clean(name)
	file, err := os.OpenFile(cleanPath, flag, perm)
	if err != nil {
		return nil, fmt.Errorf("opening file %q: %w", cleanPath, err)
	}
	return file, nil
}

type socketDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

func defaultCreateDialer() socketDialer {
	return &net.Dialer{}
}

func defaultDialTCP(ctx context.Context, network, address string) (net.Conn, error) {
	d := createDialer()
	conn, err := d.DialContext(ctx, network, address)
	if err != nil {
		return nil, fmt.Errorf("dialing %s %s: %w", network, address, err)
	}
	return conn, nil
}

func defaultProcPath(pid int, entry string) string {
	return filepath.Join(defaultProcDir, strconv.Itoa(pid), entry)
}

func cancelCommand(cmd *exec.Cmd) func() error {
	return func() error {
		if cmd.Process != nil {
			return cmd.Process.Signal(syscall.SIGTERM)
		}
		return nil
	}
}

func resolveReadinessTarget(args []string) (network, addr string) {
	if slices.Contains(args, defaultCompanionBinaryPath) {
		return "unix", defaultCompanionSocketPath
	}
	return "tcp", net.JoinHostPort(defaultLoopbackAddress, defaultServerPort)
}

func defaultCreateCommand(ctx context.Context, args []string) *exec.Cmd {
	var cmd *exec.Cmd
	cmdArgs := args
	if len(args) > 0 && args[0] == defaultCompanionBinaryPath {
		cmd = exec.CommandContext(ctx, defaultCompanionBinaryPath)
		cmdArgs = args[1:]
	} else {
		cmd = exec.CommandContext(ctx, defaultBinaryPath)
	}
	if len(cmdArgs) > 0 {
		cmd.Args = append([]string{cmd.Path}, cmdArgs...)
	}
	cmd.Cancel = cancelCommand(cmd)
	cmd.WaitDelay = defaultWaitDelay
	return cmd
}

func parseProcStat(statContent string) (envStart, envEnd int64, err error) {
	_, remainder, found := strings.CutLast(statContent, ")")
	if !found {
		return 0, 0, errors.New("missing closing parenthesis for comm in stat content")
	}

	fields := strings.Fields(remainder)
	if len(fields) < minStatFieldsAfterComm {
		return 0, 0, fmt.Errorf("stat fields count %d below required minimum %d", len(fields), minStatFieldsAfterComm)
	}

	envStart, err = strconv.ParseInt(fields[statEnvStartIndex], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parsing env_start from stat: %w", err)
	}

	envEnd, err = strconv.ParseInt(fields[statEnvEndIndex], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parsing env_end from stat: %w", err)
	}

	if envStart <= 0 || envEnd <= envStart {
		return 0, 0, fmt.Errorf("invalid environment boundaries: env_start (%d) >= env_end (%d)", envStart, envEnd)
	}

	if envEnd-envStart > maxEnvRangeSize {
		return 0, 0, fmt.Errorf("environment range size %d exceeds limit of %d bytes", envEnd-envStart, maxEnvRangeSize)
	}

	return envStart, envEnd, nil
}

func waitForChildReadiness(ctx context.Context, network, addr string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("readiness context canceled: %w", err)
		}
		conn, err := dialTCP(waitCtx, network, addr)
		if err == nil {
			if closeErr := conn.Close(); closeErr != nil {
				return fmt.Errorf("closing readiness socket: %w", closeErr)
			}
			return nil
		}
		timer := time.NewTimer(childReadinessInterval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				return nil
			}
			return fmt.Errorf("readiness context canceled: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func isIgnorableMemoryError(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission)
}

func defaultScrubChildMemory(ctx context.Context, pid int) (err error) {
	targetNetwork, targetAddr := resolveReadinessTarget(os.Args)
	if readinessErr := waitForChildReadiness(ctx, targetNetwork, targetAddr, defaultReadinessTimeout); readinessErr != nil {
		if isIgnorableMemoryError(readinessErr) || errors.Is(readinessErr, context.Canceled) {
			return nil
		}
		return fmt.Errorf("synchronizing child readiness: %w", readinessErr)
	}

	statPath := procPath(pid, "stat")
	statBytes, readErr := readFile(statPath)
	if readErr != nil {
		if isIgnorableMemoryError(readErr) {
			return nil
		}
		return fmt.Errorf("reading stat file %q: %w", statPath, readErr)
	}

	envStart, envEnd, parseErr := parseProcStat(string(statBytes))
	if parseErr != nil {
		return fmt.Errorf("parsing stat file %q: %w", statPath, parseErr)
	}

	memPath := procPath(pid, "mem")
	memFile, openErr := openFile(memPath, os.O_RDWR, 0)
	if openErr != nil {
		if isIgnorableMemoryError(openErr) {
			return nil
		}
		return fmt.Errorf("opening mem file %q: %w", memPath, openErr)
	}
	defer func() {
		if closeErr := closeFile(memFile); closeErr != nil && err == nil && !isIgnorableMemoryError(closeErr) {
			err = fmt.Errorf("closing mem file %q: %w", memPath, closeErr)
		}
	}()

	zeros := make([]byte, envEnd-envStart)
	if _, writeErr := writeMemory(memFile, zeros, envStart); writeErr != nil {
		if isIgnorableMemoryError(writeErr) {
			return nil
		}
		return fmt.Errorf("zeroing memory in %q: %w", memPath, writeErr)
	}

	return nil
}

func defaultExec(ctx context.Context, args, env []string) error {
	cmd := createCommand(ctx, args)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting target binary %q: %w", cmd.Path, err)
	}

	pid := cmd.Process.Pid
	scrubCtx, cancelScrub := context.WithCancel(ctx)
	defer cancelScrub()

	scrubDone := make(chan struct{})
	go func() {
		defer close(scrubDone)
		if scrubErr := scrubChildMemory(scrubCtx, pid); scrubErr != nil {
			fmt.Fprintf(os.Stderr, "memory scrubbing failed: %v\n", scrubErr)
		}
	}()

	cmd.Env = nil
	clear(env)
	runtime.GC()

	waitErr := cmd.Wait()
	cancelScrub()
	<-scrubDone

	if waitErr != nil {
		return fmt.Errorf("running target binary: %w", waitErr)
	}
	return nil
}

func defaultListenTCP(ctx context.Context, addr string) (net.Listener, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listening on %q: %w", addr, err)
	}
	return ln, nil
}

func defaultDialUnix(ctx context.Context, socketPath string) (net.Conn, error) {
	conn, err := createDialer().DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dialing unix socket %q: %w", socketPath, err)
	}
	return conn, nil
}

func defaultRemoveFile(path string) error {
	cleanPath := filepath.Clean(path)
	if err := os.Remove(cleanPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing file %q: %w", cleanPath, err)
	}
	return nil
}

var (
	closeFile        = defaultCloseFile
	createCommand    = defaultCreateCommand
	createDialer     = defaultCreateDialer
	dialTCP          = defaultDialTCP
	dialUnix         = defaultDialUnix
	execProcess      = defaultExec
	exitFunc         = os.Exit
	listenTCP        = defaultListenTCP
	openFile         = defaultOpenFile
	procPath         = defaultProcPath
	readDir          = defaultReadDir
	readFile         = defaultReadFile
	removeFile       = defaultRemoveFile
	scrubChildMemory = defaultScrubChildMemory
	writeMemory      = defaultWriteMemory
)

func closeCloser(c io.Closer) {
	if err := c.Close(); err != nil {
		return
	}
}

func pipeConn(dst io.Writer, src io.Reader, done chan<- struct{}) {
	if _, err := io.Copy(dst, src); err != nil {
		done <- struct{}{}
		return
	}
	done <- struct{}{}
}

func handleProxyConnection(ctx context.Context, clientConn net.Conn, socketPath string) {
	backendConn, err := dialUnix(ctx, socketPath)
	if err != nil {
		closeCloser(clientConn)
		return
	}

	done := make(chan struct{}, 2)
	go pipeConn(backendConn, clientConn, done)
	go pipeConn(clientConn, backendConn, done)

	select {
	case <-ctx.Done():
		closeCloser(clientConn)
		closeCloser(backendConn)
		<-done
		<-done
	case <-done:
		closeCloser(clientConn)
		closeCloser(backendConn)
		<-done
	}
}

func acceptProxyConnections(ctx context.Context, ln net.Listener, socketPath string) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go handleProxyConnection(ctx, conn, socketPath)
	}
}

// StartSocketProxy launches a loopback TCP listener that relays connections to a
// UNIX domain socket so services lacking native UNIX socket support can communicate
// over isolated tmpfs sockets.
func StartSocketProxy(ctx context.Context, listenAddr, socketPath string) (io.Closer, error) {
	ln, err := listenTCP(ctx, listenAddr)
	if err != nil {
		return nil, fmt.Errorf("starting socket proxy listener: %w", err)
	}

	go acceptProxyConnections(ctx, ln, socketPath)

	return ln, nil
}

func readOptionalSecret(path string) (secret string, exists bool, err error) {
	data, err := readFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("reading secret %q: %w", path, err)
	}

	trimmed := strings.TrimSpace(string(data))
	clear(data)
	return trimmed, true, nil
}

func setEnvVar(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func listSecretNames(secretsDir string) ([]string, error) {
	entries, err := readDir(secretsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning secrets directory: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}

	slices.Sort(names)
	return names, nil
}

func hasInvidiousSecretPrefix(name string) bool {
	return len(name) > len(secretPrefix) && strings.EqualFold(name[:len(secretPrefix)], secretPrefix)
}

func isInvalidEnvKeyRune(r rune) bool {
	return (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_'
}

func secretEnvKey(name string) (string, error) {
	remainder := name[len(secretPrefix):]
	key := strings.ToUpper(remainder)
	if key == "" || strings.ContainsFunc(key, isInvalidEnvKeyRune) {
		return "", fmt.Errorf("key %q: %w", name, errInvalidSecretKey)
	}
	return key, nil
}

func isAdminSeparatorRune(r rune) bool {
	return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

func formatAdminsSecret(val string) string {
	if strings.HasPrefix(val, "[") {
		return val
	}
	parts := strings.FieldsFunc(val, isAdminSeparatorRune)
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, strconv.Quote(part))
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

func configureDynamicSecrets(env []string, secretsDir string, names []string) ([]string, error) {
	for _, name := range names {
		if !hasInvidiousSecretPrefix(name) {
			continue
		}

		key, err := secretEnvKey(name)
		if err != nil {
			return nil, err
		}

		val, exists, err := readOptionalSecret(filepath.Join(secretsDir, name))
		if err != nil {
			return nil, fmt.Errorf("loading secret %q: %w", name, err)
		}
		if exists && val != "" {
			if key == envInvidiousAdmins {
				val = formatAdminsSecret(val)
			}
			env = setEnvVar(env, key, val)
		}
	}
	return env, nil
}

func configureDatabaseSecrets(env []string, secretsDir string) ([]string, error) {
	dbName, nameExists, err := readOptionalSecret(filepath.Join(secretsDir, fileDBName))
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", fileDBName, err)
	}

	dbUser, userExists, err := readOptionalSecret(filepath.Join(secretsDir, fileDBUser))
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", fileDBUser, err)
	}

	dbPassword, passExists, err := readOptionalSecret(filepath.Join(secretsDir, fileDBPassword))
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", fileDBPassword, err)
	}

	if !nameExists && !userExists && !passExists {
		return env, nil
	}

	if dbName == "" || dbUser == "" || dbPassword == "" {
		return nil, errIncompleteDatabaseSecrets
	}

	userInfo := url.UserPassword(dbUser, dbPassword)
	databaseURL := fmt.Sprintf("postgres://%s@/%s?host=%s", userInfo.String(), url.PathEscape(dbName), defaultPostgresHost)
	return setEnvVar(env, envDatabaseURL, databaseURL), nil
}

type proxySecretConfig struct {
	host      string
	port      string
	user      string
	pass      string
	anyExists bool
}

func readProxySecrets(secretsDir string) (proxySecretConfig, error) {
	host, hostExists, err := readOptionalSecret(filepath.Join(secretsDir, fileProxyHost))
	if err != nil {
		return proxySecretConfig{}, fmt.Errorf("loading %s: %w", fileProxyHost, err)
	}

	port, portExists, err := readOptionalSecret(filepath.Join(secretsDir, fileProxyPort))
	if err != nil {
		return proxySecretConfig{}, fmt.Errorf("loading %s: %w", fileProxyPort, err)
	}

	user, userExists, err := readOptionalSecret(filepath.Join(secretsDir, fileProxyUser))
	if err != nil {
		return proxySecretConfig{}, fmt.Errorf("loading %s: %w", fileProxyUser, err)
	}

	pass, passExists, err := readOptionalSecret(filepath.Join(secretsDir, fileProxyPass))
	if err != nil {
		return proxySecretConfig{}, fmt.Errorf("loading %s: %w", fileProxyPass, err)
	}

	return proxySecretConfig{
		host:      host,
		port:      port,
		user:      user,
		pass:      pass,
		anyExists: hostExists || portExists || userExists || passExists,
	}, nil
}

func configureProxySecrets(env []string, secretsDir string) ([]string, error) {
	cfg, err := readProxySecrets(secretsDir)
	if err != nil {
		return nil, err
	}
	if !cfg.anyExists {
		return env, nil
	}
	if cfg.host == "" || cfg.port == "" || (cfg.user == "") != (cfg.pass == "") {
		return nil, errIncompleteProxySecrets
	}

	proxyURL := &url.URL{
		Scheme: "socks5",
		Host:   net.JoinHostPort(cfg.host, cfg.port),
	}
	if cfg.user != "" {
		proxyURL.User = url.UserPassword(cfg.user, cfg.pass)
	}

	return setEnvVar(env, envProxy, proxyURL.String()), nil
}

func formatFixedStringArray(rawVal string, minLen int) string {
	if strings.HasPrefix(rawVal, "[") {
		return rawVal
	}
	var items []string
	if rawVal != "" {
		for part := range strings.SplitSeq(rawVal, ",") {
			items = append(items, strings.TrimSpace(part))
		}
	}
	for len(items) < minLen {
		items = append(items, "")
	}
	quoted := make([]string, 0, len(items))
	for _, item := range items {
		quoted = append(quoted, strconv.Quote(item))
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

func formatScalarJSONValue(rawVal string) string {
	if rawVal == defaultBoolTrue || rawVal == defaultBoolFalse {
		return rawVal
	}
	if _, err := strconv.ParseInt(rawVal, 10, 64); err == nil {
		return rawVal
	}
	if _, err := strconv.ParseFloat(rawVal, 64); err == nil {
		return rawVal
	}
	return strconv.Quote(rawVal)
}

func formatPreferenceJSONValue(fieldKey, rawVal string) string {
	switch fieldKey {
	case "captions":
		return formatFixedStringArray(rawVal, 3)
	case "comments":
		return formatFixedStringArray(rawVal, 2)
	case "feed_menu":
		return formatFixedStringArray(rawVal, 0)
	default:
		return formatScalarJSONValue(rawVal)
	}
}

func consumeNestedEntry(key, val string, prefs, videojs, jobs *[]string) bool {
	if after, ok := strings.CutPrefix(key, prefixPreferences); ok && after != "" {
		field := strings.ToLower(after)
		*prefs = append(*prefs, strconv.Quote(field)+":"+formatPreferenceJSONValue(field, val))
		return true
	}
	if after, ok := strings.CutPrefix(key, prefixVideoJS); ok && after != "" {
		field := strings.ToLower(after)
		*videojs = append(*videojs, strconv.Quote(field)+":"+formatScalarJSONValue(val))
		return true
	}
	if after, ok := strings.CutPrefix(key, prefixJobs); ok {
		if jobName, hasSuffix := strings.CutSuffix(after, suffixJobEnable); hasSuffix && jobName != "" {
			field := strings.ToLower(jobName)
			*jobs = append(*jobs, strconv.Quote(field)+`:{"enable":`+formatScalarJSONValue(val)+"}")
			return true
		}
	}
	return false
}

func configureNestedEnvObjects(env []string) []string {
	filtered := make([]string, 0, len(env))
	var prefs, videojs, jobs []string

	for _, kv := range env {
		key, val, found := strings.Cut(kv, "=")
		if found && consumeNestedEntry(key, val, &prefs, &videojs, &jobs) {
			continue
		}
		filtered = append(filtered, kv)
	}

	if len(prefs) > 0 {
		filtered = setEnvVar(filtered, envPreferences, "{"+strings.Join(prefs, ",")+"}")
	}
	if len(videojs) > 0 {
		filtered = setEnvVar(filtered, envVideoJS, "{"+strings.Join(videojs, ",")+"}")
	}
	if len(jobs) > 0 {
		filtered = setEnvVar(filtered, envJobs, "{"+strings.Join(jobs, ",")+"}")
	}
	return filtered
}

func configureEnforcedSettings(env []string) []string {
	env = configureNestedEnvObjects(env)
	env = setEnvVar(env, envInvidiousConfig, defaultInvidiousConfig)
	env = setEnvVar(env, envInvidiousCompanion, defaultInvidiousCompanion)
	env = setEnvVar(env, envHostBinding, defaultBindAddress)
	env = setEnvVar(env, envInvidiousPort, defaultServerPort)
	env = setEnvVar(env, envCheckTables, defaultBoolTrue)
	env = setEnvVar(env, envCaptchaEnabled, defaultBoolFalse)
	env = setEnvVar(env, envServerUseUnixSocket, defaultBoolTrue)
	env = setEnvVar(env, envServerUnixSocketPath, defaultCompanionSocketPath)
	env = setEnvVar(env, envServerBasePath, defaultServerBasePath)
	env = setEnvVar(env, envCacheEnabled, defaultBoolTrue)
	env = setEnvVar(env, envCacheDirectory, defaultCacheDirectory)
	env = setEnvVar(env, envSSLCertFile, defaultSSLCertFile)
	env = setEnvVar(env, envSSLCertDir, defaultSSLCertDir)
	env = setEnvVar(env, envHome, defaultHomeDir)
	return env
}

// PrepareEnvironment scans the specified secrets directory for prefixed and
// structured secrets, merging their values into the process environment and
// enforcing immutable container constraints required for scratch execution.
func PrepareEnvironment(baseEnv []string, secretsDir string) ([]string, error) {
	env := slices.Clone(baseEnv)

	names, err := listSecretNames(secretsDir)
	if err != nil {
		return nil, err
	}

	if env, err = configureDynamicSecrets(env, secretsDir, names); err != nil {
		return nil, err
	}
	if env, err = configureDatabaseSecrets(env, secretsDir); err != nil {
		return nil, err
	}
	if env, err = configureProxySecrets(env, secretsDir); err != nil {
		return nil, err
	}

	env = configureEnforcedSettings(env)
	return env, nil
}

// BuildCommandArguments constructs the execution argument vector, defaulting
// to the service daemon command when no explicit subcommands are provided.
func BuildCommandArguments(defaultCmd string, args []string) []string {
	if len(args) == 0 {
		if defaultCmd == "" {
			return []string{}
		}
		return []string{defaultCmd}
	}

	if args[0] == defaultBinaryPath {
		if len(args) == 1 {
			if defaultCmd == "" {
				return []string{}
			}
			return []string{defaultCmd}
		}
		res := make([]string, len(args)-1)
		copy(res, args[1:])
		return res
	}

	res := make([]string, len(args))
	copy(res, args)
	return res
}

// IsTermination determines whether a command termination was initiated by
// intentional orchestration shutdown signals to prevent benign shutdowns
// from being misclassified as container runtime failures.
func IsTermination(ctx context.Context, err error) bool {
	if errors.Is(ctx.Err(), context.Canceled) {
		return true
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		if status, statusOk := exitErr.Sys().(syscall.WaitStatus); statusOk && status.Signaled() {
			sig := status.Signal()
			return sig == syscall.SIGTERM || sig == syscall.SIGINT || sig == syscall.SIGKILL
		}
	}
	return false
}

// ExitCode extracts the process exit code from an execution error to ensure
// container health checks and parent process status monitors receive accurate termination signals.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		if status, statusOk := exitErr.Sys().(syscall.WaitStatus); statusOk && status.Signaled() {
			sig := status.Signal()
			if sig == syscall.SIGTERM || sig == syscall.SIGINT {
				return 0
			}
			return 128 + int(sig)
		}
		return exitErr.ExitCode()
	}
	return 1
}

// Run executes the initialization workflow by discovering secrets, preparing
// the runtime environment, bridging UNIX sockets, and launching the application binary.
func Run(ctx context.Context, args []string, secretsDir string) error {
	env, err := PrepareEnvironment(os.Environ(), secretsDir)
	if err != nil {
		return fmt.Errorf("preparing runtime environment: %w", err)
	}

	if slices.Contains(args, defaultCompanionBinaryPath) {
		if removeErr := removeFile(defaultCompanionSocketPath); removeErr != nil {
			return fmt.Errorf("cleaning companion socket: %w", removeErr)
		}
	} else {
		proxyCloser, proxyErr := StartSocketProxy(ctx, defaultProxyListenAddr, defaultBridgedCompanionSocketPath)
		if proxyErr != nil {
			return fmt.Errorf("initializing companion socket proxy: %w", proxyErr)
		}
		defer closeCloser(proxyCloser)
	}

	cmdArgs := BuildCommandArguments(defaultCommand, args)
	if execErr := execProcess(ctx, cmdArgs, env); execErr != nil {
		return fmt.Errorf("transferring execution to binary: %w", execErr)
	}

	return nil
}

func main() {
	var args []string
	if len(os.Args) > 1 {
		args = os.Args[1:]
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := Run(ctx, args, defaultSecretsDir); err != nil {
		if !IsTermination(ctx, err) {
			fmt.Fprintf(os.Stderr, "init error: %v\n", err)
		}
		if IsTermination(ctx, err) {
			exitFunc(0)
			return
		}
		exitFunc(ExitCode(err))
	}
}
