package cron

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

type fakeSender struct {
	outbound []platform.Outbound
}

func TestSchedulerPersistsAndRestoresJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "cron.json")
	scheduler, err := Open(&fakeSender{}, nil, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Unix(100, 0)
	scheduler.now = func() time.Time { return now }
	if err := scheduler.Add(Job{
		ID:       "daily",
		Name:     "notice",
		ChatType: platform.ChatGroup,
		ChatID:   "30003",
		SelfID:   "10001",
		Text:     "notice",
		Interval: MinInterval,
		NextRun:  now.Add(MinInterval),
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cron store was not created: %v", err)
	}
	restored, err := Open(&fakeSender{}, nil, path)
	if err != nil {
		t.Fatalf("Open(restored) error = %v", err)
	}
	jobs := restored.List()
	if len(jobs) != 1 || jobs[0].ID != "daily" || jobs[0].ChatID != "30003" {
		t.Fatalf("restored jobs = %#v", jobs)
	}
}

func TestSchedulerPersistsRunState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cron.json")
	sender := &fakeSender{}
	scheduler, err := Open(sender, nil, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Unix(200, 0)
	scheduler.now = func() time.Time { return now }
	if err := scheduler.Add(Job{
		ID:       "due",
		Name:     "notice",
		ChatType: platform.ChatPrivate,
		ChatID:   "42",
		Text:     "hello",
		Interval: MinInterval,
		NextRun:  now,
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	scheduler.RunDue(context.Background())
	restored, err := Open(&fakeSender{}, nil, path)
	if err != nil {
		t.Fatalf("Open(restored) error = %v", err)
	}
	if len(restored.List()) != 1 || !restored.List()[0].NextRun.After(now) {
		t.Fatalf("persisted run state = %#v", restored.List())
	}
}

func (s *fakeSender) Send(_ context.Context, outbound platform.Outbound) error {
	s.outbound = append(s.outbound, outbound)
	return nil
}

func TestSchedulerRunsDueJobAndAdvancesNextRun(t *testing.T) {
	sender := &fakeSender{}
	scheduler := New(sender, nil)
	now := time.Unix(100, 0)
	scheduler.now = func() time.Time { return now }
	if err := scheduler.Add(Job{
		ID:       "daily",
		Name:     "notice",
		ChatType: platform.ChatGroup,
		ChatID:   "30003",
		SelfID:   "10001",
		Text:     "notice",
		Interval: MinInterval,
		NextRun:  now,
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	scheduler.RunDue(context.Background())
	if len(sender.outbound) != 1 || sender.outbound[0].ChatID != "30003" ||
		sender.outbound[0].SelfID != "10001" {
		t.Fatalf("outbound = %#v", sender.outbound)
	}
	if len(scheduler.List()) != 1 || !scheduler.List()[0].NextRun.After(now) {
		t.Fatalf("jobs = %#v", scheduler.List())
	}
}
