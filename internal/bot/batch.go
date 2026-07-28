package bot

import (
	"context"
	"strings"
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
	if s.cfg.GroupBatchWindow <= 0 {
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-queue:
				if !ok {
					return
				}
				s.handlePlatformEvent(ctx, event)
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
		s.handlePlatformEvent(ctx, current.event)
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
				s.handlePlatformEvent(ctx, event)
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
			current.timer = time.AfterFunc(s.cfg.GroupBatchWindow, func() {
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

func (s *Service) shouldBatchPlatformEvent(event platform.Event) bool {
	if !event.IsGroupMessage() || !s.cfg.GroupAllowlist.Allows(event.ChatID) {
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
