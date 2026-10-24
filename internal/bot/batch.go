package bot

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

type pendingPlatformBatch struct {
	event   platform.Event
	timer   *time.Timer
	version uint64
}

type platformBatchFlush struct {
	key     string
	version uint64
}

func (s *Service) runPlatformWorker(
	ctx context.Context,
	queue <-chan platform.Event,
) {
	// Each event is handled on its own goroutine so a slow generation
	// request never blocks other users or chats. The wait group drains
	// in-flight handlers before the worker exits.
	var waitGroup sync.WaitGroup
	defer waitGroup.Wait()
	spawn := func(event platform.Event) {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			s.handlePlatformEvent(ctx, event)
		}()
	}

	if s.cfg.GroupBatchWindow <= 0 && s.cfg.PrivateBatchWindow <= 0 {
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-queue:
				if !ok {
					return
				}
				spawn(event)
			}
		}
	}

	pending := make(map[string]*pendingPlatformBatch)
	flushes := make(chan platformBatchFlush, workerQueueSize)
	stopPending := func() {
		for key, current := range pending {
			current.timer.Stop()
			delete(pending, key)
		}
	}
	defer stopPending()

	flush := func(key string, version uint64) {
		current, ok := pending[key]
		if !ok || current.version != version {
			return
		}
		current.timer.Stop()
		delete(pending, key)
		spawn(current.event)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case request := <-flushes:
			flush(request.key, request.version)
		case event, ok := <-queue:
			if !ok {
				for key, current := range pending {
					flush(key, current.version)
				}
				return
			}
			key := platformBatchKey(event)
			if !s.shouldBatchPlatformEvent(event) {
				if current, exists := pending[key]; exists {
					flush(key, current.version)
				}
				spawn(event)
				continue
			}
			current, exists := pending[key]
			if !exists {
				current = &pendingPlatformBatch{event: clonePlatformEvent(event)}
				pending[key] = current
			} else {
				s.stats.received.Add(1)
				current.event = mergePlatformEvents(current.event, event)
				current.timer.Stop()
			}
			current.version++
			version := current.version
			current.timer = time.AfterFunc(
				s.batchWindowFor(current.event),
				func() {
					select {
					case flushes <- platformBatchFlush{key: key, version: version}:
					case <-ctx.Done():
					}
				})
		}
	}
}

func platformBatchKey(event platform.Event) string {
	return strings.Join(
		[]string{
			event.Platform,
			event.SelfID,
			event.MessageType,
			event.ChatID,
			event.UserID,
		},
		"\x00",
	)
}

// batchWindowFor picks the merge window for the event's chat type.
// Keys are already scoped by message type so a pending entry always
// holds same-type events.
func (s *Service) batchWindowFor(event platform.Event) time.Duration {
	if event.IsGroupMessage() {
		return s.cfg.GroupBatchWindow
	}
	return s.cfg.PrivateBatchWindow
}

func (s *Service) shouldBatchPlatformEvent(event platform.Event) bool {
	switch {
	case event.IsGroupMessage():
		if s.cfg.GroupBatchWindow <= 0 ||
			!s.cfg.GroupAllowlist.Allows(event.ChatID) {
			return false
		}
	case event.IsPrivateMessage():
		if s.cfg.PrivateBatchWindow <= 0 {
			return false
		}
	default:
		return false
	}
	text, _ := event.Chain.PlainText(event.SelfID)
	return !strings.HasPrefix(strings.TrimSpace(text), "/")
}

func mergePlatformEvents(
	previous platform.Event,
	current platform.Event,
) platform.Event {
	merged := clonePlatformEvent(previous)
	if !merged.Chain.Empty() && !current.Chain.Empty() {
		merged.Chain = append(merged.Chain, message.Text("\n"))
	}
	merged.Chain = append(merged.Chain, current.Chain.Clone()...)
	if strings.TrimSpace(current.RawMessage) != "" {
		if strings.TrimSpace(merged.RawMessage) != "" {
			merged.RawMessage += "\n"
		}
		merged.RawMessage += current.RawMessage
	}
	merged.ID = current.ID
	merged.MessageID = current.MessageID
	merged.SubType = current.SubType
	merged.SenderName = current.SenderName
	merged.SenderRole = current.SenderRole
	merged.Metadata = cloneMetadata(current.Metadata)
	return merged
}

func clonePlatformEvent(event platform.Event) platform.Event {
	event.Chain = event.Chain.Clone()
	event.Metadata = cloneMetadata(event.Metadata)
	return event
}

func cloneMetadata(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
