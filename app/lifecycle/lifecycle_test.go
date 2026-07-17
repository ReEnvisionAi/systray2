//go:build windows && unit_test

package lifecycle

import (
	"sync"
	"testing"
	"time"

	"github.com/ReEnvision-AI/systray/app/tray/commontray"
)

// Mock tray implementation for testing
type mockTray struct {
	statusText string
	started    bool
	callbacks  commontray.Callbacks
}

func (m *mockTray) Run()                               {}
func (m *mockTray) Quit()                              {}
func (m *mockTray) UpdateAvailable(ver string) error   { return nil }
func (m *mockTray) GetCallbacks() commontray.Callbacks {
	return m.callbacks
}
func (m *mockTray) ChangeStatusText(text string) error {
	m.statusText = text
	return nil
}
func (m *mockTray) SetStarted() error   { m.started = true; return nil }
func (m *mockTray) SetStopped() error   { m.started = false; return nil }
func (m *mockTray) SetInferenceMode(mode string) error { return nil }
func (m *mockTray) DisplayFirstUseNotification() error { return nil }

func setupMockTray() *mockTray {
	mt := &mockTray{
		callbacks: commontray.Callbacks{
			Quit:               make(chan struct{}, 1),
			Update:             make(chan struct{}, 1),
			DoFirstUse:         make(chan struct{}, 1),
			ShowLogs:           make(chan struct{}, 1),
			StartContainer:     make(chan struct{}, 1),
			StopContainer:      make(chan struct{}, 1),
			SetModePrivate:     make(chan struct{}, 1),
			SetModeDistributed: make(chan struct{}, 1),
		},
	}
	t = mt // Set the global tray variable
	return mt
}

func resetState() {
	stateMu.Lock()
	currentState = StateStopped
	stateMu.Unlock()

	sleepStateMu.Lock()
	wasRunningBeforeSleep = false
	sleepStateMu.Unlock()
}

func TestSetState(t *testing.T) {
	setupMockTray()
	defer resetState()

	tests := []struct {
		state    AppState
		expected string
	}{
		{StateStopped, "Stopped"},
		{StateStarting, "Starting..."},
		{StateRunning, "Running"},
		{StateStopping, "Stopping..."},
		{StateError, "Please restart ReEnvision AI"},
		{StateThankyou, "Thank you!"},
	}

	for _, test := range tests {
		SetState(test.state)

		stateMu.Lock()
		if currentState != test.state {
			t.Errorf("Expected state %d, got %d", test.state, currentState)
		}
		stateMu.Unlock()

		// Check if tray status text was updated
		// Note: mockTray implementation would need to be enhanced to verify this
	}
}

func TestHandleSleepEvent(t *testing.T) {
	setupMockTray()
	defer resetState()

	// Test when container is running
	SetState(StateRunning)
	handleSleepEvent()

	sleepStateMu.Lock()
	if !wasRunningBeforeSleep {
		t.Error("Expected wasRunningBeforeSleep to be true when container is running")
	}
	sleepStateMu.Unlock()

	// Test when container is stopped
	resetState()
	SetState(StateStopped)
	handleSleepEvent()

	sleepStateMu.Lock()
	if wasRunningBeforeSleep {
		t.Error("Expected wasRunningBeforeSleep to be false when container is stopped")
	}
	sleepStateMu.Unlock()
}

func TestHandleWakeEvent(testT *testing.T) {
	mockTray := setupMockTray()
	defer resetState()

	// Test wake event when container was running before sleep
	sleepStateMu.Lock()
	wasRunningBeforeSleep = true
	sleepStateMu.Unlock()

	SetState(StateStopped)

	// Capture the start container channel
	callbacks := mockTray.GetCallbacks()

	handleWakeEvent()

	// Check if restart was triggered (should receive on StartContainer channel within timeout)
	select {
	case <-callbacks.StartContainer:
		// Restart was triggered
	case <-time.After(4 * time.Second): // Wait longer than the 3-second delay
		testT.Error("Expected container restart to be triggered within 4 seconds")
	}

	// Test wake event when container was not running before sleep
	resetState()
	sleepStateMu.Lock()
	wasRunningBeforeSleep = false
	sleepStateMu.Unlock()

	handleWakeEvent()

	// Should not trigger restart
	select {
	case <-callbacks.StartContainer:
		testT.Error("Expected no container restart when wasRunningBeforeSleep is false")
	case <-time.After(100 * time.Millisecond):
		// No restart triggered, which is expected
	}
}

func TestHandleWakeEventInInvalidStates(testT *testing.T) {
	mockTray := setupMockTray()
	defer resetState()

	// Test wake event when container is already starting
	sleepStateMu.Lock()
	wasRunningBeforeSleep = true
	sleepStateMu.Unlock()

	SetState(StateStarting)
	callbacks := mockTray.GetCallbacks()

	handleWakeEvent()

	// Should not trigger restart since container is already starting
	select {
	case <-callbacks.StartContainer:
		testT.Error("Expected no container restart when state is StateStarting")
	case <-time.After(4 * time.Second):
		// No restart triggered, which is expected
	}

	// Test wake event when container is already running
	resetState()
	sleepStateMu.Lock()
	wasRunningBeforeSleep = true
	sleepStateMu.Unlock()

	SetState(StateRunning)
	handleWakeEvent()

	// Should not trigger restart since container is already running
	select {
	case <-callbacks.StartContainer:
		testT.Error("Expected no container restart when state is StateRunning")
	case <-time.After(100 * time.Millisecond):
		// No restart triggered, which is expected
	}
}

func TestConcurrentSleepWakeEvents(t *testing.T) {
	setupMockTray()
	defer resetState()

	var wg sync.WaitGroup
	numGoroutines := 10

	// Test concurrent sleep events
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handleSleepEvent()
		}()
	}

	// Wait for all sleep events to complete
	wg.Wait()

	// Test concurrent wake events
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handleWakeEvent()
		}()
	}

	// Wait for all wake events to complete
	wg.Wait()
}

func TestSleepStateThreadSafety(t *testing.T) {
	setupMockTray()
	defer resetState()

	var wg sync.WaitGroup
	numOperations := 100

	// Concurrent read/write operations on sleep state
	for i := 0; i < numOperations; i++ {
		wg.Add(3)

		// Goroutine 1: Handle sleep events
		go func() {
			defer wg.Done()
			SetState(StateRunning)
			handleSleepEvent()
		}()

		// Goroutine 2: Handle wake events
		go func() {
			defer wg.Done()
			handleWakeEvent()
		}()

		// Goroutine 3: Read sleep state
		go func() {
			defer wg.Done()
			sleepStateMu.Lock()
			_ = wasRunningBeforeSleep
			sleepStateMu.Unlock()
		}()
	}

	wg.Wait()
}

func TestAppStateString(t *testing.T) {
	tests := []struct {
		state    AppState
		expected string
	}{
		{StateStopped, "Stopped"},
		{StateStarting, "Starting..."},
		{StateRunning, "Running"},
		{StateStopping, "Stopping..."},
		{StateError, "Please restart ReEnvision AI"},
		{StateThankyou, "Thank you!"},
		{AppState(999), "Unknown"}, // Test unknown state
	}

	for _, test := range tests {
		result := test.state.String()
		if result != test.expected {
			t.Errorf("Expected %s for state %d, got %s", test.expected, test.state, result)
		}
	}
}

func TestPowerManagementIntegration(t *testing.T) {
	setupMockTray()
	defer resetState()

	// Test that state transitions work correctly without sleep prevention
	SetState(StateRunning)

	stateMu.Lock()
	if currentState != StateRunning {
		t.Errorf("Expected state to be StateRunning, got %d", currentState)
	}
	stateMu.Unlock()

	SetState(StateStopped)

	stateMu.Lock()
	if currentState != StateStopped {
		t.Errorf("Expected state to be StateStopped, got %d", currentState)
	}
	stateMu.Unlock()

	// Note: Sleep prevention functionality has been removed
	// Sleep detection and resume functionality should still work
}

func TestSleepDetectionChannels(t *testing.T) {
	// Test that sleep detection channels work properly
	if sleepChan != nil || wakeChan != nil {
		t.Error("Expected sleep channels to be nil initially")
	}

	// Note: Full testing would require mocking power.StartSleepDetection()
	// For now, we test the channel handling logic
}

// Benchmark tests
func BenchmarkSetState(b *testing.B) {
	setupMockTray()
	defer resetState()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SetState(StateRunning)
		SetState(StateStopped)
	}
}

func BenchmarkHandleSleepEvent(b *testing.B) {
	setupMockTray()
	defer resetState()

	SetState(StateRunning)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		handleSleepEvent()
	}
}

func BenchmarkHandleWakeEvent(b *testing.B) {
	setupMockTray()
	defer resetState()

	sleepStateMu.Lock()
	wasRunningBeforeSleep = true
	sleepStateMu.Unlock()

	SetState(StateStopped)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		go handleWakeEvent()
	}
}