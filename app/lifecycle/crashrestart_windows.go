package lifecycle

// Automatic restart after an unexpected container exit (M-2).
//
// When the podman process dies without a stop request, the tray used to park
// in StateError until the user noticed. Instead, retry with exponential
// backoff and a bounded number of attempts; a stable run resets the counter
// so a machine that crashes once a week never exhausts its budget.

import (
	"log/slog"
	"sync"
	"time"
)

const (
	crashRestartMaxAttempts = 5
	crashRestartBaseDelay   = 5 * time.Second
	crashStableRunReset     = 10 * time.Minute
)

var (
	crashRestartMu     sync.Mutex
	crashRestartCount  int
	lastContainerStart time.Time
)

func noteContainerStarted() {
	crashRestartMu.Lock()
	lastContainerStart = time.Now()
	crashRestartMu.Unlock()
}

// scheduleCrashRestart queues an automatic restart after an unexpected
// container exit. Safe to call from the process-exit goroutine.
func scheduleCrashRestart() {
	shutdownMu.Lock()
	down := isShuttingDown
	shutdownMu.Unlock()
	if down {
		return
	}

	crashRestartMu.Lock()
	if !lastContainerStart.IsZero() && time.Since(lastContainerStart) > crashStableRunReset {
		crashRestartCount = 0
	}
	if crashRestartCount >= crashRestartMaxAttempts {
		crashRestartMu.Unlock()
		slog.Error("Container keeps crashing; giving up on automatic restarts",
			"attempts", crashRestartMaxAttempts)
		return
	}
	crashRestartCount++
	attempt := crashRestartCount
	delay := crashRestartBaseDelay << (attempt - 1)
	crashRestartMu.Unlock()

	slog.Info("Scheduling automatic container restart after crash",
		"attempt", attempt, "max_attempts", crashRestartMaxAttempts, "delay", delay.String())
	go func() {
		time.Sleep(delay)

		shutdownMu.Lock()
		down := isShuttingDown
		shutdownMu.Unlock()
		if down {
			return
		}
		stateMu.Lock()
		state := currentState
		stateMu.Unlock()
		if state == StateRunning || state == StateStarting || state == StateStopping {
			// Someone already intervened (manual restart, or a stop in progress).
			return
		}

		slog.Info("Restarting container after crash", "attempt", attempt)
		handleStartRequest()
	}()
}
