package logging

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fatih/color"
)

func TestConsoleLoggerDebugShowsCallerAndPrefix(t *testing.T) {
	restoreColor := disableColorsForTest()
	defer restoreColor()

	var out bytes.Buffer
	logger := newConsoleLogger(LevelDebug, &out, func() time.Time {
		return time.Date(2026, 4, 1, 9, 30, 15, 123_000_000, time.Local)
	}).CopyWithPrefix("[Cid: test-cid]")

	expectedCaller := func() string {
		_, file, line, ok := runtime.Caller(0)
		if !ok {
			t.Fatal("failed to get runtime caller")
		}

		logger.Info("worker started")
		return fmt.Sprintf("%s:%d", trimCallerPath(file), line+5)
	}()

	line := strings.TrimSpace(out.String())
	if !strings.HasPrefix(line, "2026-04-01 09:30:15.123 INFO ") {
		t.Fatalf("unexpected log prefix: %s", line)
	}
	if !strings.Contains(line, "["+expectedCaller+"]") {
		t.Fatalf("expected caller %q in log line: %s", expectedCaller, line)
	}
	if !strings.Contains(line, "[Cid: test-cid] worker started") {
		t.Fatalf("expected prefix and message in log line: %s", line)
	}
}

func TestConsoleLoggerInfoModeOmitsCaller(t *testing.T) {
	restoreColor := disableColorsForTest()
	defer restoreColor()

	var out bytes.Buffer
	logger := newConsoleLogger(LevelInformational, &out, func() time.Time {
		return time.Date(2026, 4, 1, 9, 30, 15, 123_000_000, time.Local)
	})

	logger.Info("production log")

	line := strings.TrimSpace(out.String())
	if strings.Contains(line, "[pkg/logging/logger_test.go:") {
		t.Fatalf("info mode should not include caller, got: %s", line)
	}
	if !strings.Contains(line, "INFO  production log") {
		t.Fatalf("unexpected info log line: %s", line)
	}
}

func TestConsoleLoggerCallerModeOffDisablesCallerInDebug(t *testing.T) {
	restoreColor := disableColorsForTest()
	defer restoreColor()

	var out bytes.Buffer
	logger := newConsoleLogger(LevelDebug, &out, func() time.Time {
		return time.Date(2026, 4, 1, 9, 30, 15, 123_000_000, time.Local)
	}, WithCallerMode(CallerModeOff))

	logger.Info("no caller")

	line := strings.TrimSpace(out.String())
	if strings.Contains(line, "[pkg/logging/logger_test.go:") {
		t.Fatalf("caller mode off should suppress caller, got: %s", line)
	}
	if !strings.Contains(line, "INFO  no caller") {
		t.Fatalf("unexpected log line: %s", line)
	}
}

func TestConsoleLoggerForceColorOverridesGlobalNoColor(t *testing.T) {
	restoreColor := disableColorsForTest()
	defer restoreColor()

	var out bytes.Buffer
	logger := newConsoleLogger(LevelInformational, &out, func() time.Time {
		return time.Date(2026, 4, 1, 9, 30, 15, 123_000_000, time.Local)
	}, WithForceColor(true))

	logger.Info("forced color")

	line := out.String()
	if !strings.Contains(line, "\x1b[") {
		t.Fatalf("force color should emit ANSI color codes, got: %q", line)
	}
}

func TestConsoleLoggerDebugDriverMessageUsesVisibleColor(t *testing.T) {
	restoreColor := disableColorsForTest()
	defer restoreColor()

	var out bytes.Buffer
	logger := newConsoleLogger(LevelDebug, &out, func() time.Time {
		return time.Date(2026, 4, 1, 9, 30, 15, 123_000_000, time.Local)
	}, WithForceColor(true))

	logger.Debug("driver.Query: query=%s args=%v time=%s", "select 1", []int{1}, "1ms")

	line := out.String()
	if !strings.Contains(line, "\x1b[90;1mDEBUG") && !strings.Contains(line, "\x1b[1;90mDEBUG") {
		t.Fatalf("debug level should use deep gray color, got: %q", line)
	}
	if strings.Contains(line, "\x1b[90m[pkg/logging/logger_test.go:") || strings.Contains(line, "\x1b[1;90m[pkg/logging/logger_test.go:") {
		t.Fatalf("debug caller should not be colorized, got: %q", line)
	}
	if strings.Contains(line, "\x1b[90mdriver.Query:") || strings.Contains(line, "\x1b[1;90mdriver.Query:") {
		t.Fatalf("debug message should not be colorized, got: %q", line)
	}
}

func TestFromContextUsesDefaultLoggerWhenContextMissingLogger(t *testing.T) {
	restoreColor := disableColorsForTest()
	defer restoreColor()

	prevDefault := DefaultLogger()
	defer SetDefaultLogger(prevDefault)

	var out bytes.Buffer
	SetDefaultLogger(newConsoleLogger(LevelDebug, &out, func() time.Time {
		return time.Date(2026, 4, 1, 9, 30, 15, 123_000_000, time.Local)
	}, WithForceColor(true)))

	FromContext(context.Background()).Debug("driver.Query: query=%s", "select 1")

	line := out.String()
	if !strings.Contains(line, "\x1b[90;1mDEBUG") && !strings.Contains(line, "\x1b[1;90mDEBUG") {
		t.Fatalf("fallback from context should reuse default logger color config, got: %q", line)
	}
}

func TestConsoleLoggerStacktraceModeErrorIncludesStack(t *testing.T) {
	restoreColor := disableColorsForTest()
	defer restoreColor()

	var out bytes.Buffer
	logger := newConsoleLogger(LevelInformational, &out, func() time.Time {
		return time.Date(2026, 4, 1, 9, 30, 15, 123_000_000, time.Local)
	}, WithStacktraceMode(StacktraceModeError))

	logger.Error("boom")

	line := out.String()
	if !strings.Contains(line, "stacktrace:") {
		t.Fatalf("error stacktrace should be included, got: %s", line)
	}
	if !strings.Contains(line, "pkg/logging/logger_test.go") {
		t.Fatalf("stacktrace should include caller file, got: %s", line)
	}
}

func TestRecoverUsesPanicLevelWithoutRepanic(t *testing.T) {
	restoreColor := disableColorsForTest()
	defer restoreColor()

	var out bytes.Buffer
	logger := newConsoleLogger(LevelInformational, &out, func() time.Time {
		return time.Date(2026, 4, 1, 9, 30, 15, 123_000_000, time.Local)
	}, WithStacktraceMode(StacktraceModePanic))

	Recover(logger, "recovered %s", "panic")

	line := out.String()
	if !strings.Contains(line, "PANIC") {
		t.Fatalf("recover should log as panic level, got: %s", line)
	}
	if !strings.Contains(line, "stacktrace:") {
		t.Fatalf("recover should include stacktrace in panic mode, got: %s", line)
	}
	if !strings.Contains(line, "recovered panic") {
		t.Fatalf("recover message missing, got: %s", line)
	}
}

func TestRequestUsesOuterCallerAndErrorLevel(t *testing.T) {
	restoreColor := disableColorsForTest()
	defer restoreColor()

	var out bytes.Buffer
	logger := newConsoleLogger(LevelDebug, &out, func() time.Time {
		return time.Date(2026, 4, 1, 9, 30, 15, 123_000_000, time.Local)
	})

	expectedCaller := func() string {
		_, file, line, ok := runtime.Caller(0)
		if !ok {
			t.Fatal("failed to get runtime caller")
		}

		Request(logger, true, 502, "GET", "127.0.0.1", "/api/search", "upstream timeout", time.Now().Add(-15*time.Millisecond))
		return fmt.Sprintf("%s:%d", trimCallerPath(file), line+5)
	}()

	line := strings.TrimSpace(out.String())
	if !strings.Contains(line, "ERROR [") {
		t.Fatalf("request error should use error level, got: %s", line)
	}
	if !strings.Contains(line, "["+expectedCaller+"]") {
		t.Fatalf("request log should point to outer caller %q, got: %s", expectedCaller, line)
	}
	if strings.Contains(line, "pkg/logging/logger.go:") {
		t.Fatalf("request log should not point to logging internals: %s", line)
	}
	if !strings.Contains(line, "HTTP IN status=502") {
		t.Fatalf("unexpected request message: %s", line)
	}
	if !strings.Contains(line, "err=upstream timeout") {
		t.Fatalf("expected inline request error, got: %s", line)
	}
}

func disableColorsForTest() func() {
	prev := color.NoColor
	color.NoColor = true
	return func() {
		color.NoColor = prev
	}
}
