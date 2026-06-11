package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"rocketchat-message-purger/internal/config"
	"rocketchat-message-purger/internal/purger"
	"rocketchat-message-purger/internal/rocketchat"
)

type Runner func(ctx context.Context, cfg config.Config) (purger.Summary, error)

func Main(ctx context.Context, args []string, env map[string]string, stdout io.Writer, stderr io.Writer, runner Runner) int {
	cfg, err := config.Parse(args, env)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if runner == nil {
		runner = defaultRunner
	}
	if cfg.Verbose {
		cfg.ProgressWriter = stdout
	}

	summary, err := runner(ctx, cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	summary.Verbose = false

	printSummary(summary, stdout, stderr)
	if summary.Failed > 0 {
		return 1
	}
	return 0
}

func EnvFromOS() map[string]string {
	return map[string]string{
		"ROCKETCHAT_URL":        os.Getenv("ROCKETCHAT_URL"),
		"ROCKETCHAT_USER_ID":    os.Getenv("ROCKETCHAT_USER_ID"),
		"ROCKETCHAT_AUTH_TOKEN": os.Getenv("ROCKETCHAT_AUTH_TOKEN"),
	}
}

func defaultRunner(ctx context.Context, cfg config.Config) (purger.Summary, error) {
	client := rocketchat.New(rocketchat.ClientOptions{
		BaseURL:     cfg.BaseURL,
		UserID:      cfg.UserID,
		AuthToken:   cfg.AuthToken,
		Timeout:     time.Duration(cfg.TimeoutMS) * time.Millisecond,
		Debug:       cfg.Debug,
		DebugWriter: os.Stderr,
	})
	return purger.Run(ctx, client, cfg, time.Now())
}

func printSummary(summary purger.Summary, stdout io.Writer, stderr io.Writer) {
	if summary.DryRun {
		fmt.Fprintln(stdout, "DRY RUN")
	} else {
		fmt.Fprintln(stdout, "PURGE RUN")
	}
	fmt.Fprintf(
		stdout,
		"%d rooms discovered, %d skipped, %d succeeded, %d failed\n",
		summary.TotalRooms,
		summary.Skipped,
		summary.Succeeded,
		summary.Failed,
	)
	if summary.MessageMode {
		fmt.Fprintf(
			stdout,
			"%d messages found, %d deleted, %d failed\n",
			summary.MessagesFound,
			summary.MessagesDeleted,
			summary.MessagesFailed,
		)
	}

	for _, result := range summary.Results {
		label := roomLabel(result.Room)
		if summary.MessageMode {
			printMessageResult(result, label, stdout, stderr, summary.Verbose)
			continue
		}
		switch result.Status {
		case purger.StatusFailed:
			fmt.Fprintf(stderr, "[failed] %s: %s\n", label, result.Error)
		case purger.StatusSkipped:
			fmt.Fprintf(stdout, "[skipped] %s: %s\n", label, result.Reason)
		default:
			fmt.Fprintf(stdout, "[%s] %s\n", result.Status, label)
		}
	}
}

func printMessageResult(result purger.Result, label string, stdout io.Writer, stderr io.Writer, verbose bool) {
	switch result.Status {
	case purger.StatusFailed:
		if result.MessagesFailed > 0 {
			fmt.Fprintf(stderr, "[failed] %s: %d deleted, %d failed: %s\n", label, result.MessagesDeleted, result.MessagesFailed, result.Error)
			printVerboseMessages(result, label, stdout, stderr, verbose)
			return
		}
		fmt.Fprintf(stderr, "[failed] %s: %s\n", label, result.Error)
		printVerboseMessages(result, label, stdout, stderr, verbose)
	case purger.StatusSkipped:
		fmt.Fprintf(stdout, "[skipped] %s: %s\n", label, result.Reason)
		printVerboseMessages(result, label, stdout, stderr, verbose)
	case purger.StatusDryRun:
		fmt.Fprintf(stdout, "[dry-run] %s: %d messages%s\n", label, result.MessagesFound, limitNote(result))
		printVerboseMessages(result, label, stdout, stderr, verbose)
	default:
		fmt.Fprintf(stdout, "[%s] %s: %d deleted%s\n", result.Status, label, result.MessagesDeleted, limitNote(result))
		printVerboseMessages(result, label, stdout, stderr, verbose)
	}
}

func limitNote(result purger.Result) string {
	if !result.LimitReached {
		return ""
	}
	return " (stopped at max-messages limit, more of your messages may remain)"
}

func printVerboseMessages(result purger.Result, label string, stdout io.Writer, stderr io.Writer, verbose bool) {
	if verbose {
		for _, id := range result.DeletedMessageIDs {
			fmt.Fprintf(stdout, "[verbose] %s: deleted message %s\n", label, id)
		}
		for _, id := range result.FailedMessageIDs {
			fmt.Fprintf(stderr, "[verbose] %s: failed message %s\n", label, id)
		}
	}
}

func roomLabel(room rocketchat.Room) string {
	displayName := room.FName
	if displayName == "" {
		displayName = room.Name
	}
	if displayName == "" {
		displayName = "unnamed room"
	}
	roomType := roomTypeLabel(room.Type)
	if roomType == "" {
		return fmt.Sprintf("%s (%s)", displayName, room.ID)
	}
	return fmt.Sprintf("%s %s (%s)", roomType, displayName, room.ID)
}

func roomTypeLabel(roomType string) string {
	switch roomType {
	case "c":
		return "channel"
	case "p":
		return "private"
	case "d":
		return "direct"
	case "l":
		return "livechat"
	default:
		return ""
	}
}
