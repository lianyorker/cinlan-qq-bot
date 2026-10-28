package chatprefs

import (
	"testing"
	"time"
)

func TestRemindersFireOnceAtDue(t *testing.T) {
	s, err := Load(t.TempDir()+"/prefs.json", "qq")
	if err != nil {
		t.Fatal(err)
	}
	now := s.now()
	if err := s.AddReminder("qq", "1", "private", "2", "他周三考试", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := s.DueReminders(now); len(got) != 0 {
		t.Fatalf("early fire: %d", len(got))
	}
	got := s.DueReminders(now.Add(2 * time.Hour))
	if len(got) != 1 || got[0].Text != "他周三考试" || got[0].ChatID != "2" {
		t.Fatalf("due pop: %+v", got)
	}
	if again := s.DueReminders(now.Add(3 * time.Hour)); len(again) != 0 {
		t.Fatalf("refire: %d", len(again))
	}
}

func TestReminderReschedulesSameText(t *testing.T) {
	s, err := Load(t.TempDir()+"/prefs.json", "qq")
	if err != nil {
		t.Fatal(err)
	}
	now := s.now()
	_ = s.AddReminder("qq", "1", "group", "9", "带伞", now.Add(time.Hour))
	_ = s.AddReminder("qq", "1", "group", "9", "带伞", now.Add(3*time.Hour))
	got := s.DueReminders(now.Add(2 * time.Hour))
	if len(got) != 0 {
		t.Fatalf("old trigger fired after reschedule")
	}
	got = s.DueReminders(now.Add(4 * time.Hour))
	if len(got) != 1 {
		t.Fatalf("rescheduled trigger: %d", len(got))
	}
}

func TestAddSelfFactMarksSelf(t *testing.T) {
	s, err := Load(t.TempDir()+"/prefs.json", "qq")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddSelfFact("qq", "1", "private", "2", "我那天挺委屈的"); err != nil {
		t.Fatal(err)
	}
	got := s.RecallFacts("qq", "1", "private", "2", "委屈", 5)
	if len(got) != 1 || !got[0].Self {
		t.Fatalf("self flag lost: %+v", got)
	}
}

func TestAddFactWithContextKeepsScene(t *testing.T) {
	s, err := Load(t.TempDir()+"/prefs.json", "qq")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddFactWithContext("qq", "1", "group", "9", "u1", "阿伟", "聊火锅呢", "阿伟讨厌香菜"); err != nil {
		t.Fatal(err)
	}
	got := s.RecallFacts("qq", "1", "group", "9", "他讨厌香菜吗", 5)
	if len(got) != 1 || got[0].Context != "聊火锅呢" || got[0].UserName != "阿伟" {
		t.Fatalf("context lost: %+v", got)
	}
}
