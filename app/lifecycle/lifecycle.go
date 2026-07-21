package lifecycle

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/ReEnvision-AI/systray/app/power"
	"github.com/ReEnvision-AI/systray/app/store"
	"github.com/ReEnvision-AI/systray/app/tray"
	"github.com/ReEnvision-AI/systray/app/tray/commontray"
)

type AppState int

const (
	StateStopped AppState = iota
	StateStarting
	StateRunning
	StateStopping
	StateThankyou
	StateError
)

var (
	currentState AppState = StateStopped
	stateMu      sync.Mutex
	t            commontray.ReaiTray

	// Sleep/resume state tracking
	wasRunningBeforeSleep bool
	sleepStateMu          sync.Mutex
	sleepChan             chan struct{}
	wakeChan              chan struct{}
	isShuttingDown        bool
	shutdownMu            sync.Mutex
)

func (s AppState) String() string {
	switch s {
	case StateStopped:
		return "Stopped"
	case StateStarting:
		return "Starting..."
	case StateRunning:
		return "Running"
	case StateStopping:
		return "Stopping..."
	case StateError:
		return "Please restart ReEnvision AI"
	case StateThankyou:
		return "Thank you!"
	default:
		return "Unknown"
	}
}

func Run() {
	InitLogging()
	slog.Info("ReEnvision AI app starting")

	updaterCtx, updaterCancel := context.WithCancel(context.Background())
	var updaterDone chan int

	var err error
	t, err = tray.NewTray()
	if err != nil {
		log.Fatalf("Failed to start: %s", err)
	}

	callbacks := t.GetCallbacks()

	// Initialize sleep detection
	sleepChan, wakeChan, err = power.StartSleepDetection()
	if err != nil {
		slog.Warn("Failed to start sleep detection", "error", err)
		// Continue without sleep detection
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Debug("starting callback loop")
		for {
			select {
			case <-callbacks.Quit:
				slog.Debug("quit called")
				handleQuit()
			case <-signals:
				slog.Debug("shutting down due to signal")
				handleQuit()
			case <-callbacks.Update:
				err := DoUpgrade(updaterCancel, updaterDone)
				if err != nil {
					slog.Warn("upgrade attempt failed", "error", err)
				}
			case <-callbacks.ShowLogs:
				ShowLogs()
			case <-callbacks.StartContainer:
				// Start the container
				slog.Info("Starting container")
				handleStartRequest()
			case <-callbacks.StopContainer:
				// Stop the container
				slog.Info("Stopping container")
				handleStopRequest()
			case <-callbacks.SetModePrivate:
				handleModeChange(InferenceModePrivate)
			case <-callbacks.SetModeDistributed:
				handleModeChange(InferenceModeDistributed)
			case <-callbacks.DoFirstUse:
				err := GetStarted()
				if err != nil {
					slog.Warn("Failed to launch getting started shell", "error", err)
				}
			case <-sleepChan:
				// System is going to sleep
				handleSleepEvent()
			case <-wakeChan:
				// System is waking from sleep
				handleWakeEvent()
			}
		}
	}()

	// Reflect the configured inference mode in the tray menu. Config load can
	// fail on first run (missing credential); the menu then keeps its default.
	if cfg, err := LoadConfig(); err == nil {
		if err := t.SetInferenceMode(cfg.InferenceMode); err != nil {
			slog.Warn("Failed to set inference mode menu state", "error", err)
		}
	} else {
		slog.Warn("Could not load config for mode menu init", "error", err)
	}

	// Are we first use?
	if !store.GetFirstTimeRun() {
		slog.Debug("First time run")
		err = t.DisplayFirstUseNotification()
		if err != nil {
			slog.Debug("failed to display first use notification", "error", err)
		}
		store.SetFirstTimeRun(true)
	} else {
		slog.Debug("Not first time, skipping first run notification")
	}

	StartBackgroundUpdaterChecker(updaterCtx, t.UpdateAvailable)
	StartAssignmentPoller(updaterCtx)

	handleStartRequest()

	t.Run()

	updaterCancel()
	slog.Info("Waiting for app to shutdown..")
	if updaterDone != nil {
		<-updaterDone
	}

	slog.Info("ReEnvision AI app exiting")
	CloseLogging()
}

func SetState(newState AppState) {
	stateMu.Lock()
	currentState = newState
	stateMu.Unlock()
	t.ChangeStatusText(newState.String())

	switch newState {
	case StateStopping, StateStopped, StateError:
		t.SetStopped()
	case StateStarting, StateRunning:
		t.SetStarted()
	}
}

func handleStartRequest() {
	SetState(StateStarting)

	ctx := context.Background()

	err := StartContainer(ctx)
	if err != nil {
		slog.Error("Failed to start container", "error", err)
		SetState(StateError)
		return
	}
}

func handleStopRequest() {
	SetState(StateStopping)
	ctx, cancel := context.WithTimeout(context.Background(), podmanStopTimeout)
	defer cancel()

	err := StopContainer(ctx)
	if err != nil {
		slog.Error("Failed to stop container process", "error", err)
		// Should we go to Error state or Stopped state? Let's assume Stopped for now.
		SetState(StateStopped)
		// Consider showing an error message
	} else {
		SetState(StateStopped) // Explicitly set to stopped on successful stop
	}
}

// handleModeChange persists the selected inference mode, updates the tray
// menu, and restarts the container (if running) so the new mode's server
// flags take effect. StartContainer re-reads config.json, so the persisted
// mode is picked up automatically.
func handleModeChange(mode string) {
	slog.Info("Switching inference mode", "mode", mode)

	if err := SaveInferenceMode(mode); err != nil {
		slog.Error("Failed to persist inference mode", "error", err)
		return
	}

	if err := t.SetInferenceMode(mode); err != nil {
		slog.Warn("Failed to update inference mode menu state", "error", err)
	}

	stateMu.Lock()
	running := currentState == StateRunning || currentState == StateStarting
	stateMu.Unlock()

	if running {
		slog.Info("Restarting container to apply new inference mode")
		handleStopRequest()
		handleStartRequest()
	}
}

func handleQuit() {
	slog.Info("Quitting..")

	// Set shutdown flag to prevent sleep/wake event processing
	shutdownMu.Lock()
	isShuttingDown = true
	shutdownMu.Unlock()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), podmanStopTimeout+5*time.Second) // Give a bit extra time
	defer cancel()

	stateMu.Lock()
	shouldStop := currentState == StateRunning || currentState == StateStarting
	stateMu.Unlock()

	if shouldStop {
		slog.Info("Attempting graceful shutdown of container...")
		// This might block, so use the shutdown context
		err := StopContainer(shutdownCtx)
		if err != nil {
			slog.Error("Error during shutdown stop", "error", err)
		}
	}

	t.Quit()

	// Stop sleep detection
	if power.WasSleepDetectionActive() {
		if err := power.StopSleepDetection(); err != nil {
			slog.Warn("Failed to stop sleep detection", "error", err)
		}
	}

	slog.Info("Finished exit procedures.")
}

// handleSleepEvent is called when the system is going to sleep
func handleSleepEvent() {
	// Skip sleep event handling during shutdown
	shutdownMu.Lock()
	shuttingDown := isShuttingDown
	shutdownMu.Unlock()

	if shuttingDown {
		return
	}

	slog.Info("Handling system sleep event")

	sleepStateMu.Lock()
	defer sleepStateMu.Unlock()

	// Check if container is currently running
	stateMu.Lock()
	containerIsRunning := currentState == StateRunning
	stateMu.Unlock()

	if containerIsRunning {
		slog.Info("Container is running, marking for restart after sleep")
		wasRunningBeforeSleep = true
	} else {
		slog.Info("Container is not running, no restart needed after sleep")
		wasRunningBeforeSleep = false
	}
}

// handleWakeEvent is called when the system is waking from sleep
func handleWakeEvent() {
	// Skip wake event handling during shutdown
	shutdownMu.Lock()
	shuttingDown := isShuttingDown
	shutdownMu.Unlock()

	if shuttingDown {
		return
	}

	slog.Info("Handling system wake event")

	sleepStateMu.Lock()
	defer sleepStateMu.Unlock()

	if wasRunningBeforeSleep {
		slog.Info("Container was running before sleep, attempting to restart")

		// Check current state first
		stateMu.Lock()
		currentStateValue := currentState
		stateMu.Unlock()

		// Always restart the container if it was running before sleep, as the process
		// might be in an inconsistent state after sleep
		slog.Info("Restarting container after sleep", "previous_state", currentStateValue)
		go func() {
			// Add a small delay to ensure system is fully awake
			time.Sleep(3 * time.Second)

			// Force stop first if the container appears to be running
			if currentStateValue == StateRunning || currentStateValue == StateStarting {
				slog.Info("Stopping potentially inconsistent container before restart")
				handleStopRequest()
				// Give it a moment to stop
				time.Sleep(2 * time.Second)
			}

			slog.Info("Starting container after sleep")
			handleStartRequest()
		}()

		// Reset the sleep state flag
		wasRunningBeforeSleep = false
	} else {
		slog.Info("Container was not running before sleep, no restart needed")
	}
}
