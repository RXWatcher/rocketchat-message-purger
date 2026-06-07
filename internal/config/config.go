package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	BaseURL            string
	UserID             string
	AuthToken          string
	DryRun             bool
	ConfirmPurge       bool
	AllRooms           bool
	TargetRooms        []string
	ExcludeRooms       []string
	ExcludeDMs         bool
	IncludeDiscussions bool
	IncludeThreads     bool
	PreservePinned     bool
	Mode               string
	PageSize           int
	MaxMessages        int
	Verbose            bool
	Debug              bool
	ProgressWriter     io.Writer
	Concurrency        int
	TimeoutMS          int
}

type repeatedStrings []string

func (r *repeatedStrings) String() string {
	return strings.Join(*r, ",")
}

func (r *repeatedStrings) Set(value string) error {
	*r = append(*r, value)
	return nil
}

func Parse(args []string, env map[string]string) (Config, error) {
	var excludes repeatedStrings
	var targets repeatedStrings
	var rawConcurrency string
	var rawTimeout string
	var rawPageSize string
	var rawMaxMessages string
	var mode string
	var configPath string
	var verbose bool
	var debug bool
	var dryRun bool
	var confirmPurge bool
	var allRooms bool
	var excludeDMs bool
	var includeDiscussions bool
	var includeThreads bool
	var preservePinned bool
	var url string
	var userID string
	var token string

	fs := flag.NewFlagSet("rocketchat-message-purger", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&configPath, "config", "", "JSON config file path")
	fs.StringVar(&url, "url", "", "Rocket.Chat base URL")
	fs.StringVar(&userID, "user-id", "", "Rocket.Chat user ID")
	fs.StringVar(&token, "token", "", "Rocket.Chat auth token")
	fs.BoolVar(&dryRun, "dry-run", false, "show what would be purged")
	fs.BoolVar(&confirmPurge, "confirm-purge", false, "actually purge room histories")
	fs.BoolVar(&allRooms, "all", false, "target every accessible room")
	fs.Var(&targets, "room", "room ID, name, or display name to target")
	fs.Var(&excludes, "exclude-room", "room ID, name, or display name to skip")
	fs.BoolVar(&excludeDMs, "exclude-dms", false, "skip direct message rooms")
	fs.BoolVar(&includeDiscussions, "include-discussions", false, "include discussion messages")
	fs.BoolVar(&includeThreads, "include-threads", false, "include thread messages")
	fs.BoolVar(&preservePinned, "preserve-pinned", false, "preserve pinned messages")
	fs.StringVar(&mode, "mode", "", "purge mode: history or messages")
	fs.StringVar(&rawPageSize, "page-size", "100", "message history page size")
	fs.StringVar(&rawMaxMessages, "max-messages", "0", "maximum messages to delete, 0 means no limit")
	fs.BoolVar(&verbose, "verbose", false, "print per-message delete results")
	fs.BoolVar(&debug, "debug", false, "stream chat.delete request and response diagnostics")
	fs.StringVar(&rawConcurrency, "concurrency", "1", "room purge concurrency")
	fs.StringVar(&rawTimeout, "timeout-ms", "30000", "HTTP timeout in milliseconds")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	seen := seenFlags(fs)
	fileConfig, err := readFileConfig(configPath)
	if err != nil {
		return Config{}, err
	}
	if dryRun && confirmPurge {
		return Config{}, fmt.Errorf("Use either --dry-run or --confirm-purge, not both")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(firstNonEmpty(valueIfSeen(seen, "url", url), env["ROCKETCHAT_URL"], fileConfig.URL)), "/")
	targetRooms := fileConfig.Room
	if seen["room"] {
		targetRooms = []string(targets)
	}
	excludeRooms := fileConfig.ExcludeRoom
	if seen["exclude-room"] {
		excludeRooms = []string(excludes)
	}
	cfg := Config{
		BaseURL:            baseURL,
		UserID:             firstNonEmpty(valueIfSeen(seen, "user-id", userID), env["ROCKETCHAT_USER_ID"], fileConfig.UserID),
		AuthToken:          firstNonEmpty(valueIfSeen(seen, "token", token), env["ROCKETCHAT_AUTH_TOKEN"], fileConfig.Token),
		DryRun:             !confirmPurge,
		ConfirmPurge:       confirmPurge,
		AllRooms:           boolValue(seen, "all", allRooms, fileConfig.All),
		TargetRooms:        targetRooms,
		ExcludeRooms:       excludeRooms,
		ExcludeDMs:         boolValue(seen, "exclude-dms", excludeDMs, fileConfig.ExcludeDMs),
		IncludeDiscussions: boolValue(seen, "include-discussions", includeDiscussions, fileConfig.IncludeDiscussions),
		IncludeThreads:     boolValue(seen, "include-threads", includeThreads, fileConfig.IncludeThreads),
		PreservePinned:     boolValue(seen, "preserve-pinned", preservePinned, fileConfig.PreservePinned),
		Mode:               firstNonEmpty(valueIfSeen(seen, "mode", mode), fileConfig.Mode, "history"),
		Verbose:            boolValue(seen, "verbose", verbose, fileConfig.Verbose),
		Debug:              boolValue(seen, "debug", debug, fileConfig.Debug),
	}
	if fileConfig.ConfirmPurge && !seen["confirm-purge"] {
		cfg.ConfirmPurge = true
		cfg.DryRun = false
	}
	if fileConfig.DryRun && !seen["dry-run"] && !cfg.ConfirmPurge {
		cfg.DryRun = true
	}
	if cfg.BaseURL == "" || cfg.UserID == "" || cfg.AuthToken == "" {
		return Config{}, fmt.Errorf("Missing required Rocket.Chat configuration: --url/ROCKETCHAT_URL, --user-id/ROCKETCHAT_USER_ID, and --token/ROCKETCHAT_AUTH_TOKEN are required")
	}
	if cfg.AllRooms && len(cfg.TargetRooms) > 0 {
		return Config{}, fmt.Errorf("Use either --room or --all, not both")
	}
	if !cfg.AllRooms && len(cfg.TargetRooms) == 0 {
		return Config{}, fmt.Errorf("Specify --room for one room or --all for every accessible room")
	}
	if cfg.Mode != "history" && cfg.Mode != "messages" {
		return Config{}, fmt.Errorf("--mode must be either history or messages")
	}

	cfg.Concurrency, err = positiveInt(stringValue(seen, "concurrency", rawConcurrency, fileConfig.Concurrency, "1"), "--concurrency")
	if err != nil {
		return Config{}, err
	}
	cfg.TimeoutMS, err = positiveInt(stringValue(seen, "timeout-ms", rawTimeout, fileConfig.TimeoutMS, "30000"), "--timeout-ms")
	if err != nil {
		return Config{}, err
	}
	cfg.PageSize, err = positiveInt(stringValue(seen, "page-size", rawPageSize, fileConfig.PageSize, "100"), "--page-size")
	if err != nil {
		return Config{}, err
	}
	cfg.MaxMessages, err = zeroOrPositiveInt(stringValue(seen, "max-messages", rawMaxMessages, fileConfig.MaxMessages, "0"), "--max-messages")
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

type fileConfig struct {
	URL                string   `json:"url"`
	UserID             string   `json:"user_id"`
	Token              string   `json:"token"`
	DryRun             bool     `json:"dry_run"`
	ConfirmPurge       bool     `json:"confirm_purge"`
	All                bool     `json:"all"`
	Room               []string `json:"room"`
	ExcludeRoom        []string `json:"exclude_room"`
	ExcludeDMs         bool     `json:"exclude_dms"`
	IncludeDiscussions bool     `json:"include_discussions"`
	IncludeThreads     bool     `json:"include_threads"`
	PreservePinned     bool     `json:"preserve_pinned"`
	Mode               string   `json:"mode"`
	PageSize           int      `json:"page_size"`
	MaxMessages        int      `json:"max_messages"`
	Verbose            bool     `json:"verbose"`
	Debug              bool     `json:"debug"`
	Concurrency        int      `json:"concurrency"`
	TimeoutMS          int      `json:"timeout_ms"`
}

func readFileConfig(path string) (fileConfig, error) {
	if path == "" {
		return fileConfig{}, nil
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return fileConfig{}, err
	}
	var cfg fileConfig
	if err := json.Unmarshal(contents, &cfg); err != nil {
		return fileConfig{}, err
	}
	return cfg, nil
}

func seenFlags(fs *flag.FlagSet) map[string]bool {
	seen := map[string]bool{}
	fs.Visit(func(flag *flag.Flag) {
		seen[flag.Name] = true
	})
	return seen
}

func valueIfSeen(seen map[string]bool, name string, value string) string {
	if seen[name] {
		return value
	}
	return ""
}

func boolValue(seen map[string]bool, name string, flagValue bool, configValue bool) bool {
	if seen[name] {
		return flagValue
	}
	return configValue
}

func stringValue(seen map[string]bool, name string, flagValue string, configValue int, defaultValue string) string {
	if seen[name] {
		return flagValue
	}
	if configValue > 0 {
		return strconv.Itoa(configValue)
	}
	return defaultValue
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func positiveInt(value string, flagName string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", flagName)
	}
	return parsed, nil
}

func zeroOrPositiveInt(value string, flagName string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("%s must be zero or a positive integer", flagName)
	}
	return parsed, nil
}
