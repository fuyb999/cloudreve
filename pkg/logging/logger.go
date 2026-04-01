package logging

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"
)

const (
	logTimeFormat       = "2006-01-02 15:04:05.000"
	logFileSuffix       = "/pkg/logging/logger.go"
	requestDirectionIn  = "IN"
	requestDirectionOut = "OUT"
)

type CallerMode string
type StacktraceMode string

const (
	CallerModeAuto      CallerMode     = "auto"
	CallerModeOn        CallerMode     = "on"
	CallerModeOff       CallerMode     = "off"
	StacktraceModeOff   StacktraceMode = "off"
	StacktraceModePanic StacktraceMode = "panic"
	StacktraceModeError StacktraceMode = "error"
	StacktraceModeAll   StacktraceMode = "all"
)

var callerPathAnchors = []string{
	"/cloudreve/",
	"/Cloudreve/",
	"/application/",
	"/inventory/",
	"/middleware/",
	"/pkg/",
	"/routers/",
	"/service/",
	"/ent/",
	"/cmd/",
}

var (
	defaultLoggerMu sync.RWMutex
	defaultLogger   Logger
)

// Logger interface for logging messages.
type Logger interface {
	Panic(format string, v ...any)
	Error(format string, v ...any)
	Warning(format string, v ...any)
	Info(format string, v ...any)
	Debug(format string, v ...any)
	// Copy a new logger with a prefix.
	CopyWithPrefix(prefix string) Logger

	// SupportColor returns if current logger support outputting colors.
	SupportColor() bool
}

// LoggerCtx defines keys for logger with correlation ID
type LoggerCtx struct{}

// CorrelationIDCtx defines keys for correlation ID
type CorrelationIDCtx struct{}
type LogLevel string

const (
	// LevelError 错误
	LevelError LogLevel = "error"
	// LevelWarning 警告
	LevelWarning LogLevel = "warning"
	// LevelInformational 提示
	LevelInformational LogLevel = "info"
	// LevelDebug 除错
	LevelDebug LogLevel = "debug"
)

// NewConsoleLogger initializes a new logging that prints logs to Stdout.
func NewConsoleLogger(level LogLevel, opts ...ConsoleLoggerOption) Logger {
	return newConsoleLogger(level, color.Output, time.Now, opts...)
}

type consoleLoggerConfig struct {
	forceColor     bool
	callerMode     CallerMode
	stacktraceMode StacktraceMode
}

type ConsoleLoggerOption func(*consoleLoggerConfig)

func WithForceColor(force bool) ConsoleLoggerOption {
	return func(cfg *consoleLoggerConfig) {
		cfg.forceColor = force
	}
}

func WithCallerMode(mode CallerMode) ConsoleLoggerOption {
	return func(cfg *consoleLoggerConfig) {
		if mode == "" {
			cfg.callerMode = CallerModeAuto
			return
		}
		cfg.callerMode = mode
	}
}

func WithStacktraceMode(mode StacktraceMode) ConsoleLoggerOption {
	return func(cfg *consoleLoggerConfig) {
		if mode == "" {
			cfg.stacktraceMode = StacktraceModePanic
			return
		}
		cfg.stacktraceMode = mode
	}
}

func newConsoleLogger(level LogLevel, out io.Writer, now func() time.Time, opts ...ConsoleLoggerOption) *consoleLogger {
	cfg := consoleLoggerConfig{
		callerMode:     CallerModeAuto,
		stacktraceMode: StacktraceModePanic,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	logFunc := func(level string) loggingFunc {
		return func(logger *consoleLogger, s string, a ...any) {
			logger.println(level, fmt.Sprintf(s, a...))
		}
	}

	logger := &consoleLogger{
		warning:    logFunc("WARN"),
		panic:      nil,
		error:      logFunc("ERROR"),
		info:       logFunc("INFO"),
		debug:      logFunc("DEBUG"),
		out:        out,
		mu:         &sync.Mutex{},
		now:        now,
		showCaller: shouldShowCaller(level, cfg.callerMode),
		forceColor: cfg.forceColor,
		stacktrace: cfg.stacktraceMode,
	}
	logger.panic = func(logger *consoleLogger, s string, a ...any) {
		msg := fmt.Sprintf(s, a...)
		logger.println("PANIC", msg)
		panic(msg)
	}

	switch level {
	case LevelError:
		logger.warning = noopLoggingFunc
		logger.info = noopLoggingFunc
		logger.debug = noopLoggingFunc
	case LevelWarning:
		logger.info = noopLoggingFunc
		logger.debug = noopLoggingFunc
	case LevelInformational:
		logger.debug = noopLoggingFunc
	case LevelDebug:
	}

	return logger
}

// FromContext retrieves a logger from context.
func FromContext(ctx context.Context) Logger {
	v, ok := ctx.Value(LoggerCtx{}).(Logger)
	if !ok {
		v = DefaultLogger()
	}
	return v
}

// SetDefaultLogger sets the process-wide fallback logger used when context has no logger attached.
func SetDefaultLogger(l Logger) {
	defaultLoggerMu.Lock()
	defer defaultLoggerMu.Unlock()
	defaultLogger = l
}

// DefaultLogger returns the process-wide fallback logger.
func DefaultLogger() Logger {
	defaultLoggerMu.RLock()
	defer defaultLoggerMu.RUnlock()
	if defaultLogger != nil {
		return defaultLogger
	}

	return NewConsoleLogger(LevelDebug)
}

// CorrelationID retrieves a correlation ID from context.
func CorrelationID(ctx context.Context) uuid.UUID {
	v, ok := ctx.Value(CorrelationIDCtx{}).(uuid.UUID)
	if !ok {
		v = uuid.Nil
	}
	return v
}

type consoleLogger struct {
	warning    loggingFunc
	panic      loggingFunc
	error      loggingFunc
	info       loggingFunc
	debug      loggingFunc
	prefix     string
	out        io.Writer
	mu         *sync.Mutex
	now        func() time.Time
	showCaller bool
	forceColor bool
	stacktrace StacktraceMode
}

func (ll *consoleLogger) Panic(format string, v ...any) {
	ll.panic(ll, format, v...)
}

func (ll *consoleLogger) Error(format string, v ...any) {
	ll.error(ll, format, v...)
}

func (ll *consoleLogger) Warning(format string, v ...any) {
	ll.warning(ll, format, v...)
}

func (ll *consoleLogger) Info(format string, v ...any) {
	ll.info(ll, format, v...)
}

func (ll *consoleLogger) Debug(format string, v ...any) {
	ll.debug(ll, format, v...)
}

// Recover logs a recovered panic without re-panicking.
func Recover(l Logger, format string, v ...any) {
	if ll, ok := l.(*consoleLogger); ok {
		ll.println("PANIC", fmt.Sprintf(format, v...))
		return
	}

	if l != nil {
		l.Error("%s", fmt.Sprintf(format, v...))
	}
}

// println 打印
func (ll *consoleLogger) println(level string, msg string) {
	var line strings.Builder
	line.Grow(len(msg) + len(ll.prefix) + 96)

	line.WriteString(ll.now().Format(logTimeFormat))
	line.WriteByte(' ')
	line.WriteString(formatLevel(level, ll.SupportColor(), ll.forceColor))

	if caller := ll.resolveCaller(); caller != "" {
		line.WriteString(" [")
		line.WriteString(caller)
		line.WriteByte(']')
	}

	if ll.prefix != "" {
		line.WriteByte(' ')
		line.WriteString(ll.prefix)
	}

	line.WriteByte(' ')
	line.WriteString(msg)

	if stack := ll.resolveStack(level); stack != "" {
		line.WriteByte('\n')
		line.WriteString(stack)
	}
	line.WriteByte('\n')

	ll.mu.Lock()
	defer ll.mu.Unlock()

	_, _ = io.WriteString(ll.out, line.String())
}

func (ll *consoleLogger) resolveCaller() string {
	if !ll.showCaller {
		return ""
	}

	var pcs [8]uintptr
	frameCount := runtime.Callers(3, pcs[:])
	frames := runtime.CallersFrames(pcs[:frameCount])
	for {
		frame, more := frames.Next()
		path := filepath.ToSlash(frame.File)
		if path != "" && !strings.HasSuffix(path, logFileSuffix) {
			return fmt.Sprintf("%s:%d", trimCallerPath(path), frame.Line)
		}
		if !more {
			break
		}
	}

	return ""
}

func (ll *consoleLogger) resolveStack(level string) string {
	if !ll.shouldIncludeStack(level) {
		return ""
	}

	var pcs [32]uintptr
	frameCount := runtime.Callers(3, pcs[:])
	frames := runtime.CallersFrames(pcs[:frameCount])

	var stack strings.Builder
	stack.WriteString("stacktrace:")
	stackFound := false
	for {
		frame, more := frames.Next()
		path := filepath.ToSlash(frame.File)
		if path != "" && !strings.HasSuffix(path, logFileSuffix) {
			stackFound = true
			stack.WriteString("\n  ")
			stack.WriteString(trimCallerPath(path))
			stack.WriteByte(':')
			stack.WriteString(strconv.Itoa(frame.Line))
			stack.WriteByte(' ')
			stack.WriteString(trimFunctionName(frame.Function))
		}
		if !more {
			break
		}
	}

	if !stackFound {
		return ""
	}

	return stack.String()
}

func trimCallerPath(path string) string {
	path = filepath.ToSlash(path)
	for _, anchor := range callerPathAnchors {
		if idx := strings.LastIndex(path, anchor); idx >= 0 {
			return path[idx+len(anchor):]
		}
	}

	parts := strings.Split(path, "/")
	if len(parts) <= 4 {
		return path
	}

	return strings.Join(parts[len(parts)-4:], "/")
}

func trimFunctionName(name string) string {
	name = strings.ReplaceAll(name, "·", ".")
	if lastSlash := strings.LastIndexByte(name, '/'); lastSlash >= 0 {
		name = name[lastSlash+1:]
	}
	return name
}

func (ll *consoleLogger) CopyWithPrefix(prefix string) Logger {
	return &consoleLogger{
		warning:    ll.warning,
		panic:      ll.panic,
		error:      ll.error,
		info:       ll.info,
		debug:      ll.debug,
		prefix:     mergePrefix(ll.prefix, prefix),
		out:        ll.out,
		mu:         ll.mu,
		now:        ll.now,
		showCaller: ll.showCaller,
		forceColor: ll.forceColor,
		stacktrace: ll.stacktrace,
	}
}

func mergePrefix(base, extra string) string {
	base = strings.TrimSpace(base)
	extra = strings.TrimSpace(extra)
	switch {
	case base == "":
		return extra
	case extra == "":
		return base
	default:
		return base + " " + extra
	}
}

func (ll *consoleLogger) SupportColor() bool {
	return ll.forceColor || (!color.NoColor && ll.out == color.Output)
}

type loggingFunc func(*consoleLogger, string, ...any)

func noopLoggingFunc(*consoleLogger, string, ...any) {}

var levelStyles = map[string]struct {
	label string
	attrs []color.Attribute
}{
	"WARN":  {label: "WARN ", attrs: []color.Attribute{color.FgYellow, color.Bold}},
	"PANIC": {label: "PANIC", attrs: []color.Attribute{color.FgWhite, color.BgRed, color.Bold}},
	"ERROR": {label: "ERROR", attrs: []color.Attribute{color.FgRed, color.Bold}},
	"INFO":  {label: "INFO ", attrs: []color.Attribute{color.FgCyan, color.Bold}},
	"DEBUG": {label: "DEBUG", attrs: []color.Attribute{color.FgHiBlack, color.Bold}},
}

func formatLevel(level string, withColor bool, forceColor bool) string {
	style, ok := levelStyles[level]
	if !ok {
		label := fmt.Sprintf("%-5s", strings.ToUpper(level))
		if withColor {
			return colorize(label, forceColor, color.Bold)
		}
		return label
	}

	if withColor {
		return colorize(style.label, forceColor, style.attrs...)
	}

	return style.label
}

func colorize(text string, forceColor bool, attrs ...color.Attribute) string {
	c := color.New(attrs...)
	if forceColor {
		c.EnableColor()
	}
	return c.Sprint(text)
}

func shouldShowCaller(level LogLevel, mode CallerMode) bool {
	switch mode {
	case CallerModeOn:
		return true
	case CallerModeOff:
		return false
	default:
		return level == LevelDebug
	}
}

func (ll *consoleLogger) shouldIncludeStack(level string) bool {
	switch ll.stacktrace {
	case StacktraceModeAll:
		return true
	case StacktraceModeError:
		return level == "ERROR" || level == "PANIC"
	case StacktraceModePanic:
		return level == "PANIC"
	default:
		return false
	}
}

// Request helper func to log request.
func Request(l Logger, incoming bool, code int, method, clientIP, path, err string, start time.Time) {
	param := gin.LogFormatterParams{
		StatusCode: code,
		Method:     method,
	}

	statusCode := fmt.Sprintf("%3d", code)
	methodLabel := fmt.Sprintf("%-7s", method)
	if l.SupportColor() {
		statusCode = param.StatusCodeColor() + statusCode + param.ResetColor()
		methodLabel = param.MethodColor() + methodLabel + param.ResetColor()
	}

	direction := requestDirectionOut
	if incoming {
		direction = requestDirectionIn
	}

	latency := time.Since(start)
	if latency < 0 {
		latency = 0
	}
	latency = latency.Round(time.Microsecond)

	message := fmt.Sprintf(
		"HTTP %s status=%s duration=%s ip=%s method=%s path=%s",
		direction,
		statusCode,
		latency,
		clientIP,
		methodLabel,
		path,
	)

	err = strings.TrimSpace(err)
	if err != "" {
		message += " err=" + err
	}

	switch {
	case err != "" || code >= 500:
		l.Error("%s", message)
	case code >= 400:
		l.Warning("%s", message)
	default:
		l.Info("%s", message)
	}
}
