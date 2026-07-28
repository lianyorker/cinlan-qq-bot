package bot

import (
	"context"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/binding"
	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/session"
)

func TestProgressReplyMatchesOnlyRelevantToolRequests(t *testing.T) {
	tests := []struct {
		name  string
		tools []string
		text  string
		want  string
	}{
		{
			name:  "image",
			tools: []string{"generate_image"},
			text:  "生成图片回复我",
			want:  "我给你生成一下。",
		},
		{
			name:  "web screenshot",
			tools: []string{"capture_webpage"},
			text:  "帮我截一下官网首页",
			want:  "我打开网页看一下。",
		},
		{
			name:  "casual",
			tools: []string{"capture_webpage"},
			text:  "你好",
			want:  "",
		},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			if got := progressReply(current.tools, current.text); got != current.want {
				t.Fatalf("progressReply() = %q, want %q", got, current.want)
			}
		})
	}
}

func TestSlowToolRequestSendsProgressBeforeFinalReply(t *testing.T) {
	cfg := testBotConfig(t)
	agentClient := &fakeAgent{
		reply: func(domain.AgentRequest) (domain.AgentResponse, error) {
			time.Sleep(2600 * time.Millisecond)
			return domain.AgentResponse{Reply: "官网首页截图已生成。"}, nil
		},
	}
	sender := &fakeSender{}
	service := New(
		cfg,
		agentClient,
		sender,
		session.New(10, time.Hour),
		testLogger(),
	)
	registry, err := binding.NewRegistry([]binding.Rule{{
		Name:     "source",
		Platform: "*",
		SelfID:   "10001",
		ChatType: "group",
		ChatID:   "30003",
		Tools:    []string{"capture_webpage"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	service.SetChatBindings(registry)

	service.handleEvent(
		context.Background(),
		testEvent("slow-web", "帮我截一下官网首页", true),
	)

	if len(sender.messages) != 2 ||
		sender.messages[0].text != "我打开网页看一下。" ||
		sender.messages[1].text != "官网首页截图已生成。" {
		t.Fatalf("sent messages = %#v", sender.messages)
	}
}
