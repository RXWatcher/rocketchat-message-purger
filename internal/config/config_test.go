package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testEnv = map[string]string{
	"ROCKETCHAT_URL":        "https://chat.example.com",
	"ROCKETCHAT_USER_ID":    "user-123",
	"ROCKETCHAT_AUTH_TOKEN": "token-abc",
}

func TestParseReadsEnvAndDefaultsToDryRun(t *testing.T) {
	cfg, err := Parse([]string{"--room", "general"}, testEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.BaseURL != "https://chat.example.com" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.UserID != "user-123" {
		t.Fatalf("UserID = %q", cfg.UserID)
	}
	if cfg.AuthToken != "token-abc" {
		t.Fatalf("AuthToken = %q", cfg.AuthToken)
	}
	if !cfg.DryRun || cfg.ConfirmPurge {
		t.Fatalf("DryRun/ConfirmPurge = %v/%v", cfg.DryRun, cfg.ConfirmPurge)
	}
	if len(cfg.ExcludeRooms) != 0 {
		t.Fatalf("ExcludeRooms = %#v", cfg.ExcludeRooms)
	}
	if cfg.AllRooms {
		t.Fatalf("AllRooms = true")
	}
	if strings.Join(cfg.TargetRooms, ",") != "general" {
		t.Fatalf("TargetRooms = %#v", cfg.TargetRooms)
	}
	if cfg.IncludeDiscussions || cfg.IncludeThreads || cfg.PreservePinned {
		t.Fatalf("cleanup booleans were not false by default")
	}
	if cfg.Concurrency != 1 {
		t.Fatalf("Concurrency = %d", cfg.Concurrency)
	}
	if cfg.TimeoutMS != 30000 {
		t.Fatalf("TimeoutMS = %d", cfg.TimeoutMS)
	}
	if cfg.Mode != "history" {
		t.Fatalf("Mode = %q", cfg.Mode)
	}
	if cfg.PageSize != 100 {
		t.Fatalf("PageSize = %d", cfg.PageSize)
	}
	if cfg.MaxMessages != 0 {
		t.Fatalf("MaxMessages = %d", cfg.MaxMessages)
	}
	if cfg.Verbose {
		t.Fatalf("Verbose = true")
	}
	if cfg.Debug {
		t.Fatalf("Debug = true")
	}
}

func TestParseLetsFlagsOverrideEnv(t *testing.T) {
	cfg, err := Parse([]string{
		"--room", "general",
		"--url", "https://override.example.com/",
		"--user-id", "flag-user",
		"--token", "flag-token",
	}, testEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.BaseURL != "https://override.example.com" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.UserID != "flag-user" {
		t.Fatalf("UserID = %q", cfg.UserID)
	}
	if cfg.AuthToken != "flag-token" {
		t.Fatalf("AuthToken = %q", cfg.AuthToken)
	}
}

func TestParseSelectionAndCleanupOptions(t *testing.T) {
	cfg, err := Parse([]string{
		"--all",
		"--exclude-room", "general",
		"--exclude-room", "room-id-2",
		"--exclude-dms",
		"--include-discussions",
		"--include-threads",
		"--preserve-pinned",
		"--concurrency", "3",
		"--timeout-ms", "45000",
		"--mode", "messages",
		"--page-size", "25",
		"--max-messages", "10",
		"--verbose",
		"--debug",
	}, testEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	wantRooms := []string{"general", "room-id-2"}
	if strings.Join(cfg.ExcludeRooms, ",") != strings.Join(wantRooms, ",") {
		t.Fatalf("ExcludeRooms = %#v", cfg.ExcludeRooms)
	}
	if !cfg.ExcludeDMs {
		t.Fatalf("ExcludeDMs = false")
	}
	if !cfg.IncludeDiscussions || !cfg.IncludeThreads || !cfg.PreservePinned {
		t.Fatalf("cleanup booleans not parsed")
	}
	if cfg.Concurrency != 3 {
		t.Fatalf("Concurrency = %d", cfg.Concurrency)
	}
	if cfg.TimeoutMS != 45000 {
		t.Fatalf("TimeoutMS = %d", cfg.TimeoutMS)
	}
	if cfg.Mode != "messages" || cfg.PageSize != 25 || cfg.MaxMessages != 10 {
		t.Fatalf("message options = %#v", cfg)
	}
	if !cfg.Verbose {
		t.Fatalf("Verbose = false")
	}
	if !cfg.Debug {
		t.Fatalf("Debug = false")
	}
}

func TestParseConfirmPurgeDisablesDryRun(t *testing.T) {
	cfg, err := Parse([]string{"--all", "--confirm-purge"}, testEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.DryRun || !cfg.ConfirmPurge {
		t.Fatalf("DryRun/ConfirmPurge = %v/%v", cfg.DryRun, cfg.ConfirmPurge)
	}
}

func TestParseRejectsMissingCredentials(t *testing.T) {
	_, err := Parse([]string{"--all"}, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "Missing required Rocket.Chat configuration") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseRejectsConflictingSafetyFlags(t *testing.T) {
	_, err := Parse([]string{"--dry-run", "--confirm-purge"}, testEnv)
	if err == nil || !strings.Contains(err.Error(), "Use either --dry-run or --confirm-purge, not both") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseRejectsNonPositiveNumericOptions(t *testing.T) {
	_, err := Parse([]string{"--all", "--concurrency", "0"}, testEnv)
	if err == nil || !strings.Contains(err.Error(), "--concurrency must be a positive integer") {
		t.Fatalf("concurrency err = %v", err)
	}

	_, err = Parse([]string{"--all", "--timeout-ms", "-5"}, testEnv)
	if err == nil || !strings.Contains(err.Error(), "--timeout-ms must be a positive integer") {
		t.Fatalf("timeout err = %v", err)
	}

	_, err = Parse([]string{"--all", "--page-size", "0"}, testEnv)
	if err == nil || !strings.Contains(err.Error(), "--page-size must be a positive integer") {
		t.Fatalf("page size err = %v", err)
	}

	_, err = Parse([]string{"--all", "--max-messages", "-1"}, testEnv)
	if err == nil || !strings.Contains(err.Error(), "--max-messages must be zero or a positive integer") {
		t.Fatalf("max messages err = %v", err)
	}
}

func TestParseRejectsInvalidMode(t *testing.T) {
	_, err := Parse([]string{"--all", "--mode", "other"}, testEnv)
	if err == nil || !strings.Contains(err.Error(), "--mode must be either history or messages") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseRequiresRoomOrAll(t *testing.T) {
	_, err := Parse(nil, testEnv)
	if err == nil || !strings.Contains(err.Error(), "Specify --room for one room or --all for every accessible room") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseRejectsRoomAndAllTogether(t *testing.T) {
	_, err := Parse([]string{"--all", "--room", "general"}, testEnv)
	if err == nil || !strings.Contains(err.Error(), "Use either --room or --all, not both") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseAllowsMultipleTargetRooms(t *testing.T) {
	cfg, err := Parse([]string{"--room", "general", "--room", "random"}, testEnv)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if strings.Join(cfg.TargetRooms, ",") != "general,random" {
		t.Fatalf("TargetRooms = %#v", cfg.TargetRooms)
	}
}

func TestParseReadsJSONConfigFile(t *testing.T) {
	path := writeConfigFile(t, `{
  "url": "https://file.example.com",
  "user_id": "file-user",
  "token": "file-token",
  "room": ["general"],
  "exclude_room": ["announcements"],
  "exclude_dms": true,
  "include_threads": true,
  "preserve_pinned": true,
  "mode": "messages",
  "page_size": 50,
  "max_messages": 12,
  "verbose": true,
  "debug": true,
  "concurrency": 4,
  "timeout_ms": 60000
}`)

	cfg, err := Parse([]string{"--config", path}, map[string]string{})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.BaseURL != "https://file.example.com" || cfg.UserID != "file-user" || cfg.AuthToken != "file-token" {
		t.Fatalf("credentials = %#v", cfg)
	}
	if strings.Join(cfg.TargetRooms, ",") != "general" {
		t.Fatalf("TargetRooms = %#v", cfg.TargetRooms)
	}
	if strings.Join(cfg.ExcludeRooms, ",") != "announcements" {
		t.Fatalf("ExcludeRooms = %#v", cfg.ExcludeRooms)
	}
	if !cfg.ExcludeDMs {
		t.Fatalf("ExcludeDMs = false")
	}
	if !cfg.IncludeThreads || !cfg.PreservePinned || cfg.Concurrency != 4 || cfg.TimeoutMS != 60000 {
		t.Fatalf("cfg = %#v", cfg)
	}
	if cfg.Mode != "messages" || cfg.PageSize != 50 || cfg.MaxMessages != 12 {
		t.Fatalf("message options = %#v", cfg)
	}
	if !cfg.Verbose {
		t.Fatalf("Verbose = false")
	}
	if !cfg.Debug {
		t.Fatalf("Debug = false")
	}
}

func TestParseFlagsAndEnvOverrideConfigFile(t *testing.T) {
	path := writeConfigFile(t, `{
  "url": "https://file.example.com",
  "user_id": "file-user",
  "token": "file-token",
  "room": ["general"],
  "concurrency": 4
}`)

	cfg, err := Parse([]string{
		"--config", path,
		"--room", "random",
		"--concurrency", "2",
	}, map[string]string{
		"ROCKETCHAT_URL":        "https://env.example.com",
		"ROCKETCHAT_USER_ID":    "env-user",
		"ROCKETCHAT_AUTH_TOKEN": "env-token",
	})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.BaseURL != "https://env.example.com" || cfg.UserID != "env-user" || cfg.AuthToken != "env-token" {
		t.Fatalf("credentials = %#v", cfg)
	}
	if strings.Join(cfg.TargetRooms, ",") != "random" {
		t.Fatalf("TargetRooms = %#v", cfg.TargetRooms)
	}
	if cfg.Concurrency != 2 {
		t.Fatalf("Concurrency = %d", cfg.Concurrency)
	}
}

func writeConfigFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "purger.json")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}
