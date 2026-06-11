package purger

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"rocketchat-message-purger/internal/config"
	"rocketchat-message-purger/internal/rocketchat"
)

const oldestMessageDate = "1970-01-01T00:00:00.000Z"

const (
	StatusDryRun  = "dry-run"
	StatusSkipped = "skipped"
	StatusSuccess = "success"
	StatusFailed  = "failed"
)

type Client interface {
	ListRooms(ctx context.Context) ([]rocketchat.Room, error)
	CleanRoomHistory(ctx context.Context, roomID string, options rocketchat.CleanRoomHistoryOptions) error
	ListMessages(ctx context.Context, room rocketchat.Room, options rocketchat.ListMessagesOptions) ([]rocketchat.Message, rocketchat.Page, error)
	DeleteMessage(ctx context.Context, roomID string, messageID string) error
	MessageExists(ctx context.Context, messageID string) (bool, error)
}

type Result struct {
	Room              rocketchat.Room
	Status            string
	Reason            string
	Error             string
	MessagesFound     int
	MessagesDeleted   int
	MessagesFailed    int
	LimitReached      bool
	DeletedMessageIDs []string
	FailedMessageIDs  []string
}

type Summary struct {
	TotalRooms      int
	Skipped         int
	DryRun          bool
	MessageMode     bool
	Verbose         bool
	Succeeded       int
	Failed          int
	MessagesFound   int
	MessagesDeleted int
	MessagesFailed  int
	Results         []Result
}

type purgeJob struct {
	index int
	room  rocketchat.Room
}

func Run(ctx context.Context, client Client, cfg config.Config, now time.Time) (Summary, error) {
	rooms, err := client.ListRooms(ctx)
	if err != nil {
		return Summary{}, err
	}

	excluded := map[string]struct{}{}
	for _, room := range cfg.ExcludeRooms {
		excluded[room] = struct{}{}
	}

	targetRooms, err := filterTargets(rooms, cfg)
	if err != nil {
		return Summary{}, err
	}
	if cfg.Mode == "messages" {
		return runMessageMode(ctx, client, cfg, rooms, targetRooms), nil
	}

	results := make([]Result, 0, len(targetRooms))
	jobs := make([]purgeJob, 0, len(targetRooms))
	latest := formatRocketChatTime(now)
	for _, room := range targetRooms {
		if isExcluded(room, excluded) {
			results = append(results, Result{
				Room:   room,
				Status: StatusSkipped,
				Reason: "Excluded by --exclude-room",
			})
			continue
		}
		if isExcludedDM(room, cfg) {
			results = append(results, Result{
				Room:   room,
				Status: StatusSkipped,
				Reason: "Excluded by --exclude-dms",
			})
			continue
		}
		if cfg.DryRun {
			results = append(results, Result{Room: room, Status: StatusDryRun})
			continue
		}
		jobs = append(jobs, purgeJob{index: len(results), room: room})
		results = append(results, Result{})
	}

	purgeResults := runLimited(ctx, client, cfg, latest, jobs)
	for index, result := range purgeResults {
		results[index] = result
	}

	return summarize(results, cfg.DryRun, len(rooms), false, cfg.Verbose), nil
}

func runMessageMode(ctx context.Context, client Client, cfg config.Config, rooms []rocketchat.Room, targetRooms []rocketchat.Room) Summary {
	excluded := map[string]struct{}{}
	for _, room := range cfg.ExcludeRooms {
		excluded[room] = struct{}{}
	}

	results := make([]Result, 0, len(targetRooms))
	for _, room := range targetRooms {
		if isExcluded(room, excluded) {
			results = append(results, Result{
				Room:   room,
				Status: StatusSkipped,
				Reason: "Excluded by --exclude-room",
			})
			continue
		}
		if isExcludedDM(room, cfg) {
			results = append(results, Result{
				Room:   room,
				Status: StatusSkipped,
				Reason: "Excluded by --exclude-dms",
			})
			continue
		}

		results = append(results, purgeMessagesInRoom(ctx, client, cfg, room))
	}
	return summarize(results, cfg.DryRun, len(rooms), true, cfg.Verbose)
}

func purgeMessagesInRoom(ctx context.Context, client Client, cfg config.Config, room rocketchat.Room) Result {
	result := Result{
		Room:   room,
		Status: StatusSuccess,
	}
	if cfg.DryRun {
		result.Status = StatusDryRun
	}

	pageSize := cfg.PageSize
	if pageSize < 1 {
		pageSize = 100
	}

	printVerboseScanStart(cfg, room)
	attempted := map[string]struct{}{}
	offset := 0
	for {
		if cfg.MaxMessages > 0 && result.MessagesFound >= cfg.MaxMessages {
			break
		}

		pageMessages, page, err := client.ListMessages(ctx, room, rocketchat.ListMessagesOptions{
			Offset:             offset,
			Count:              pageSize,
			IncludeThreads:     cfg.IncludeThreads,
			IncludeDiscussions: cfg.IncludeDiscussions,
		})
		if err != nil {
			result.Status = StatusFailed
			result.Error = err.Error()
			return result
		}

		ownPageMessages := ownMessages(pageMessages, cfg.UserID)
		printVerboseScanPage(cfg, room, offset, len(pageMessages), len(ownPageMessages), result.MessagesFound)

		deletedFromPage := false
		for _, message := range ownPageMessages {
			if _, ok := attempted[message.ID]; ok {
				continue
			}
			if cfg.MaxMessages > 0 && result.MessagesFound >= cfg.MaxMessages {
				break
			}
			attempted[message.ID] = struct{}{}
			result.MessagesFound++
			if cfg.DryRun {
				continue
			}

			printVerboseDelete(cfg, room, message.ID, "deleting", nil)
			if err := client.DeleteMessage(ctx, room.ID, message.ID); err != nil {
				result.MessagesFailed++
				result.FailedMessageIDs = append(result.FailedMessageIDs, message.ID)
				result.Error = err.Error()
				printVerboseDelete(cfg, room, message.ID, "failed", err)
				if rocketchat.IsRoomReadOnly(err) {
					result.Status = StatusFailed
					return result
				}
				continue
			}
			exists, err := client.MessageExists(ctx, message.ID)
			if err != nil {
				result.MessagesFailed++
				result.FailedMessageIDs = append(result.FailedMessageIDs, message.ID)
				result.Error = err.Error()
				printVerboseDelete(cfg, room, message.ID, "failed", err)
				continue
			}
			if exists {
				err := fmt.Errorf("message %s still exists after delete", message.ID)
				result.MessagesFailed++
				result.FailedMessageIDs = append(result.FailedMessageIDs, message.ID)
				result.Error = err.Error()
				printVerboseDelete(cfg, room, message.ID, "failed", err)
				continue
			}
			result.MessagesDeleted++
			result.DeletedMessageIDs = append(result.DeletedMessageIDs, message.ID)
			deletedFromPage = true
			printVerboseDelete(cfg, room, message.ID, "deleted", nil)
			break
		}

		// A delete shifts everything below it up one position, which can pull
		// the next page's first message into this page. Re-fetching the same
		// offset catches it; pages before this one are unaffected because the
		// deleted message was the first own message found scanning forward.
		if deletedFromPage {
			continue
		}
		if len(pageMessages) == 0 || len(pageMessages) < pageSize {
			break
		}
		if cfg.MaxMessages > 0 && result.MessagesFound >= cfg.MaxMessages {
			break
		}
		offset += len(pageMessages)
		if page.Total > 0 && offset >= page.Total {
			break
		}
	}

	if cfg.MaxMessages > 0 && result.MessagesFound >= cfg.MaxMessages {
		result.LimitReached = true
	}
	if result.MessagesFailed > 0 {
		result.Status = StatusFailed
	}
	return result
}

func printVerboseScanStart(cfg config.Config, room rocketchat.Room) {
	if !cfg.Verbose || cfg.ProgressWriter == nil {
		return
	}
	fmt.Fprintf(cfg.ProgressWriter, "[verbose] %s: scanning messages\n", roomLabel(room))
}

func printVerboseScanPage(cfg config.Config, room rocketchat.Room, offset int, returned int, ownFound int, totalOwnFound int) {
	if !cfg.Verbose || cfg.ProgressWriter == nil {
		return
	}
	fmt.Fprintf(
		cfg.ProgressWriter,
		"[verbose] %s: scanned page offset=%d returned=%d own_found=%d total_own_found=%d\n",
		roomLabel(room),
		offset,
		returned,
		ownFound,
		totalOwnFound+ownFound,
	)
}

func printVerboseDelete(cfg config.Config, room rocketchat.Room, messageID string, action string, err error) {
	if !cfg.Verbose || cfg.ProgressWriter == nil {
		return
	}
	printMessageProgress(cfg.ProgressWriter, room, messageID, action, err)
}

func printMessageProgress(writer io.Writer, room rocketchat.Room, messageID string, action string, err error) {
	if err != nil {
		fmt.Fprintf(writer, "[verbose] %s: %s message %s: %s\n", roomLabel(room), action, messageID, err)
		return
	}
	fmt.Fprintf(writer, "[verbose] %s: %s message %s\n", roomLabel(room), action, messageID)
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

func ownMessages(messages []rocketchat.Message, userID string) []rocketchat.Message {
	own := make([]rocketchat.Message, 0, len(messages))
	for _, message := range messages {
		if message.Type == rocketchat.RemovedMessageType {
			continue
		}
		authorID := message.UserID
		if authorID == "" {
			authorID = message.User.ID
		}
		if authorID == userID {
			own = append(own, message)
		}
	}
	return own
}

func filterTargets(rooms []rocketchat.Room, cfg config.Config) ([]rocketchat.Room, error) {
	if cfg.AllRooms {
		return rooms, nil
	}

	targets := map[string]struct{}{}
	for _, target := range cfg.TargetRooms {
		targets[target] = struct{}{}
	}
	matchedTargets := map[string]struct{}{}
	matchedRooms := make([]rocketchat.Room, 0, len(rooms))
	for _, room := range rooms {
		matched := false
		for _, value := range []string{room.ID, room.Name, room.FName} {
			if _, ok := targets[value]; ok {
				matchedTargets[value] = struct{}{}
				matched = true
			}
		}
		if matched {
			matchedRooms = append(matchedRooms, room)
		}
	}

	for _, target := range cfg.TargetRooms {
		if _, ok := matchedTargets[target]; !ok {
			return nil, fmt.Errorf("no rooms matched --room target: %s", target)
		}
	}
	return matchedRooms, nil
}

func runLimited(ctx context.Context, client Client, cfg config.Config, latest string, jobs []purgeJob) map[int]Result {
	if len(jobs) == 0 {
		return map[int]Result{}
	}

	concurrency := cfg.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(jobs) {
		concurrency = len(jobs)
	}

	input := make(chan purgeJob)
	output := make(chan resultAt, len(jobs))
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range input {
				output <- resultAt{
					index:  job.index,
					result: purgeRoom(ctx, client, cfg, latest, job.room),
				}
			}
		}()
	}

	go func() {
		for _, job := range jobs {
			input <- job
		}
		close(input)
		wg.Wait()
		close(output)
	}()

	results := map[int]Result{}
	for item := range output {
		results[item.index] = item.result
	}
	return results
}

type resultAt struct {
	index  int
	result Result
}

func purgeRoom(ctx context.Context, client Client, cfg config.Config, latest string, room rocketchat.Room) Result {
	err := client.CleanRoomHistory(ctx, room.ID, rocketchat.CleanRoomHistoryOptions{
		Oldest:           oldestMessageDate,
		Latest:           latest,
		IgnoreDiscussion: !cfg.IncludeDiscussions,
		IgnoreThreads:    !cfg.IncludeThreads,
		ExcludePinned:    cfg.PreservePinned,
	})
	if err != nil {
		return Result{Room: room, Status: StatusFailed, Error: err.Error()}
	}
	return Result{Room: room, Status: StatusSuccess}
}

func isExcluded(room rocketchat.Room, excluded map[string]struct{}) bool {
	for _, value := range []string{room.ID, room.Name, room.FName} {
		if _, ok := excluded[value]; ok {
			return true
		}
	}
	return false
}

func isExcludedDM(room rocketchat.Room, cfg config.Config) bool {
	return cfg.ExcludeDMs && room.Type == "d"
}

func summarize(results []Result, dryRun bool, totalRooms int, messageMode bool, verbose bool) Summary {
	summary := Summary{
		TotalRooms:  totalRooms,
		DryRun:      dryRun,
		MessageMode: messageMode,
		Verbose:     verbose,
		Results:     results,
	}
	for _, result := range results {
		summary.MessagesFound += result.MessagesFound
		summary.MessagesDeleted += result.MessagesDeleted
		summary.MessagesFailed += result.MessagesFailed
		switch result.Status {
		case StatusSkipped:
			summary.Skipped++
		case StatusSuccess:
			summary.Succeeded++
		case StatusFailed:
			summary.Failed++
		}
	}
	return summary
}

func formatRocketChatTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}
