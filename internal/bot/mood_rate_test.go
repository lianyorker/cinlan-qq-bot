package bot

import "testing"

func TestMoodRateNeutral(t *testing.T) {
	if got := moodRate(1.0, 0); got != 1.0 {
		t.Fatalf("moodRate(1.0, 0) = %v, want 1.0", got)
	}
}

func TestMoodRateSadSlows(t *testing.T) {
	got := moodRate(1.0, -60)
	if got >= 1.0 || got < 0.85 {
		t.Fatalf("moodRate(1.0, -60) = %v, want ~0.91 in [0.85,1.0)", got)
	}
}

func TestMoodRateHappySpeeds(t *testing.T) {
	got := moodRate(1.0, 80)
	if got <= 1.0 || got > 1.15 {
		t.Fatalf("moodRate(1.0, 80) = %v, want ~1.12 in (1.0,1.15]", got)
	}
}

func TestMoodRateClamps(t *testing.T) {
	if got := moodRate(0.4, -100); got != 0.5 {
		t.Fatalf("moodRate(0.4, -100) = %v, want clamp 0.5", got)
	}
	if got := moodRate(3.0, 100); got != 2.0 {
		t.Fatalf("moodRate(3.0, 100) = %v, want clamp 2.0", got)
	}
}
