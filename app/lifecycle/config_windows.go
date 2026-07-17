package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/danieljoos/wincred"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// AppConfig struct holds values loaded from config.json and Windows Credential Manager.
type AppConfig struct {
	ContainerName   string `json:"container_name"`
	ContainerImage  string `json:"container_image"`
	InitialPeers    string `json:"initial_peers"`
	ModelName       string `json:"model_name"`
	DefaultPort     uint64 `json:"default_port"`
	UseGPU          bool   `json:"use_gpu"`
	InferenceMode   string `json:"inference_mode"` // "distributed" (default) or "private"
	// Optional agentgrid-api image. When set and InferenceMode is "private",
	// the tray runs this API container (full model in-process, chat endpoint
	// on localhost) instead of the block-server container.
	APIContainerImage string `json:"api_container_image"`
	APIPort           uint64 `json:"api_port"` // localhost port for the private chat API (default 5000)
	SupabaseURL     string `json:"supabaseUrl"`
	SupabaseAnonKey string `json:"supabaseAnonKey"`
	// Server quantization for distributed mode ("nf4" when empty).
	QuantType string `json:"quant_type"`
	// Central model assignment (Supabase grid_nodes). Pair by setting
	// grid_user_id + grid_pairing_secret (minted in AgentOS); the poller
	// swaps the secret for grid_refresh_token on first contact.
	GridUserID        string `json:"grid_user_id"`
	GridDeviceID      string `json:"grid_device_id"`
	GridPairingSecret string `json:"grid_pairing_secret"`
	GridRefreshToken  string `json:"grid_refresh_token"`
	FollowAssignment  bool   `json:"follow_assignment"`
	Token           string // Loaded separately from Credential Manager
}

var (
	Port uint64
)

const (
	configDirName     = "ReEnvisionAI"
	configFileName    = "config.json"
	registryKeyPath   = `SOFTWARE\ReEnvisionAI\ReEnvisionAI`
	registryPortValue = "Port"

	// Inference modes: how the local AgentGrid node participates.
	//   distributed — joins the public swarm and serves blocks to everyone.
	//   private     — isolated swarm; the full model runs for this machine
	//                 only and no inference traffic leaves the box.
	InferenceModeDistributed = "distributed"
	InferenceModePrivate     = "private"
)

// SaveInferenceMode persists the chosen inference mode back to config.json so
// it survives restarts.
func SaveInferenceMode(mode string) error {
	if mode != InferenceModeDistributed && mode != InferenceModePrivate {
		return fmt.Errorf("invalid inference mode %q", mode)
	}
	if err := saveConfigValues(map[string]any{"inference_mode": mode}); err != nil {
		return err
	}
	slog.Info("Inference mode saved", "mode", mode)
	return nil
}

// saveConfigValues merges the given values into config.json. The file is
// read/written as a generic map to preserve any fields this app version
// doesn't know about; the HF token is never written (it lives in Windows
// Credential Manager).
func saveConfigValues(values map[string]any) error {
	configFile, err := configFilePath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to read config file %q: %w", configFile, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("failed to parse config file %q: %w", configFile, err)
	}
	for k, v := range values {
		raw[k] = v
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode config: %w", err)
	}
	if err := os.WriteFile(configFile, out, 0640); err != nil {
		return fmt.Errorf("failed to write config file %q: %w", configFile, err)
	}
	return nil
}

func configFilePath() (string, error) {
	configDir, err := os.UserCacheDir()
	if err != nil {
		slog.Warn("Failed to get user cache directory, falling back to working directory", "error", err)
		configDir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine config directory: %w", err)
		}
	} else {
		configDir = filepath.Join(configDir, configDirName)
		if err := os.MkdirAll(configDir, 0750); err != nil {
			return "", fmt.Errorf("failed to create config directory %q: %w", configDir, err)
		}
	}
	return filepath.Join(configDir, configFileName), nil
}

func LoadConfig() (AppConfig, error) {
	configFile, err := configFilePath()
	if err != nil {
		return AppConfig{}, err
	}
	slog.Info("Using configuration file", "path", configFile)

	appConfig, err := loadAppConfig(configFile)
	if err != nil {
		return AppConfig{}, fmt.Errorf("failed to load configuration from %q: %w", configFile, err)
	}

	// Set default port initially from config
	Port = appConfig.DefaultPort
	slog.Info("Default port set from config", "port", Port)

	loadPortFromRegistry()

	return appConfig, nil
}

func loadPortFromRegistry() {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, registryKeyPath, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			slog.Info("Registry key not found, using default/config port", "key", registryKeyPath, "port", Port)
		} else {
			slog.Warn("Failed to open registry key, using default/config port", "key", registryKeyPath, "error", err)
		}
		return // Use port already set from config
	}
	defer key.Close()

	regPort, _, err := key.GetIntegerValue(registryPortValue)
	if err != nil {
		slog.Warn("Failed to read port value from registry, using default/config port", "value", registryPortValue, "error", err)
		return // Use port already set from config
	}

	Port = regPort // Override with registry value
	slog.Info("Port loaded from registry", "port", Port)
}

func loadAppConfig(filePath string) (AppConfig, error) {
	var cfg AppConfig

	// --- Load from JSON file ---
	data, err := os.ReadFile(filePath)
	if err != nil {
		return cfg, fmt.Errorf("failed to read config file '%s': %w", filePath, err)
	}

	err = json.Unmarshal(data, &cfg)
	if err != nil {
		return cfg, fmt.Errorf("failed to parse config file '%s': %w", filePath, err)
	}

	// --- Validate required fields from JSON ---
	if cfg.ContainerName == "" || cfg.ContainerImage == "" || cfg.ModelName == "" {
		return cfg, fmt.Errorf("config file '%s' is missing required fields (container_name, container_image, model_name)", filePath)
	}

	if cfg.DefaultPort == 0 {
		slog.Warn("DefaultPort is zero in config, using fallback 31330", "filePath", filePath)
		cfg.DefaultPort = 31330 // Provide a default fallback
	}

	switch cfg.InferenceMode {
	case InferenceModeDistributed, InferenceModePrivate:
		// valid, keep as-is
	case "":
		cfg.InferenceMode = InferenceModeDistributed
	default:
		slog.Warn("Unknown inference_mode in config, falling back to distributed", "value", cfg.InferenceMode)
		cfg.InferenceMode = InferenceModeDistributed
	}

	if cfg.APIPort == 0 {
		cfg.APIPort = 5000
	}

	// --- Load Token from Windows Credential Manager ---
	targetName := "ReEnvisionAI/hf_token" // The target name used in Credential Manager

	cred, err := wincred.GetGenericCredential(targetName)
	if err != nil {
		// Check if the error specifically means the credential wasn't found
		if errors.Is(err, wincred.ErrElementNotFound) {
			// Return a specific error indicating the credential is missing
			return cfg, fmt.Errorf("credential '%s' not found in Windows Credential Manager. Please ensure it has been added: %w", targetName, err)
		}
		// Return other potential errors (e.g., access permissions)
		return cfg, fmt.Errorf("error retrieving credential '%s': %w", targetName, err)
	}

	// Decode the token from UTF-16LE (as stored by Windows) to UTF-8
	apiTokenBytesUTF16LE := cred.CredentialBlob
	utf16leDecoder := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder()

	apiTokenBytesUTF8, _, err := transform.Bytes(utf16leDecoder, apiTokenBytesUTF16LE)
	if err != nil {
		// Handle potential decoding errors
		return cfg, fmt.Errorf("error decoding token from UTF-16LE to UTF-8: %w", err)
	}

	cfg.Token = string(apiTokenBytesUTF8)
	slog.Debug("Successfully loaded and decoded token")

	return cfg, nil
}
