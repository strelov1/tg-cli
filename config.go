package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ── File config ──

// fileConfig is the persisted config file at ~/.tg-cli/config.json.
type fileConfig struct {
	AppID          string `json:"app_id,omitempty"`
	APIHash        string `json:"api_hash,omitempty"`
	SessionDir     string `json:"session_dir,omitempty"`
	DefaultAccount string `json:"default_account,omitempty"`
}

var configKeys = []string{"app-id", "api-hash", "default-account", "session-dir"}

func getFileConfigField(fc *fileConfig, key string) *string {
	switch key {
	case "app-id":
		return &fc.AppID
	case "api-hash":
		return &fc.APIHash
	case "session-dir":
		return &fc.SessionDir
	case "default-account":
		return &fc.DefaultAccount
	}
	return nil
}

func defaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".tg-cli", "config.json"), nil
}

func readFileConfig() fileConfig {
	path, err := defaultConfigPath()
	if err != nil {
		return fileConfig{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileConfig{}
	}
	var fc fileConfig
	if err := json.Unmarshal(data, &fc); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: corrupt config file %s: %v\n", path, err)
	}
	return fc
}

func writeFileConfig(fc fileConfig) error {
	path, err := defaultConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(fc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

func configEnvFor(key string) string {
	switch key {
	case "app-id":
		return os.Getenv("TG_APP_ID")
	case "api-hash":
		return os.Getenv("TG_API_HASH")
	case "session-dir":
		return os.Getenv("SESSION_DIR")
	case "default-account":
		return os.Getenv("ACCOUNT")
	}
	return ""
}

// ── Runtime config ──

// config is the runtime configuration used by all commands.
type config struct {
	appID   string // numeric string, e.g. "12345"
	apiHash string
	baseDir string // ~/.tg-cli
	account string // selected phone number
	timeout time.Duration
}

func (c config) sessionsDir() string { return filepath.Join(c.baseDir, "sessions") }
func (c config) accountDir() string  { return filepath.Join(c.sessionsDir(), c.account) }
func (c config) sessFile() string    { return filepath.Join(c.accountDir(), "session.json") }

func (c config) hasSess() bool {
	if c.account == "" {
		return false
	}
	_, err := os.Stat(c.sessFile())
	return err == nil
}

func (c config) appIDInt() (int, error) {
	n, err := strconv.Atoi(c.appID)
	if err != nil {
		return 0, fmt.Errorf("invalid app-id %q: must be a number", c.appID)
	}
	return n, nil
}

func resolveBaseDir() (string, error) {
	fc := readFileConfig()
	dir := firstNonEmpty(os.Getenv("SESSION_DIR"), fc.SessionDir)
	if dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".tg-cli"), nil
}

func loadConfig(globalAccount string, timeout time.Duration) (config, error) {
	fc := readFileConfig()

	appID := firstNonEmpty(os.Getenv("TG_APP_ID"), fc.AppID)
	apiHash := firstNonEmpty(os.Getenv("TG_API_HASH"), fc.APIHash)

	if appID == "" || apiHash == "" {
		path, _ := defaultConfigPath()
		return config{}, fmt.Errorf(
			"app-id and api-hash are required\n\nRun:\n  tg-cli config set app-id <id>\n  tg-cli config set api-hash <hash>\n\nConfig file: %s",
			path,
		)
	}

	baseDir, err := resolveBaseDir()
	if err != nil {
		return config{}, err
	}

	account := firstNonEmpty(globalAccount, os.Getenv("ACCOUNT"), fc.DefaultAccount)
	if account == "" {
		phones, _ := listAccounts(filepath.Join(baseDir, "sessions"))
		switch len(phones) {
		case 0:
		case 1:
			account = phones[0]
		default:
			return config{}, fmt.Errorf(
				"multiple accounts — use --account <phone> or set a default:\n  tg-cli accounts use <phone>\n\nAccounts:\n  %s",
				strings.Join(phones, "\n  "),
			)
		}
	}

	return config{appID: appID, apiHash: apiHash, baseDir: baseDir, account: account, timeout: timeout}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ── config command ──

func cmdConfig(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: tg-cli config <set|get|list>")
	}
	sub := args[0]
	rest := args[1:]

	switch sub {
	case "set":
		if len(rest) < 2 {
			return fmt.Errorf("usage: tg-cli config set <key> <value>\nKeys: %s", strings.Join(configKeys, ", "))
		}
		key, val := rest[0], strings.Join(rest[1:], " ")
		fc := readFileConfig()
		field := getFileConfigField(&fc, key)
		if field == nil {
			return fmt.Errorf("unknown key %q — valid keys: %s", key, strings.Join(configKeys, ", "))
		}
		*field = val
		if err := writeFileConfig(fc); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		path, _ := defaultConfigPath()
		display := val
		if key == "api-hash" && len(display) > 6 {
			display = display[:6] + strings.Repeat("*", len(display)-6)
		}
		fmt.Fprintf(os.Stderr, "%s = %s\nSaved → %s\n", key, display, path)
		return nil

	case "get":
		if len(rest) == 0 {
			return fmt.Errorf("usage: tg-cli config get <key>")
		}
		key := rest[0]
		fc := readFileConfig()
		field := getFileConfigField(&fc, key)
		if field == nil {
			return fmt.Errorf("unknown key %q — valid keys: %s", key, strings.Join(configKeys, ", "))
		}
		if envVal := configEnvFor(key); envVal != "" {
			fmt.Println(envVal)
			fmt.Fprintln(os.Stderr, "(from environment variable)")
		} else if *field != "" {
			fmt.Println(*field)
		} else {
			fmt.Fprintln(os.Stderr, "(not set)")
		}
		return nil

	case "list":
		fc := readFileConfig()
		path, _ := defaultConfigPath()
		fmt.Fprintf(os.Stderr, "Config: %s\n\n", path)
		type row struct{ key, file, env string }
		rows := []row{
			{"app-id", fc.AppID, os.Getenv("TG_APP_ID")},
			{"api-hash", fc.APIHash, os.Getenv("TG_API_HASH")},
			{"default-account", fc.DefaultAccount, os.Getenv("ACCOUNT")},
			{"session-dir", fc.SessionDir, os.Getenv("SESSION_DIR")},
		}
		for _, r := range rows {
			effective, src := r.file, "config"
			if r.env != "" {
				effective, src = r.env, "env"
			}
			display := effective
			if r.key == "api-hash" && len(display) > 6 {
				display = display[:6] + strings.Repeat("*", len(display)-6)
			}
			if effective == "" {
				fmt.Printf("%-16s = (not set)\n", r.key)
			} else {
				fmt.Printf("%-16s = %s  [%s]\n", r.key, display, src)
			}
		}
		return nil

	default:
		return fmt.Errorf("unknown subcommand %q — use: set, get, list", sub)
	}
}
