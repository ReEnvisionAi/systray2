package lifecycle

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const (
	podmanVolumeName          = "reai-cache:/cache"
	nvidiaCDIConfPath         = "/etc/cdi/nvidia.yaml"
	podmanMachineStartTimeout = 5 * time.Minute
	podmanInfoPollInterval    = 5 * time.Second
	podmanStopTimeout         = 30 * time.Second
)

var (
	currentCmd *exec.Cmd          // Holds the running podman command
	cancelCmd  context.CancelFunc // Function to cancel the currentCmd context
	appConfig  AppConfig
)

func StartContainer(ctx context.Context) error {
	var err error
	appConfig, err = LoadConfig()
	if err != nil {
		slog.Error("Failed to load configuration", "error", err)
		return err
	}

	// Wait for Podman Service
	if err := waitForPodman(ctx); err != nil {
		return fmt.Errorf("podman service check failed")
	}

	setupCtx, setupCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer setupCancel()
	if err := setupPodmanNvidia(setupCtx); err != nil {
		return fmt.Errorf("failed to setup Podman for NVIDIA: %w", err)
	}

	stateMu.Lock()
	//check the state
	if currentState != StateStarting {
		slog.Warn("Container start aborted.", "state", currentState)
		stateMu.Unlock()

		return nil
	}

	cmdCtx, cmdCancel := context.WithCancel(context.Background())
	cancelCmd = cmdCancel

	args := buildPodmanRunCommandArgs()
	currentCmd = exec.CommandContext(cmdCtx, "podman", args...)
	currentCmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	slog.Info("Starting container", "command", currentCmd.String())

	stdoutPipe, err := currentCmd.StdoutPipe()
	if err != nil {
		cancelCmd() // Clean up context
		currentCmd = nil
		stateMu.Unlock()
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	stderrPipe, err := currentCmd.StderrPipe()
	if err != nil {
		cancelCmd()
		currentCmd = nil
		stateMu.Unlock()
		return fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	// Release the lock before starting the command and goroutines
	stateMu.Unlock()

	// Start capturing output *before* starting the command
	var wg sync.WaitGroup
	wg.Add(2)
	go captureOutput(&wg, stdoutPipe, "stdout")
	go captureOutput(&wg, stderrPipe, "stderr")

	if err := currentCmd.Start(); err != nil {
		cancelCmd() // Clean up context
		stateMu.Lock()
		currentCmd = nil
		stateMu.Unlock()

		outputCaptureDone := make(chan struct{})
		go func() {
			wg.Wait()
			close(outputCaptureDone)

		}()
		select {
		case <-outputCaptureDone:
			// Goroutines finished
		case <-time.After(1 * time.Second):
			slog.Warn("Timeout waiting for output goroutines after command start failure")
		}
		return fmt.Errorf("failed to start podman command: %w", err)
	}

	slog.Info("Container process started successfully.", "pid", currentCmd.Process.Pid)
	SetState(StateRunning) // Transition to Running state *after* successful start

	// Goroutine to wait for the command to exit and handle cleanup
	go func() {
		// Wait for the command to finish (either normally, by error, or cancellation)
		waitErr := currentCmd.Wait()

		// Wait for output streams to be fully processed
		wg.Wait()

		stateMu.Lock()
		// Check if we are supposed to be stopping; if so, the state is handled by stopContainerProcess
		isStopping := currentState == StateStopping
		// Clear command and cancel function regardless
		currentCmd = nil
		cancelCmd = nil // Allow GC
		stateMu.Unlock()

		if waitErr != nil {
			// Log error unless it was context cancellation during a planned stop
			if !(errors.Is(waitErr, context.Canceled) && isStopping) {
				slog.Error("Container process exited unexpectedly.", "error", waitErr)
				if !isStopping { // Avoid overwriting Stopping state
					SetState(StateError)
				}
			} else {
				slog.Info("Container process exited after cancellation (likely during stop).")
				// State should already be Stopping or Stopped
			}
		} else {
			slog.Info("Container process exited normally.")
			if !isStopping { // If it exited normally without a stop request
				SetState(StateStopped)
			}
		}
	}()

	return nil
}

func StopContainer(ctx context.Context) error {
	slog.Info("Attempting to stop container.", "name", appConfig.ContainerName)

	// Use `podman stop` first for graceful shutdown within the container
	stopCmd := exec.CommandContext(ctx, "podman", "stop", appConfig.ContainerName)
	stopCmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	stopOutput, stopErr := stopCmd.CombinedOutput()

	if stopErr != nil {
		// Log the error but continue, as we might need to cancel the `podman run` process anyway
		slog.Warn("`podman stop` command failed or timed out.",
			"output", string(stopOutput),
			"error", stopErr)
		// If the context timed out, log that specifically
		if errors.Is(stopErr, context.DeadlineExceeded) {
			slog.Warn("Context deadline exceeded while waiting for `podman stop`.")
		} else if ctx.Err() != nil {
			// Parent context was canceled (e.g., during shutdown)
			slog.Warn("Stop operation canceled by parent context.", "error", ctx.Err())
		}
	} else {
		slog.Info("`podman stop` command completed successfully.", "output", string(stopOutput))
	}

	// Regardless of `podman stop` success, cancel the `podman run` command's context.
	// This signals `currentCmd.Wait()` to unblock if it hasn't already.
	stateMu.Lock()
	if cancelCmd != nil {
		slog.Info("Cancelling container command context.")
		cancelCmd()
		// The goroutine waiting on currentCmd.Wait() should handle subsequent cleanup (setting currentCmd=nil etc.)
	} else {
		slog.Info("No active container command context to cancel.")
	}
	// We don't set currentCmd = nil here; the Wait() goroutine does that upon exit confirmation.
	stateMu.Unlock()

	// Note: We don't forcefully kill the `podman run` process (`currentCmd.Process.Kill()`)
	// because `podman stop` followed by context cancellation should be sufficient.
	// The `--rm` flag ensures the container is removed eventually. Killing `podman run`
	// might prevent `--rm` from working correctly within the Podman VM.

	// The state transition to Stopped is handled either by the handleStopRequest function
	// on success, or by the Wait() goroutine when the process finally exits.

	// Return the error from `podman stop` if there was one, allowing caller to know if graceful stop failed.
	if stopErr != nil && !errors.Is(stopErr, context.Canceled) && !errors.Is(stopErr, context.DeadlineExceeded) {
		return fmt.Errorf("podman stop failed: %w", stopErr)
	}

	return nil
}

func buildPodmanRunCommandArgs() []string {

	// Base arguments
	args := []string{
		"run",
		"--network=host", // Use host networking
		"--rm",           // Remove container on exit
		"--name=" + appConfig.ContainerName,
		"--volume=" + podmanVolumeName, // Mount cache volume
		"--pull=newer",                 // Pulls newer image even if same version
		"-e AGENT_GRID_VERSION=1.6.0",
	}

	// GPU arguments - Use CDI if available, requires Podman >= 4.x
	// Using --device nvidia.com/gpu=all enables CDI discovery.
	// --gpus=all might be redundant or an older way. Check Podman docs.
	// Let's use the recommended CDI approach if GPU is intended.
	// Assuming setupPodmanNvidia was successful if GPU is desired/present.
	// We might need a config flag or runtime check result to decide if GPU args are added.
	// For now, add them conditionally based on a simple config flag (example)
	if appConfig.UseGPU { // Assuming an `UseGPU bool` field in config.AppConfig
		slog.Info("Adding GPU arguments to podman run command.")
		args = append(args, "--device=nvidia.com/gpu=all")
		// Privilege/IPC might be needed for some GPU setups/drivers
		args = append(args, "--privileged") // CAUTION: Security risk! Evaluate if necessary.
		args = append(args, "--ipc=host")   // Often needed for CUDA multi-process
	} else {
		slog.Info("GPU arguments omitted based on configuration.")
	}

	// Add image and command parts
	args = append(args, appConfig.ContainerImage) // The image name
	args = append(args,                           // The command and its arguments within the container
		"python", "-m", "agentgrid.cli.run_server",
		"--inference_max_length", "136192",
		"--port", strconv.FormatUint(Port, 10),
		"--max_alloc_timeout", "6000",
		"--quant_type", "nf4",
		"--attn_cache_tokens", "128000",
		appConfig.ModelName,
		"--token", appConfig.Token,
		"--throughput", "eval",
		//"--initial_peers", appConfig.InitialPeers,
	)

	if appConfig.InferenceMode == InferenceModePrivate {
		// Private mode: run an isolated swarm. The node hosts the full model
		// for this machine only — it never dials public bootstrap peers and
		// no inference traffic leaves the box.
		slog.Info("Private inference mode: starting isolated swarm (no public peers)")
		args = append(args, "--new_swarm", "--skip_reachability_check")
	}

	return args
}

func waitForPodman(ctx context.Context) error {
	slog.Info("Waiting for Podman machine and service...")

	// Attempt to start the machine, ignore errors for now (might already be running)
	// Hide the window for this command.
	startCmd := exec.CommandContext(ctx, "podman", "machine", "start")
	startCmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	startOutput, startErr := startCmd.CombinedOutput()
	if startErr != nil {
		// Log output only if there was an error, might contain useful info
		slog.Warn("Podman machine start command finished", "output", string(startOutput), "error", startErr)
		// Don't return yet, maybe it's already running and 'podman info' will succeed
	} else {
		slog.Info("Podman machine start command finished", "output", string(startOutput))
	}

	// Check podman info periodically
	ticker := time.NewTicker(podmanInfoPollInterval)
	defer ticker.Stop()

	// Combined timeout for the whole wait process
	waitCtx, cancel := context.WithTimeout(ctx, podmanMachineStartTimeout)
	defer cancel()

	for {
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("timed out after %v waiting for podman service", podmanMachineStartTimeout)
		case <-ticker.C:
			slog.Info("Checking podman status...")
			cmd := exec.CommandContext(waitCtx, "podman", "info")
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			// Run and discard output, we only care about the exit code
			if err := cmd.Run(); err == nil {
				slog.Info("Podman service is ready.")
				return nil // Podman is ready
			} else {
				// Log the specific error from podman info
				slog.Info("Podman service not ready yet", "error", err)
			}
		}
	}
}

func setupPodmanNvidia(ctx context.Context) error {
	hasGPU, err := checkNvidiaGPU(ctx)
	if err != nil {
		// Log the error but don't necessarily block startup if check fails
		slog.Error("Error checking for Nvidia GPU", "error", err)
		// Decide if this is fatal. If GPU support is optional, maybe just warn and continue.
		// For now, let's warn and proceed without GPU setup.
		slog.Warn("Proceeding without attempting Nvidia CDI setup due to GPU check error.")
		return errors.New("error checking for Nvidia GPU")
	}

	if !hasGPU {
		slog.Info("No Nvidia GPU detected or nvidia-smi failed, skipping Nvidia CDI setup for Podman.")
		SetState(StateThankyou)
		return errors.New("no Nvidia GPU detected")
	}

	slog.Info("Nvidia GPU detected, attempting to configure Podman machine via CDI...")

	// Command to generate CDI spec inside the podman machine VM
	// IMPORTANT: This assumes passwordless sudo and nvidia-ctk installed in the VM.
	cdiCmd := fmt.Sprintf("sudo nvidia-ctk cdi generate --output=%s", nvidiaCDIConfPath)
	cmd := exec.CommandContext(ctx, "podman", "machine", "ssh", cdiCmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Error("Failed to generate Nvidia CDI configuration in Podman machine.",
			"command", cmd.String(),
			"output", string(output),
			"error", err)
		// This might be critical depending on whether GPU is required.
		// Returning an error signals failure.
		return fmt.Errorf("nvidia CDI setup failed: %w. Output: %s", err, string(output))
	}

	slog.Info("Successfully generated Nvidia CDI configuration.", "path_in_vm", nvidiaCDIConfPath, "output", string(output))
	return nil
}

func checkNvidiaGPU(ctx context.Context) (bool, error) {

	slog.Info("Checking for Nvidia GPU using nvidia-smi...")
	cmd := exec.CommandContext(ctx, "nvidia-smi", "--list-gpus")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	output, err := cmd.Output() // Use Output instead of CombinedOutput if stderr is not needed for success check
	if err != nil {
		// Check if the error is because the command wasn't found or failed execution
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Command ran but returned non-zero exit code. Likely no GPUs found or driver issue.
			slog.Warn("nvidia-smi command finished with non-zero status.", "stderr", string(exitErr.Stderr))
			return false, nil // Treat as "no GPU found" rather than a fatal error
		}
		// Other errors (e.g., command not found)
		return false, fmt.Errorf("failed to execute nvidia-smi: %w", err)
	}

	found := len(output) > 0
	if found {
		slog.Info("Nvidia GPU detected.")
	} else {
		slog.Info("No Nvidia GPU detected by nvidia-smi.")
	}
	return found, nil
}

func captureOutput(wg *sync.WaitGroup, rc io.ReadCloser, streamName string) {
	defer wg.Done()
	defer rc.Close()
	scanner := bufio.NewScanner(rc)
	for scanner.Scan() {
		slog.Info(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		// Don't log EOF errors, they are expected
		if !errors.Is(err, io.EOF) {
			slog.Error("Error reading container output", "stream", streamName, "error", err)
		}
	}
	slog.Debug("Finished capturing output", "stream", streamName)
}
