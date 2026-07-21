package lifecycle

// Central model assignment poller (Supabase grid_nodes).
//
// Pairing: the user mints a single-use claim secret in AgentOS (Admin →
// Grid Nodes → "Pair a node") and puts grid_user_id + grid_pairing_secret
// into config.json. On the next poll the tray exchanges the secret for a
// Supabase session via claim_installer_session, persists the refresh token,
// and clears the secret. Every poll then calls upsert_grid_node — which
// reports this node's state and returns the desired assignment in the same
// round trip. Assignment changes are persisted to config.json and the
// container is restarted to apply them.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	assignmentPollInterval = 5 * time.Minute
	assignmentInitialDelay = 30 * time.Second
	assignmentHTTPTimeout  = 20 * time.Second
)

type gridAssignment struct {
	AssignedModel       *string `json:"assigned_model"`
	AssignedMode        *string `json:"assigned_mode"`
	AssignedQuant       *string `json:"assigned_quant"`
	AssignmentUpdatedAt *string `json:"assignment_updated_at"`
}

var (
	gridSessionMu        sync.Mutex
	gridAccessToken      string
	gridAccessExpiresAt  time.Time
	assignmentHTTPClient = &http.Client{Timeout: assignmentHTTPTimeout}
)

// StartAssignmentPoller launches the background assignment loop. It is a
// no-op for unpaired installs (no grid_user_id in config.json).
func StartAssignmentPoller(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(assignmentInitialDelay):
		}
		pollAssignment()
		ticker := time.NewTicker(assignmentPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pollAssignment()
			}
		}
	}()
}

func pollAssignment() {
	cfg, err := LoadConfig()
	if err != nil {
		slog.Debug("Assignment poll skipped: config not loadable", "error", err)
		return
	}
	if !cfg.FollowAssignment || cfg.SupabaseURL == "" || cfg.SupabaseAnonKey == "" || cfg.GridUserID == "" {
		return
	}

	if cfg.GridPairingSecret != "" {
		if err := claimGridSession(&cfg); err != nil {
			slog.Warn("Grid pairing failed", "error", err)
			return
		}
	}
	if cfg.GridRefreshToken == "" {
		slog.Debug("Assignment poll skipped: not paired (no refresh token)")
		return
	}

	access, err := ensureGridAccessToken(&cfg)
	if err != nil {
		slog.Warn("Assignment poll: token refresh failed", "error", err)
		return
	}

	hostname, _ := os.Hostname()
	deviceID := cfg.GridDeviceID
	if deviceID == "" {
		deviceID = hostname
	}
	var gpuModel *string
	if cfg.UseGPU {
		g := "NVIDIA (CDI)"
		gpuModel = &g
	}

	payload := map[string]any{
		"p_user_id":        cfg.GridUserID,
		"p_device_id":      deviceID,
		"p_platform":       "windows",
		"p_hostname":       hostname,
		"p_gpu_model":      gpuModel,
		"p_gpu_vram_mb":    nil,
		"p_node_version":   agentGridVersion,
		"p_reported_model": cfg.ModelName,
		"p_reported_mode":  cfg.InferenceMode,
	}
	var rows []gridAssignment
	err = supabasePost(cfg.SupabaseURL+"/rest/v1/rpc/upsert_grid_node", cfg.SupabaseAnonKey, access, payload, &rows)
	if err != nil {
		slog.Warn("Assignment poll failed", "error", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	applyAssignment(cfg, rows[0])
}

// claimGridSession exchanges the one-shot pairing secret for a session and
// persists the refresh token (clearing the secret) in config.json.
func claimGridSession(cfg *AppConfig) error {
	var rows []struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	err := supabasePost(
		cfg.SupabaseURL+"/rest/v1/rpc/claim_installer_session",
		cfg.SupabaseAnonKey, cfg.SupabaseAnonKey,
		map[string]any{"p_user_id": cfg.GridUserID, "p_claim_secret": cfg.GridPairingSecret},
		&rows,
	)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("no relay session found — log into AgentOS and mint a new pairing secret")
	}
	if err := saveConfigValues(map[string]any{
		"grid_refresh_token":  rows[0].RefreshToken,
		"grid_pairing_secret": "",
	}); err != nil {
		return fmt.Errorf("failed to persist pairing: %w", err)
	}
	cfg.GridRefreshToken = rows[0].RefreshToken
	cfg.GridPairingSecret = ""
	gridSessionMu.Lock()
	gridAccessToken = rows[0].AccessToken
	gridAccessExpiresAt = time.Now().Add(45 * time.Minute)
	gridSessionMu.Unlock()
	slog.Info("Grid node paired", "user_id", cfg.GridUserID)
	return nil
}

func ensureGridAccessToken(cfg *AppConfig) (string, error) {
	gridSessionMu.Lock()
	if gridAccessToken != "" && time.Now().Before(gridAccessExpiresAt) {
		token := gridAccessToken
		gridSessionMu.Unlock()
		return token, nil
	}
	gridSessionMu.Unlock()

	var resp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	err := supabasePost(
		cfg.SupabaseURL+"/auth/v1/token?grant_type=refresh_token",
		cfg.SupabaseAnonKey, "",
		map[string]any{"refresh_token": cfg.GridRefreshToken},
		&resp,
	)
	if err != nil {
		return "", err
	}
	// Supabase rotates refresh tokens — persist immediately or the next
	// refresh fails.
	if resp.RefreshToken != "" && resp.RefreshToken != cfg.GridRefreshToken {
		if err := saveConfigValues(map[string]any{"grid_refresh_token": resp.RefreshToken}); err != nil {
			slog.Warn("Failed to persist rotated refresh token", "error", err)
		} else {
			cfg.GridRefreshToken = resp.RefreshToken
		}
	}
	expires := time.Duration(resp.ExpiresIn) * time.Second
	if expires <= 0 {
		expires = time.Hour
	}
	gridSessionMu.Lock()
	gridAccessToken = resp.AccessToken
	gridAccessExpiresAt = time.Now().Add(expires - 15*time.Minute)
	gridSessionMu.Unlock()
	return resp.AccessToken, nil
}

// applyAssignment persists any changed assignment fields and restarts a
// running container so they take effect (same flow as handleModeChange).
func applyAssignment(cfg AppConfig, a gridAssignment) {
	values := map[string]any{}
	modeChanged := false
	if a.AssignedModel != nil && *a.AssignedModel != "" && *a.AssignedModel != cfg.ModelName {
		values["model_name"] = *a.AssignedModel
	}
	if a.AssignedMode != nil &&
		(*a.AssignedMode == InferenceModeDistributed || *a.AssignedMode == InferenceModePrivate) &&
		*a.AssignedMode != cfg.InferenceMode {
		values["inference_mode"] = *a.AssignedMode
		modeChanged = true
	}
	if a.AssignedQuant != nil && *a.AssignedQuant != "" && *a.AssignedQuant != cfg.QuantType {
		values["quant_type"] = *a.AssignedQuant
	}
	if len(values) == 0 {
		return
	}
	slog.Info("Applying central assignment", "changes", values)
	if err := saveConfigValues(values); err != nil {
		slog.Error("Failed to persist central assignment", "error", err)
		return
	}
	if modeChanged {
		if err := t.SetInferenceMode(values["inference_mode"].(string)); err != nil {
			slog.Warn("Failed to update inference mode menu state", "error", err)
		}
	}

	stateMu.Lock()
	running := currentState == StateRunning || currentState == StateStarting
	stateMu.Unlock()
	if running {
		slog.Info("Restarting container to apply central assignment")
		handleStopRequest()
		handleStartRequest()
	}
}

// supabasePost does a JSON POST with Supabase apikey/Authorization headers
// and decodes the JSON response into out (skipped when out is nil).
func supabasePost(url, anonKey, bearer string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", anonKey)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := assignmentHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := string(data)
		if len(detail) > 300 {
			detail = detail[:300]
		}
		return fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, req.URL.Path, detail)
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}
