package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"rocketchat-message-purger/internal/config"
	"rocketchat-message-purger/internal/purger"
	"rocketchat-message-purger/internal/rocketchat"
)

var cliEnv = map[string]string{
	"ROCKETCHAT_URL":        "https://chat.example.com",
	"ROCKETCHAT_USER_ID":    "user-123",
	"ROCKETCHAT_AUTH_TOKEN": "token-abc",
}

func testSummary(overrides func(*purger.Summary)) purger.Summary {
	summary := purger.Summary{
		TotalRooms: 2,
		Skipped:    0,
		DryRun:     true,
		Succeeded:  0,
		Failed:     0,
		Results: []purger.Result{
			{Room: rocketchat.Room{ID: "r1", Name: "general", Type: "c"}, Status: purger.StatusDryRun},
			{Room: rocketchat.Room{ID: "r2", Name: "random", Type: "p"}, Status: purger.StatusDryRun},
		},
	}
	if overrides != nil {
		overrides(&summary)
	}
	return summary
}

func TestMainPrintsDryRunOutput(t *testing.T) {
	var stdout bytes.Buffer
	var gotConfig config.Config
	exitCode := Main(context.Background(), []string{"--room", "general"}, cliEnv, &stdout, &bytes.Buffer{}, func(ctx context.Context, cfg config.Config) (purger.Summary, error) {
		gotConfig = cfg
		return testSummary(nil), nil
	})

	if exitCode != 0 {
		t.Fatalf("exitCode = %d", exitCode)
	}
	if !gotConfig.DryRun || gotConfig.BaseURL != "https://chat.example.com" {
		t.Fatalf("config = %#v", gotConfig)
	}
	output := stdout.String()
	for _, want := range []string{"DRY RUN", "2 rooms discovered", "channel general"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %s", want, output)
		}
	}
}

func TestMainPrintsConfirmedPurgeOutput(t *testing.T) {
	var stdout bytes.Buffer
	exitCode := Main(context.Background(), []string{"--all", "--confirm-purge"}, cliEnv, &stdout, &bytes.Buffer{}, func(ctx context.Context, cfg config.Config) (purger.Summary, error) {
		return testSummary(func(summary *purger.Summary) {
			summary.DryRun = false
			summary.Succeeded = 2
			summary.Results = []purger.Result{
				{Room: rocketchat.Room{ID: "r1", Name: "general"}, Status: purger.StatusSuccess},
				{Room: rocketchat.Room{ID: "r2", Name: "random"}, Status: purger.StatusSuccess},
			}
		}), nil
	})

	if exitCode != 0 {
		t.Fatalf("exitCode = %d", exitCode)
	}
	output := stdout.String()
	if !strings.Contains(output, "PURGE RUN") || !strings.Contains(output, "2 succeeded") {
		t.Fatalf("output = %s", output)
	}
}

func TestMainPrintsMessageModeOutput(t *testing.T) {
	var stdout bytes.Buffer
	exitCode := Main(context.Background(), []string{"--room", "general", "--mode", "messages"}, cliEnv, &stdout, &bytes.Buffer{}, func(ctx context.Context, cfg config.Config) (purger.Summary, error) {
		return testSummary(func(summary *purger.Summary) {
			summary.MessageMode = true
			summary.MessagesFound = 3
			summary.Results = []purger.Result{
				{
					Room:          rocketchat.Room{ID: "r1", Name: "general", Type: "c"},
					Status:        purger.StatusDryRun,
					MessagesFound: 3,
				},
			}
		}), nil
	})

	if exitCode != 0 {
		t.Fatalf("exitCode = %d", exitCode)
	}
	output := stdout.String()
	if !strings.Contains(output, "3 messages found") || !strings.Contains(output, "[dry-run] channel general (r1): 3 messages") {
		t.Fatalf("output = %s", output)
	}
}

func TestMainWarnsWhenMaxMessagesLimitStoppedScan(t *testing.T) {
	var stdout bytes.Buffer
	exitCode := Main(context.Background(), []string{"--room", "general", "--mode", "messages", "--confirm-purge"}, cliEnv, &stdout, &bytes.Buffer{}, func(ctx context.Context, cfg config.Config) (purger.Summary, error) {
		return testSummary(func(summary *purger.Summary) {
			summary.DryRun = false
			summary.MessageMode = true
			summary.Succeeded = 1
			summary.MessagesFound = 30
			summary.MessagesDeleted = 30
			summary.Results = []purger.Result{
				{
					Room:            rocketchat.Room{ID: "r1", Name: "general", Type: "c"},
					Status:          purger.StatusSuccess,
					MessagesFound:   30,
					MessagesDeleted: 30,
					LimitReached:    true,
				},
			}
		}), nil
	})

	if exitCode != 0 {
		t.Fatalf("exitCode = %d", exitCode)
	}
	output := stdout.String()
	if !strings.Contains(output, "[success] channel general (r1): 30 deleted (stopped at max-messages limit, more of your messages may remain)") {
		t.Fatalf("output missing max-messages warning: %s", output)
	}
}

func TestMainWarnsWhenMaxMessagesLimitStoppedDryRunScan(t *testing.T) {
	var stdout bytes.Buffer
	exitCode := Main(context.Background(), []string{"--room", "general", "--mode", "messages"}, cliEnv, &stdout, &bytes.Buffer{}, func(ctx context.Context, cfg config.Config) (purger.Summary, error) {
		return testSummary(func(summary *purger.Summary) {
			summary.MessageMode = true
			summary.MessagesFound = 30
			summary.Results = []purger.Result{
				{
					Room:          rocketchat.Room{ID: "r1", Name: "general", Type: "c"},
					Status:        purger.StatusDryRun,
					MessagesFound: 30,
					LimitReached:  true,
				},
			}
		}), nil
	})

	if exitCode != 0 {
		t.Fatalf("exitCode = %d", exitCode)
	}
	output := stdout.String()
	if !strings.Contains(output, "[dry-run] channel general (r1): 30 messages (stopped at max-messages limit, more of your messages may remain)") {
		t.Fatalf("output missing max-messages warning: %s", output)
	}
}

func TestMainPrintsVerboseMessageDetails(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Main(context.Background(), []string{"--room", "general", "--mode", "messages", "--verbose"}, cliEnv, &stdout, &stderr, func(ctx context.Context, cfg config.Config) (purger.Summary, error) {
		if !cfg.Verbose {
			t.Fatal("Verbose = false")
		}
		if cfg.ProgressWriter == nil {
			t.Fatal("ProgressWriter = nil")
		}
		fmt.Fprintln(cfg.ProgressWriter, "[verbose] channel general (r1): deleting message m1")
		fmt.Fprintln(cfg.ProgressWriter, "[verbose] channel general (r1): deleted message m1")
		fmt.Fprintln(cfg.ProgressWriter, "[verbose] channel general (r1): deleting message m2")
		fmt.Fprintln(cfg.ProgressWriter, "[verbose] channel general (r1): failed message m2: delete denied")
		return testSummary(func(summary *purger.Summary) {
			summary.MessageMode = true
			summary.MessagesFound = 2
			summary.MessagesDeleted = 1
			summary.MessagesFailed = 1
			summary.Failed = 1
			summary.Results = []purger.Result{
				{
					Room:              rocketchat.Room{ID: "r1", Name: "general", Type: "c"},
					Status:            purger.StatusFailed,
					MessagesFound:     2,
					MessagesDeleted:   1,
					MessagesFailed:    1,
					DeletedMessageIDs: []string{"m1"},
					FailedMessageIDs:  []string{"m2"},
					Error:             "delete denied",
				},
			}
		}), nil
	})

	if exitCode != 1 {
		t.Fatalf("exitCode = %d", exitCode)
	}
	if !strings.Contains(stdout.String(), "deleting message m1") || !strings.Contains(stdout.String(), "deleted message m1") {
		t.Fatalf("stdout = %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "deleting message m2") || !strings.Contains(stdout.String(), "failed message m2: delete denied") {
		t.Fatalf("stdout = %s", stdout.String())
	}
	if strings.Contains(stderr.String(), "failed message m2") {
		t.Fatalf("stderr should not contain streamed verbose output: %s", stderr.String())
	}
}

func TestMainReturnsFailureForFailedRooms(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := Main(context.Background(), []string{"--all", "--confirm-purge"}, cliEnv, &bytes.Buffer{}, &stderr, func(ctx context.Context, cfg config.Config) (purger.Summary, error) {
		return testSummary(func(summary *purger.Summary) {
			summary.DryRun = false
			summary.Succeeded = 1
			summary.Failed = 1
			summary.Results = []purger.Result{
				{Room: rocketchat.Room{ID: "r1", Name: "general"}, Status: purger.StatusSuccess},
				{Room: rocketchat.Room{ID: "r2", Name: "random"}, Status: purger.StatusFailed, Error: "permission denied"},
			}
		}), nil
	})

	if exitCode != 1 {
		t.Fatalf("exitCode = %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "permission denied") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}

func TestMainReturnsUsageFailureForConfigErrors(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := Main(context.Background(), nil, map[string]string{}, &bytes.Buffer{}, &stderr, func(ctx context.Context, cfg config.Config) (purger.Summary, error) {
		t.Fatal("runner should not be called")
		return purger.Summary{}, nil
	})

	if exitCode != 2 {
		t.Fatalf("exitCode = %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "Missing required Rocket.Chat configuration") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}
