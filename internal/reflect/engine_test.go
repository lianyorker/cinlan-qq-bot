package reflect

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/binding"
	"github.com/lianyorker/cinlan-qq-bot/internal/chatprefs"
	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/persona"
	"github.com/lianyorker/cinlan-qq-bot/internal/session"
)

type fakeAgent struct {
	reply string
	err   error
	seen  []domain.AgentRequest
}

func (f *fakeAgent) Reply(
	ctx context.Context, req domain.AgentRequest,
) (domain.AgentResponse, error) {
	f.seen = append(f.seen, req)
	if f.err != nil {
		return domain.AgentResponse{}, f.err
	}
	return domain.AgentResponse{Reply: f.reply}, nil
}

func newEngine(t *testing.T, agent *fakeAgent) (*Engine, *chatprefs.Store) {
	t.Helper()
	store, err := chatprefs.Load(t.TempDir() + "/prefs.json")
	if err != nil {
		t.Fatalf("load prefs: %v", err)
	}
	reg, err := binding.NewRegistry([]binding.Rule{{
		Name:     "test",
		ChatType: "private",
		ChatIDs:  []string{"u1"},
		SelfID:   "bot1",
		Persona:  "七七",
	}})
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	e := New(
		Config{
			Enabled:            true,
			Tick:               time.Hour,
			MergeFactsAt:       5,
			MergeImpressionsAt: 4,
			JournalMaxAge:      time.Hour,
		},
		Deps{
			Agent:     agent,
			Bindings:  reg,
			ChatPrefs: store,
			Activity: func(chatType, chatID string) time.Time {
				return time.Now()
			},
			Logger: nil,
		},
	)
	return e, store
}

func TestParseLines(t *testing.T) {
	got := parseLines("1. 他胃不好\n- 怕苦\n· \n空行\n")
	if len(got) != 3 || got[0] != "他胃不好" || got[1] != "怕苦" || got[2] != "空行" {
		t.Fatalf("got %#v", got)
	}
}

func TestConsolidateFactsShrinks(t *testing.T) {
	agent := &fakeAgent{reply: "他胃不好还爱冰美式\n最近在赶工"}
	e, store := newEngine(t, agent)
	for i := 0; i < 8; i++ {
		if err := store.AddFact("private", "u1",
			fmt.Sprintf("零散记忆%d", i)); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	e.consolidateFacts(context.Background(), "private", "u1")
	facts := store.FactsSnapshot("private", "u1")
	if len(facts) != 2 || facts[0].Text != "他胃不好还爱冰美式" {
		t.Fatalf("facts = %#v", facts)
	}
}

func TestConsolidateFactsSkipsWhenSmall(t *testing.T) {
	agent := &fakeAgent{reply: "x"}
	e, store := newEngine(t, agent)
	_ = store.AddFact("private", "u1", "一条")
	e.consolidateFacts(context.Background(), "private", "u1")
	if len(agent.seen) != 0 {
		t.Fatal("below threshold should not call the model")
	}
}

func TestConsolidateFactsKeepsOnNoShrink(t *testing.T) {
	agent := &fakeAgent{reply: "1\n2\n3\n4\n5\n6\n7\n8"}
	e, store := newEngine(t, agent)
	for i := 0; i < 8; i++ {
		_ = store.AddFact("private", "u1", fmt.Sprintf("m%d", i))
	}
	e.consolidateFacts(context.Background(), "private", "u1")
	if got := store.FactsSnapshot("private", "u1"); len(got) != 8 {
		t.Fatalf("no-shrink should keep originals, got %d", len(got))
	}
}

func TestConsolidateImpressionsRoster(t *testing.T) {
	agent := &fakeAgent{reply: "群友速写：爱抬杠的甲、半夜冒泡的乙"}
	e, store := newEngine(t, agent)
	for i := 0; i < 6; i++ {
		_ = store.SetImpression("private", "u1",
			fmt.Sprintf("u%d", i), fmt.Sprintf("印象%d", i))
	}
	e.consolidateImpressions(context.Background(), "private", "u1")
	imps := store.ImpressionTexts("private", "u1")
	if len(imps) != 3 {
		t.Fatalf("fresh half kept = %d, want 3", len(imps))
	}
	facts := store.FactsSnapshot("private", "u1")
	found := false
	for _, f := range facts {
		if f.Text == "群友速写：爱抬杠的甲、半夜冒泡的乙" {
			found = true
		}
	}
	if !found {
		t.Fatal("roster line missing from facts")
	}
}

func TestJournalOncePerDay(t *testing.T) {
	agent := &fakeAgent{reply: "他今天赶工到很晚，情绪不太好"}
	e, store := newEngine(t, agent)
	p := persona.NewRegistry()
	if err := p.Register(persona.Profile{
		Name: "七七", SystemPrompt: "你是七七",
	}); err != nil {
		t.Fatalf("persona: %v", err)
	}
	e.deps.Personas = p
	e.deps.Sessions = session.New(10, time.Hour)
	sess := "qq-onebot:self:bot1:private:u1"
	for i := 0; i < 4; i++ {
		e.deps.Sessions.AddExchange(sess, "hi", "yo")
	}
	e.journal(context.Background())
	facts := store.FactsSnapshot("private", "u1")
	if len(facts) != 1 ||
		facts[0].Text != "日记·"+e.deps.Now().Format("01.02")+
			" 他今天赶工到很晚，情绪不太好" {
		t.Fatalf("facts = %#v", facts)
	}
	// Second sweep same day: already written, no second call.
	e.journal(context.Background())
	if got := store.FactsSnapshot("private", "u1"); len(got) != 1 {
		t.Fatalf("journal re-fires, got %d facts", len(got))
	}
}
