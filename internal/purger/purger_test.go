package purger

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rocketchat-message-purger/internal/config"
	"rocketchat-message-purger/internal/rocketchat"
)

var rooms = []rocketchat.Room{
	{ID: "r1", Name: "general", FName: "General", Type: "c"},
	{ID: "r2", Name: "random", FName: "Random", Type: "p"},
	{ID: "r3", FName: "No Name Room", Type: "d"},
}

type cleanCall struct {
	roomID  string
	options rocketchat.CleanRoomHistoryOptions
}

type fakeClient struct {
	cleanFn        func(roomID string, options rocketchat.CleanRoomHistoryOptions) error
	deleteFn       func(roomID string, msgID string) error
	messages       []rocketchat.Message
	calls          []cleanCall
	deleteCalls    []deleteCall
	listMessagesFn func(room rocketchat.Room, options rocketchat.ListMessagesOptions) ([]rocketchat.Message, rocketchat.Page, error)
	mu             sync.Mutex
}

func (f *fakeClient) ListRooms(ctx context.Context) ([]rocketchat.Room, error) {
	return rooms, nil
}

func (f *fakeClient) CleanRoomHistory(ctx context.Context, roomID string, options rocketchat.CleanRoomHistoryOptions) error {
	f.mu.Lock()
	f.calls = append(f.calls, cleanCall{roomID: roomID, options: options})
	f.mu.Unlock()
	if f.cleanFn != nil {
		return f.cleanFn(roomID, options)
	}
	return nil
}

func (f *fakeClient) ListMessages(ctx context.Context, room rocketchat.Room, options rocketchat.ListMessagesOptions) ([]rocketchat.Message, rocketchat.Page, error) {
	if f.listMessagesFn != nil {
		return f.listMessagesFn(room, options)
	}
	total := len(f.messages)
	if options.Offset >= total {
		return nil, rocketchat.Page{Offset: options.Offset, Count: 0, Total: total}, nil
	}
	end := options.Offset + options.Count
	if end > total {
		end = total
	}
	return f.messages[options.Offset:end], rocketchat.Page{Offset: options.Offset, Count: end - options.Offset, Total: total}, nil
}

func (f *fakeClient) DeleteMessage(ctx context.Context, roomID string, msgID string) error {
	f.mu.Lock()
	f.deleteCalls = append(f.deleteCalls, deleteCall{roomID: roomID, msgID: msgID})
	f.mu.Unlock()
	if f.deleteFn != nil {
		return f.deleteFn(roomID, msgID)
	}
	return nil
}

func (f *fakeClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type deleteCall struct {
	roomID string
	msgID  string
}

func intsToStrings(values []int) []string {
	strings := make([]string, 0, len(values))
	for _, value := range values {
		strings = append(strings, strconv.Itoa(value))
	}
	return strings
}

func cfg(overrides func(*config.Config)) config.Config {
	c := config.Config{
		BaseURL:      "https://chat.example.com",
		UserID:       "user-123",
		AuthToken:    "token-abc",
		DryRun:       true,
		Concurrency:  1,
		TimeoutMS:    30000,
		ExcludeRooms: []string{},
		TargetRooms:  []string{"general"},
		Mode:         "history",
		PageSize:     100,
	}
	if overrides != nil {
		overrides(&c)
	}
	return c
}

func TestMessageModeDryRunListsMessagesWithoutDeleting(t *testing.T) {
	client := &fakeClient{
		messages: []rocketchat.Message{
			{ID: "m1", RoomID: "r1", UserID: "user-123"},
			{ID: "m2", RoomID: "r1", UserID: "someone-else"},
		},
	}

	summary, err := Run(context.Background(), client, cfg(func(c *config.Config) {
		c.Mode = "messages"
		c.PageSize = 1
	}), time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if len(client.deleteCalls) != 0 {
		t.Fatalf("delete calls = %#v", client.deleteCalls)
	}
	if summary.MessageMode != true || summary.MessagesFound != 1 || summary.Results[0].MessagesFound != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Results[0].Status != StatusDryRun {
		t.Fatalf("status = %s", summary.Results[0].Status)
	}
}

func TestMessageModeConfirmedDeletesMessagesAndHonorsMaxMessages(t *testing.T) {
	client := &fakeClient{
		messages: []rocketchat.Message{
			{ID: "m1", RoomID: "r1", UserID: "user-123"},
			{ID: "m2", RoomID: "r1", UserID: "someone-else"},
			{ID: "m3", RoomID: "r1", UserID: "user-123"},
		},
	}

	summary, err := Run(context.Background(), client, cfg(func(c *config.Config) {
		c.Mode = "messages"
		c.DryRun = false
		c.ConfirmPurge = true
		c.PageSize = 2
		c.MaxMessages = 2
	}), time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if len(client.deleteCalls) != 2 {
		t.Fatalf("delete calls = %#v", client.deleteCalls)
	}
	if client.deleteCalls[0] != (deleteCall{roomID: "r1", msgID: "m1"}) {
		t.Fatalf("first delete = %#v", client.deleteCalls[0])
	}
	if client.deleteCalls[1] != (deleteCall{roomID: "r1", msgID: "m3"}) {
		t.Fatalf("second delete = %#v", client.deleteCalls[1])
	}
	if summary.MessagesDeleted != 2 || summary.MessagesFound != 2 || summary.Failed != 0 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestMessageModeKeepsFullPageSizeWhileLookingForMaxOwnMessages(t *testing.T) {
	var counts []int
	client := &fakeClient{
		listMessagesFn: func(room rocketchat.Room, options rocketchat.ListMessagesOptions) ([]rocketchat.Message, rocketchat.Page, error) {
			counts = append(counts, options.Count)
			switch options.Offset {
			case 0:
				return []rocketchat.Message{
					{ID: "m1", RoomID: "r1", UserID: "user-123"},
					{ID: "m2", RoomID: "r1", UserID: "someone-else"},
				}, rocketchat.Page{Offset: options.Offset, Count: 2, Total: 4}, nil
			case 2:
				return []rocketchat.Message{
					{ID: "m3", RoomID: "r1", UserID: "someone-else"},
					{ID: "m4", RoomID: "r1", UserID: "user-123"},
				}, rocketchat.Page{Offset: options.Offset, Count: 2, Total: 4}, nil
			default:
				return nil, rocketchat.Page{Offset: options.Offset, Count: 0, Total: 4}, nil
			}
		},
	}

	summary, err := Run(context.Background(), client, cfg(func(c *config.Config) {
		c.Mode = "messages"
		c.PageSize = 2
		c.MaxMessages = 2
	}), time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if strings.Join(intsToStrings(counts), ",") != "2,2" {
		t.Fatalf("page counts = %#v", counts)
	}
	if summary.MessagesFound != 2 || summary.MessagesDeleted != 0 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestMessageModeFindsAgainAfterEachDelete(t *testing.T) {
	events := []string{}
	client := &fakeClient{
		listMessagesFn: func(room rocketchat.Room, options rocketchat.ListMessagesOptions) ([]rocketchat.Message, rocketchat.Page, error) {
			events = append(events, "find")
			switch len(events) {
			case 1:
				return []rocketchat.Message{
					{ID: "m1", RoomID: "r1", UserID: "user-123"},
					{ID: "m2", RoomID: "r1", UserID: "user-123"},
				}, rocketchat.Page{Offset: options.Offset, Count: 2, Total: 2}, nil
			case 3:
				return []rocketchat.Message{
					{ID: "m2", RoomID: "r1", UserID: "user-123"},
				}, rocketchat.Page{Offset: options.Offset, Count: 1, Total: 1}, nil
			default:
				return nil, rocketchat.Page{Offset: options.Offset, Count: 0, Total: 0}, nil
			}
		},
		deleteFn: func(roomID string, msgID string) error {
			events = append(events, "delete:"+msgID)
			return nil
		},
	}

	summary, err := Run(context.Background(), client, cfg(func(c *config.Config) {
		c.Mode = "messages"
		c.DryRun = false
		c.ConfirmPurge = true
		c.PageSize = 100
		c.MaxMessages = 2
	}), time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if strings.Join(events, ",") != "find,delete:m1,find,delete:m2" {
		t.Fatalf("events = %#v", events)
	}
	if summary.MessagesDeleted != 2 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestMessageModeRestartsScanAfterDeleteSoShiftedMessagesAreNotSkipped(t *testing.T) {
	offsets := []int{}
	deleted := map[string]bool{}
	client := &fakeClient{
		listMessagesFn: func(room rocketchat.Room, options rocketchat.ListMessagesOptions) ([]rocketchat.Message, rocketchat.Page, error) {
			offsets = append(offsets, options.Offset)
			if !deleted["m2"] {
				switch options.Offset {
				case 0:
					return []rocketchat.Message{
						{ID: "other-1", RoomID: "r1", UserID: "someone-else"},
						{ID: "other-2", RoomID: "r1", UserID: "someone-else"},
					}, rocketchat.Page{Offset: options.Offset, Count: 2, Total: 4}, nil
				case 2:
					return []rocketchat.Message{
						{ID: "m2", RoomID: "r1", UserID: "user-123"},
						{ID: "m3", RoomID: "r1", UserID: "user-123"},
					}, rocketchat.Page{Offset: options.Offset, Count: 2, Total: 4}, nil
				}
			}
			if !deleted["m3"] {
				switch options.Offset {
				case 0:
					return []rocketchat.Message{
						{ID: "other-1", RoomID: "r1", UserID: "someone-else"},
						{ID: "other-2", RoomID: "r1", UserID: "someone-else"},
					}, rocketchat.Page{Offset: options.Offset, Count: 2, Total: 3}, nil
				case 2:
					return []rocketchat.Message{
						{ID: "m3", RoomID: "r1", UserID: "user-123"},
					}, rocketchat.Page{Offset: options.Offset, Count: 1, Total: 3}, nil
				}
			}
			switch options.Offset {
			case 0:
				return []rocketchat.Message{
					{ID: "other-1", RoomID: "r1", UserID: "someone-else"},
					{ID: "other-2", RoomID: "r1", UserID: "someone-else"},
				}, rocketchat.Page{Offset: options.Offset, Count: 2, Total: 2}, nil
			default:
				return nil, rocketchat.Page{Offset: options.Offset, Count: 0, Total: 2}, nil
			}
		},
		deleteFn: func(roomID string, msgID string) error {
			deleted[msgID] = true
			return nil
		},
	}

	summary, err := Run(context.Background(), client, cfg(func(c *config.Config) {
		c.Mode = "messages"
		c.DryRun = false
		c.ConfirmPurge = true
		c.PageSize = 2
	}), time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if summary.MessagesDeleted != 2 {
		t.Fatalf("summary = %#v", summary)
	}
	if strings.Join(intsToStrings(offsets), ",") != "0,2,0,2,0" {
		t.Fatalf("offsets = %#v", offsets)
	}
}

func TestMessageModeRecordsDeleteFailuresAndContinues(t *testing.T) {
	client := &fakeClient{
		messages: []rocketchat.Message{
			{ID: "m1", RoomID: "r1", UserID: "user-123"},
			{ID: "m2", RoomID: "r1", UserID: "user-123"},
		},
		deleteFn: func(roomID string, msgID string) error {
			if msgID == "m2" {
				return errors.New("delete denied")
			}
			return nil
		},
	}

	summary, err := Run(context.Background(), client, cfg(func(c *config.Config) {
		c.Mode = "messages"
		c.DryRun = false
		c.ConfirmPurge = true
	}), time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if summary.MessagesDeleted != 1 || summary.MessagesFailed != 1 || summary.Failed != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Results[0].Error != "delete denied" {
		t.Fatalf("result = %#v", summary.Results[0])
	}
}

func TestVerboseMessageModePrintsScanProgressBeforeDeletes(t *testing.T) {
	var progress bytes.Buffer
	client := &fakeClient{
		messages: []rocketchat.Message{
			{ID: "m1", RoomID: "r1", UserID: "someone-else"},
			{ID: "m2", RoomID: "r1", UserID: "user-123"},
		},
	}

	_, err := Run(context.Background(), client, cfg(func(c *config.Config) {
		c.Mode = "messages"
		c.DryRun = false
		c.ConfirmPurge = true
		c.PageSize = 1
		c.Verbose = true
		c.ProgressWriter = &progress
	}), time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	output := progress.String()
	for _, want := range []string{
		"[verbose] channel General (r1): scanning messages",
		"[verbose] channel General (r1): scanned page offset=0 returned=1 own_found=0 total_own_found=0",
		"[verbose] channel General (r1): scanned page offset=1 returned=1 own_found=1 total_own_found=1",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("progress missing %q: %s", want, output)
		}
	}
	if strings.Index(output, "scanned page offset=0") > strings.Index(output, "deleting message m2") {
		t.Fatalf("scan progress did not print before delete: %s", output)
	}
}

func TestVerboseMessageModeStreamsEachDeleteAsItHappens(t *testing.T) {
	var progress bytes.Buffer
	client := &fakeClient{
		messages: []rocketchat.Message{
			{ID: "m1", RoomID: "r1", UserID: "user-123"},
			{ID: "m2", RoomID: "r1", UserID: "user-123"},
		},
		deleteFn: func(roomID string, msgID string) error {
			if msgID == "m2" && !strings.Contains(progress.String(), "deleted message m1") {
				t.Fatalf("first delete had not been printed before second delete started: %s", progress.String())
			}
			return nil
		},
	}

	_, err := Run(context.Background(), client, cfg(func(c *config.Config) {
		c.Mode = "messages"
		c.DryRun = false
		c.ConfirmPurge = true
		c.Verbose = true
		c.ProgressWriter = &progress
	}), time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	output := progress.String()
	for _, want := range []string{
		"[verbose] channel General (r1): deleting message m1",
		"[verbose] channel General (r1): deleted message m1",
		"[verbose] channel General (r1): deleting message m2",
		"[verbose] channel General (r1): deleted message m2",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("progress missing %q: %s", want, output)
		}
	}
}

func TestDryRunListsRoomsWithoutCleaningHistories(t *testing.T) {
	client := &fakeClient{}

	summary, err := Run(context.Background(), client, cfg(nil), time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if client.callCount() != 0 {
		t.Fatalf("clean calls = %d", client.callCount())
	}
	if summary.TotalRooms != 3 || summary.Skipped != 0 || !summary.DryRun || summary.Succeeded != 0 || summary.Failed != 0 || len(summary.Results) != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	for _, result := range summary.Results {
		if result.Status != StatusDryRun {
			t.Fatalf("result status = %s", result.Status)
		}
	}
}

func TestConfirmedPurgeCleansIncludedRoomsWithOptions(t *testing.T) {
	client := &fakeClient{}

	summary, err := Run(context.Background(), client, cfg(func(c *config.Config) {
		c.DryRun = false
		c.ConfirmPurge = true
		c.AllRooms = true
		c.TargetRooms = nil
		c.IncludeDiscussions = true
		c.IncludeThreads = false
		c.PreservePinned = true
	}), time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if client.callCount() != 3 {
		t.Fatalf("clean calls = %d", client.callCount())
	}
	got := client.calls[0]
	if got.roomID != "r1" {
		t.Fatalf("roomID = %q", got.roomID)
	}
	wantOptions := rocketchat.CleanRoomHistoryOptions{
		Oldest:           "1970-01-01T00:00:00.000Z",
		Latest:           "2026-06-07T12:00:00.000Z",
		IgnoreDiscussion: false,
		IgnoreThreads:    true,
		ExcludePinned:    true,
	}
	if got.options != wantOptions {
		t.Fatalf("options = %#v", got.options)
	}
	if summary.Succeeded != 3 || summary.Failed != 0 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestSkipsRoomsExcludedByIDNameOrDisplayName(t *testing.T) {
	client := &fakeClient{}

	summary, err := Run(context.Background(), client, cfg(func(c *config.Config) {
		c.DryRun = false
		c.ConfirmPurge = true
		c.AllRooms = true
		c.TargetRooms = nil
		c.ExcludeRooms = []string{"r1", "random", "No Name Room"}
	}), time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if client.callCount() != 0 {
		t.Fatalf("clean calls = %d", client.callCount())
	}
	if summary.Skipped != 3 {
		t.Fatalf("Skipped = %d", summary.Skipped)
	}
	for _, result := range summary.Results {
		if result.Status != StatusSkipped {
			t.Fatalf("result status = %s", result.Status)
		}
	}
}

func TestAllRoomsCanExcludeDirectMessages(t *testing.T) {
	client := &fakeClient{}

	summary, err := Run(context.Background(), client, cfg(func(c *config.Config) {
		c.DryRun = false
		c.ConfirmPurge = true
		c.AllRooms = true
		c.TargetRooms = nil
		c.ExcludeDMs = true
	}), time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if client.callCount() != 2 {
		t.Fatalf("clean calls = %d", client.callCount())
	}
	for _, call := range client.calls {
		if call.roomID == "r3" {
			t.Fatalf("deleted direct message room: %#v", client.calls)
		}
	}
	if summary.Skipped != 1 || summary.Succeeded != 2 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestRecordsRoomFailuresAndContinues(t *testing.T) {
	var count int
	client := &fakeClient{
		cleanFn: func(roomID string, options rocketchat.CleanRoomHistoryOptions) error {
			count++
			if count == 2 {
				return errors.New("permission denied")
			}
			return nil
		},
	}

	summary, err := Run(context.Background(), client, cfg(func(c *config.Config) {
		c.DryRun = false
		c.ConfirmPurge = true
		c.AllRooms = true
		c.TargetRooms = nil
	}), time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if summary.Succeeded != 2 || summary.Failed != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Results[1].Status != StatusFailed || summary.Results[1].Error != "permission denied" {
		t.Fatalf("failed result = %#v", summary.Results[1])
	}
}

func TestLimitsConfirmedPurgeConcurrency(t *testing.T) {
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	var mu sync.Mutex
	inFlight := 0
	maxInFlight := 0
	client := &fakeClient{
		cleanFn: func(roomID string, options rocketchat.CleanRoomHistoryOptions) error {
			mu.Lock()
			inFlight++
			if inFlight > maxInFlight {
				maxInFlight = inFlight
			}
			mu.Unlock()
			started <- struct{}{}
			<-release
			mu.Lock()
			inFlight--
			mu.Unlock()
			return nil
		},
	}

	done := make(chan Summary, 1)
	go func() {
		summary, err := Run(context.Background(), client, cfg(func(c *config.Config) {
			c.DryRun = false
			c.ConfirmPurge = true
			c.AllRooms = true
			c.TargetRooms = nil
			c.Concurrency = 2
		}), time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
		if err != nil {
			t.Errorf("Run returned error: %v", err)
		}
		done <- summary
	}()

	<-started
	<-started
	if client.callCount() != 2 {
		t.Fatalf("clean calls before release = %d", client.callCount())
	}
	release <- struct{}{}
	<-started
	for i := 0; i < 2; i++ {
		release <- struct{}{}
	}

	summary := <-done
	if maxInFlight != 2 {
		t.Fatalf("maxInFlight = %d", maxInFlight)
	}
	if summary.Succeeded != 3 {
		t.Fatalf("Succeeded = %d", summary.Succeeded)
	}
}

func TestSingleRoomTargetCleansOnlyMatchingRoom(t *testing.T) {
	client := &fakeClient{}

	summary, err := Run(context.Background(), client, cfg(func(c *config.Config) {
		c.DryRun = false
		c.ConfirmPurge = true
		c.TargetRooms = []string{"random"}
	}), time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if client.callCount() != 1 {
		t.Fatalf("clean calls = %d", client.callCount())
	}
	if client.calls[0].roomID != "r2" {
		t.Fatalf("roomID = %q", client.calls[0].roomID)
	}
	if summary.TotalRooms != 3 || len(summary.Results) != 1 || summary.Succeeded != 1 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestSingleRoomTargetErrorsWhenNoRoomMatches(t *testing.T) {
	client := &fakeClient{}

	_, err := Run(context.Background(), client, cfg(func(c *config.Config) {
		c.TargetRooms = []string{"missing"}
	}), time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
	if err == nil || err.Error() != "no rooms matched --room target: missing" {
		t.Fatalf("err = %v", err)
	}
}
