// Package mood scores inbound messages for the per-chat mood meter and
// renders the prompt hint that nudges persona replies toward the mood.
package mood

import (
	"strings"
	"time"
)

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

// Circadian returns a time-of-day state hint — morning her and 3am her
// don't feel the same. Only non-neutral hours get a line.
func Circadian(now time.Time) string {
	switch h := now.Hour(); {
	case h >= 6 && h < 9:
		return "（现在是早上，你刚醒不久——精神还行，说话清醒利落）"
	case h >= 13 && h < 15:
		return "（现在是午后，你有点犯困——语气松一点、懒一点）"
	case h >= 22:
		return "（现在晚了，你安静下来了——比白天话少、语气柔一点）"
	default:
		return ""
	}
}

// CircadianFactor scales the urge to start a conversation by hour —
// she doesn't get chatty at the same rate at 8am and 1am. Sleep-window
// hours (23-07) are handled separately by the hard asleep gate; this
// shapes the awake-but-dim hours.
func CircadianFactor(now time.Time) float64 {
	switch h := now.Hour(); {
	case h >= 6 && h < 9:
		return 0.8 // 刚醒，还没那么话痨
	case h >= 12 && h < 14:
		return 0.8 // 午休犯困
	case h >= 19 && h < 23:
		return 1.2 // 晚上是最想聊天的时候
	default:
		return 1.0
	}
}

// Scene gives her a grounded "what I'm doing right now" anchor so 刚睡醒
// / 在摸鱼 isn't improvised turn-by-turn (and contradicting itself).
// Derived from clock + mood so every injection of the same hour tells the
// same story; "" means nothing worth pinning down.
func Scene(now time.Time, moodScore int) string {
	weekend := now.Weekday() == time.Saturday || now.Weekday() == time.Sunday
	var scene string
	switch h := now.Hour(); {
	case h >= 6 && h < 9:
		scene = "刚起床没多久，还有点迷糊"
	case h >= 9 && h < 12:
		if weekend {
			scene = "周末上午，瘫着刷手机"
		} else {
			scene = "上午，摸一会儿忙一会儿"
		}
	case h >= 12 && h < 14:
		scene = "刚吃完午饭，犯困"
	case h >= 14 && h < 18:
		if weekend {
			scene = "周末下午，闲得发慌"
		} else {
			scene = "下午，熬着等天黑"
		}
	case h >= 18 && h < 19:
		scene = "晚饭点儿，想着吃点啥"
	case h >= 19 && h < 23:
		scene = "晚上瘫着，一天里最闲的时候"
	case h >= 23 || h < 6:
		scene = "大半夜，困得眼皮打架"
	default:
		return ""
	}
	switch {
	case moodScore <= -30:
		scene += "，心情不太好"
	case moodScore >= 30:
		scene += "，心情不错"
	}
	return "（你现在的状态：" + scene +
		"——被问到在干嘛时按这个说，别编出矛盾的来）"
}
