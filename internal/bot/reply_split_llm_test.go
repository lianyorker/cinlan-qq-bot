package bot

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/config"
	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
)

func TestSegFidelityAcceptsPunctuationTweak(t *testing.T) {
	// Judge merged a mid-thought [[NEXT]] cut and dropped a comma at
	// the join — word order unchanged, so it must pass.
	orig := "搜出来的东西不太对劲 没找到这个号[[NEXT]]就是个抖音号 你自己去搜呗 别拿这种图给我看"
	segs := []string{"搜出来的东西不太对劲 没找到这个号，就是个抖音号 你自己去搜呗 别拿这种图给我看"}
	if !segFidelity(orig, segs) {
		t.Fatalf("punctuation-only tweak must pass fidelity")
	}
}

func TestSegFidelityAcceptsRealSplit(t *testing.T) {
	orig := "想你了[[NEXT]]刚忙完"
	segs := []string{"想你了", "刚忙完"}
	if !segFidelity(orig, segs) {
		t.Fatalf("verbatim split must pass")
	}
}

func TestSegFidelityRejectsReword(t *testing.T) {
	orig := "搜出来全是乱七八糟的号 没一个对得上"
	segs := []string{"搜出来全是杂七杂八的号 没一个对得上"}
	if segFidelity(orig, segs) {
		t.Fatalf("reworded segment must fail fidelity")
	}
}

func TestSegFidelityRejectsMarkerSplit(t *testing.T) {
	// The marker gets scattered across segments — no single segment
	// contains it whole, so fidelity must fail.
	orig := "看看这个[[NEXT]][[MEME:嘲笑]]"
	segs := []string{"看看这个[[MEME:", "嘲笑]]"}
	if segFidelity(orig, segs) {
		t.Fatalf("split marker must fail fidelity")
	}
}

func TestSegFidelityKeepsMarkerInOneSegment(t *testing.T) {
	orig := "呐 给你[[NEXT]][[MEME:害羞]]"
	segs := []string{"呐 给你", "[[MEME:害羞]]"}
	if !segFidelity(orig, segs) {
		t.Fatalf("intact marker segment must pass")
	}
}

func TestParseSegJSONFenced(t *testing.T) {
	got := parseSegJSON("```json\n[\"第一句\",\"第二句\"]\n```")
	if !reflect.DeepEqual(got, []string{"第一句", "第二句"}) {
		t.Fatalf("fenced json = %#v", got)
	}
}

func TestParseSegJSONStripsNextInsideSegment(t *testing.T) {
	got := parseSegJSON("[\"还想吃[[NEXT]]再来一份\"]")
	if !reflect.DeepEqual(got, []string{"还想吃 再来一份"}) {
		t.Fatalf("[[NEXT]] inside segment must strip to space, got %#v", got)
	}
}

func TestLLMSegmentReplyMergesMidThought(t *testing.T) {
	agent := &fakeAgent{reply: func(req domain.AgentRequest) (domain.AgentResponse, error) {
		return domain.AgentResponse{
			Reply: "[\"搜出来的东西不太对劲 没找到这个号，就是个抖音号 你自己去搜呗 别拿这种图给我看\"]",
		}, nil
	}}
	s := &Service{
		agent: agent,
		now:   time.Now,
		cfg:   config.Config{LLMReplySegment: true},
	}
	segs, ok := s.llmSegmentReply(
		context.Background(),
		"搜出来的东西不太对劲 没找到这个号[[NEXT]]就是个抖音号 你自己去搜呗 别拿这种图给我看",
		4,
	)
	if !ok || len(segs) != 1 {
		t.Fatalf("merged mid-thought should return one segment, got %#v ok=%v", segs, ok)
	}
	if segs[0] != "搜出来的东西不太对劲 没找到这个号，就是个抖音号 你自己去搜呗 别拿这种图给我看" {
		t.Fatalf("merged text wrong: %q", segs[0])
	}
}

func TestLLMSegmentReplyFallsBackOnGarbage(t *testing.T) {
	agent := &fakeAgent{reply: func(req domain.AgentRequest) (domain.AgentResponse, error) {
		return domain.AgentResponse{Reply: "不是一个JSON哦"}, nil
	}}
	s := &Service{
		agent: agent,
		now:   time.Now,
		cfg:   config.Config{LLMReplySegment: true},
	}
	if _, ok := s.llmSegmentReply(
		context.Background(),
		"搜出来的东西不太对劲 没找到这个号[[NEXT]]就是个抖音号",
		4,
	); ok {
		t.Fatalf("non-JSON judge output must fall back")
	}
}

func TestLLMSegmentReplySkipsShort(t *testing.T) {
	agent := &fakeAgent{reply: func(req domain.AgentRequest) (domain.AgentResponse, error) {
		t.Fatalf("short reply must not call judge")
		return domain.AgentResponse{}, nil
	}}
	s := &Service{
		agent: agent,
		now:   time.Now,
		cfg:   config.Config{LLMReplySegment: true},
	}
	if _, ok := s.llmSegmentReply(context.Background(), "嗯嗯", 4); ok {
		t.Fatalf("short reply should skip segmentation")
	}
}

func TestLLMSegmentReplyDisabled(t *testing.T) {
	agent := &fakeAgent{reply: func(req domain.AgentRequest) (domain.AgentResponse, error) {
		t.Fatalf("disabled feature must not call judge")
		return domain.AgentResponse{}, nil
	}}
	s := &Service{
		agent: agent,
		now:   time.Now,
		cfg:   config.Config{LLMReplySegment: false},
	}
	if _, ok := s.llmSegmentReply(
		context.Background(),
		"搜出来的东西不太对劲 没找到这个号[[NEXT]]就是个抖音号",
		4,
	); ok {
		t.Fatalf("disabled segment must return false")
	}
}
