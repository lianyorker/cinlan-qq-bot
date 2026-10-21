// Package mood scores inbound messages for the per-chat mood meter and
// renders the prompt hint that nudges persona replies toward the mood.
package mood

import "strings"

// Score returns the mood delta for one inbound message. Only messages that
// mention the bot move the meter — ambient chatter between other people is
// not about us. Positive interactions warm it up, being cursed at drags it
// down; the store clamps the running total to ±100.
func Score(text string, mentioned bool) int {
	if !mentioned {
		return 0
	}
	t := strings.ToLower(text)
	for _, w := range negative {
		if strings.Contains(t, w) {
			return -8
		}
	}
	for _, w := range positive {
		if strings.Contains(t, w) {
			return 6
		}
	}
	return 3 // being talked to at all is mildly nice
}

var negative = []string{
	"滚", "闭嘴", "恶心", "烦死", "弱智", "傻逼", "傻b", "sb", "垃圾",
	"死肥", "废物", "吵死", "别说话", "有病", "滚开", "去死", "妈的",
}

var positive = []string{
	"哈哈", "可爱", "谢谢", "谢啦", "爱你", "贴贴", "亲亲", "好耶",
	"厉害", "牛批", "牛逼", "赞", "夸", "乖", "摸摸", "抱抱", "喜欢你",
}

// Hint renders the system-prompt line for a mood score. Neutral moods say
// nothing so the prompt stays lean; extreme moods get explicit guidance.
func Hint(score int) string {
	switch {
	case score <= -60:
		return "（当前心情：很差，被人惹毛了——说话冲一点、爱答不理、容易烦，闲聊可以敷衍过去）"
	case score <= -20:
		return "（当前心情：不太好——回复冷淡点、短一点，闲聊不想接就简短带过）"
	case score >= 60:
		return "（当前心情：很好——活泼点、话多点、愿意开玩笑和帮忙）"
	case score >= 20:
		return "（当前心情：不错——语气轻松点，可以多聊两句）"
	default:
		return ""
	}
}
