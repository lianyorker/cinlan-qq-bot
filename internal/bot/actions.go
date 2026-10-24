package bot

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

// Human micro-actions: the small non-text things a real person does in QQ
// — react to a message with an emoji, poke someone, roll dice, play
// rock-paper-scissors, fire an "oops wrong message" that gets recalled a
// second later, and sticker their own face. Each is one marker in the
// reply, resolved here in stage order.

// stickerFilePattern matches image files inside a persona sticker pack.
var stickerFilePattern = regexp.MustCompile(
	`(?i)\.(png|jpe?g|webp|gif)$`,
)

// stickerDirsFor lists the persona pack directory then the shared pack.
func (s *Service) stickerDirsFor(personaName string) []string {
	if s.stickerDir == "" {
		return nil
	}
	dirs := make([]string, 0, 2)
	if name := strings.TrimSpace(personaName); name != "" {
		dirs = append(dirs, filepath.Join(s.stickerDir, name))
	}
	return append(dirs, filepath.Join(s.stickerDir, "shared"))
}

// StickerPool maps tag -> file path for the persona's own sticker pack
// (persona dir wins over shared on duplicate tags). Proactive engine and
// the prompt note both use it, so the model only ever names real tags.
func (s *Service) StickerPool(personaName string) map[string]string {
	pool := make(map[string]string)
	for _, dir := range s.stickerDirsFor(personaName) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !stickerFilePattern.MatchString(e.Name()) {
				continue
			}
			tag := strings.TrimSuffix(
				e.Name(), filepath.Ext(e.Name()),
			)
			if tag == "" {
				continue
			}
			if _, exists := pool[tag]; !exists {
				pool[tag] = filepath.Join(dir, e.Name())
			}
		}
	}
	return pool
}

// pickSticker resolves a [[SELFIE:tag]] to a file: exact tag first, then
// substring match, then (only when no tag was asked for) a random one.
func (s *Service) pickSticker(personaName, tag string) string {
	pool := s.StickerPool(personaName)
	if len(pool) == 0 {
		return ""
	}
	tag = strings.TrimSpace(tag)
	if tag != "" {
		if path, ok := pool[tag]; ok {
			return path
		}
		for name, path := range pool {
			if strings.Contains(name, tag) || strings.Contains(tag, name) {
				return path
			}
		}
		return ""
	}
	paths := make([]string, 0, len(pool))
	for _, path := range pool {
		paths = append(paths, path)
	}
	return paths[rand.Intn(len(paths))]
}

// stickerPromptNote teaches [[SELFIE]] only when the pack actually has
// files — otherwise the model would emit markers that land as nothing.
func (s *Service) stickerPromptNote(personaName string) string {
	pool := s.StickerPool(personaName)
	if len(pool) == 0 {
		return ""
	}
	tags := make([]string, 0, len(pool))
	for tag := range pool {
		tags = append(tags, tag)
	}
	return `- 发自己的表情包：单独一行写 [[SELFIE:情绪]] 会发出你的专属贴纸（可用：` +
		strings.Join(tags, "、") + `）。撒娇卖萌、炸毛委屈、求夸夸时随手甩一张就行，别连着发，也别配讲解文字。`
}

// emojiLikeIDs maps reaction keywords to QQ system face ids (NapCat
// set_msg_emoji_like: ids <= 3 digits ride the classic face table we
// already ship in faceNameToID). A couple of newer NTQQ ids are included
// best-effort — tweak this map if a reaction renders the wrong face.
// Raw numeric ids in [[REACT:123]] pass straight through.
var emojiLikeIDs = map[string]string{
	"爱心": "59", "爱": "59", "心": "59", "么么哒": "109", "亲亲": "109",
	"笑": "28", "憨笑": "28", "开心": "28", "偷笑": "21", "坏笑": "101",
	"哭": "9", "大哭": "9", "哭哭": "9", "流泪": "5", "委屈": "106", "可怜": "111", "快哭了": "107",
	"疑问": "32", "疑惑": "32", "晕": "34", "发呆": "3",
	"生气": "11", "发怒": "11", "怒": "11", "怄火": "79", "咒骂": "31",
	"鼓掌": "99", "棒棒": "99", "厉害": "99",
	"再见": "39", "挥手": "84", "拜拜": "39",
	"惊讶": "0", "震惊": "0", "吓": "110", "惊恐": "26",
	"得意": "4", "调皮": "12", "龇牙": "13", "呲牙": "13",
	"尴尬": "10", "流汗": "27", "擦汗": "97", "冷汗": "96",
	"白眼": "22", "鄙视": "105", "傲慢": "23", "阴险": "108",
	"困": "25", "哈欠": "104", "睡": "8",
	"玫瑰": "56", "示爱": "58", "爱你": "72", "飞吻": "76", "献吻": "87",
	"心碎": "60", "凋谢": "57", "衰": "36", "糗大了": "100",
	"拥抱": "45", "握手": "66", "胜利": "67", "抱拳": "68", "拳头": "70", "勾引": "69",
	"蛋糕": "46", "礼物": "62", "咖啡": "53", "啤酒": "114", "西瓜": "113", "便便": "52",
	"敲打": "38", "炸弹": "48", "刀": "49", "菜刀": "112", "骷髅": "37",
	"猪头": "44", "跳跳": "43", "闪电": "47", "太阳": "63", "月亮": "64",
	"吐": "20", "抠鼻": "98", "差劲": "71",
	// NTQQ panel faces beyond the classic 0-116 range — best effort.
	"赞": "339", "点赞": "339", "强": "339", "棒": "339",
	"捂脸": "187", "doge": "302", "狗头": "302",
}

// reactMarkerPattern matches a whole part like [[REACT:爱心]] or
// [[REACT:59]]; [[REACT]] alone defaults to a heart.
var reactMarkerPattern = regexp.MustCompile(
	`^\s*\[\[\s*(?:REACT|EMOJI|LIKE|表情)\s*(?:[:：]\s*([^\[\]]{1,12}))?\s*\]\]\s*$`,
)

// selfieMarkerPattern matches a whole part [[SELFIE:情绪]] that sends a
// sticker from the persona's own pack; bare [[SELFIE]] picks a random one.
var selfieMarkerPattern = regexp.MustCompile(
	`^\s*\[\[\s*SELFIE\s*(?:[:：]\s*([^\[\]]{1,20}))?\s*\]\]\s*$`,
)

func parseSelfieMarker(part string) (string, bool) {
	m := selfieMarkerPattern.FindStringSubmatch(part)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

func parseReactMarker(part string) (string, bool) {
	m := reactMarkerPattern.FindStringSubmatch(part)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// reactToMessage attaches an emoji reaction to the incoming message —
// the "didn't type but acknowledged" move every real QQ user makes.
func (s *Service) reactToMessage(
	ctx context.Context,
	state *flowState,
	key string,
) {
	if s.userProfiler == nil || state.messageID == "" {
		return
	}
	emojiID := "59" // bare [[REACT]] = heart
	if key != "" {
		if mapped, ok := emojiLikeIDs[key]; ok {
			emojiID = mapped
		} else if _, err := strconv.Atoi(key); err == nil {
			emojiID = key
		} else {
			return // unknown keyword — skip silently
		}
	}
	msgID, err := strconv.ParseInt(state.messageID, 10, 64)
	if err != nil {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := s.callAction(callCtx, state, "set_msg_emoji_like", map[string]any{
		"message_id": msgID,
		"emoji_id":   emojiID,
		"set":        true,
	}); err != nil {
		s.logger.Info("emoji reaction failed",
			"chat_id", state.chatID, "emoji_id", emojiID,
			"error", err.Error())
	}
}

// pokeMarkerPattern matches [[POKE]] (poke the current sender) and
// [[POKE:12345]] (poke a specific member).
var pokeMarkerPattern = regexp.MustCompile(
	`^\s*\[\[\s*POKE\s*(?:[:：]\s*([0-9]{5,12}))?\s*\]\]\s*$`,
)

func parsePokeMarker(part string) (string, bool) {
	m := pokeMarkerPattern.FindStringSubmatch(part)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// sendPoke fires a real 戳一戳. NapCat's unified action is send_poke;
// older deployments expose group_poke/friend_poke, so we walk the fallbacks.
func (s *Service) sendPoke(
	ctx context.Context,
	state *flowState,
	target string,
) {
	if s.userProfiler == nil {
		return
	}
	target = strings.TrimSpace(target)
	if target == "" {
		target = state.userID
	}
	if target == "" || target == state.selfID {
		return
	}
	userID, err := strconv.ParseInt(target, 10, 64)
	if err != nil {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if state.chatType == platform.ChatGroup {
		groupID, _ := strconv.ParseInt(state.chatID, 10, 64)
		params := map[string]any{"user_id": userID, "group_id": groupID}
		if _, err = s.callAction(callCtx, state, "send_poke", params); err != nil {
			_, err = s.callAction(callCtx, state, "group_poke", params)
		}
	} else {
		params := map[string]any{"user_id": userID}
		if _, err = s.callAction(callCtx, state, "send_poke", params); err != nil {
			_, err = s.callAction(callCtx, state, "friend_poke", params)
		}
	}
	if err != nil {
		s.logger.Info("poke failed",
			"chat_id", state.chatID, "target", target,
			"error", err.Error())
	}
}

var diceMarkerPattern = regexp.MustCompile(
	`^\s*\[\[\s*(?:DICE|骰子)\s*\]\]\s*$`,
)
var rpsMarkerPattern = regexp.MustCompile(
	`^\s*\[\[\s*(?:RPS|猜拳)\s*\]\]\s*$`,
)

func isDiceMarker(part string) bool { return diceMarkerPattern.MatchString(part) }
func isRPSMarker(part string) bool  { return rpsMarkerPattern.MatchString(part) }

// oopsMarkerPattern matches a whole part [[OOPS]]内容[[/OOPS]] — the inner
// text is sent as a normal bubble then recalled a beat later, the way a
// person fat-fingers a message and immediately takes it back.
var oopsMarkerPattern = regexp.MustCompile(
	`(?s)^\s*\[\[\s*OOPS\s*\]\](.*?)\[\[\s*/\s*OOPS\s*\]\]\s*$`,
)

func parseOopsMarker(part string) (string, bool) {
	m := oopsMarkerPattern.FindStringSubmatch(part)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// sendThenRecall posts the text as a real message via the platform Call
// path (so we keep the message_id), waits a human moment, then recalls it.
func (s *Service) sendThenRecall(
	ctx context.Context,
	state *flowState,
	text string,
) {
	if s.userProfiler == nil || text == "" {
		return
	}
	// Let face markers degrade to unicode emoji so no [face:x] leaks.
	if _, plain, _ := parseFaceMarkers(text); plain != "" {
		text = plain
	}
	var action string
	params := map[string]any{
		"message": []map[string]any{
			{"type": "text", "data": map[string]any{"text": text}},
		},
	}
	if state.chatType == platform.ChatGroup {
		action = "send_group_msg"
		if id, err := strconv.ParseInt(state.chatID, 10, 64); err == nil {
			params["group_id"] = id
		}
	} else {
		action = "send_private_msg"
		if id, err := strconv.ParseInt(state.chatID, 10, 64); err == nil {
			params["user_id"] = id
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	raw, err := s.callAction(callCtx, state, action, params)
	if err != nil {
		s.logger.Info("oops send failed",
			"chat_id", state.chatID, "error", err.Error())
		return
	}
	msgID := intField(actionResultMap(raw), "message_id")
	if msgID == 0 {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(1200 * time.Millisecond):
	}
	if _, err := s.callAction(callCtx, state, "delete_msg", map[string]any{
		"message_id": msgID,
	}); err != nil {
		s.logger.Info("oops recall failed",
			"chat_id", state.chatID, "message_id", msgID,
			"error", err.Error())
	}
}

// actionPromptNote teaches the micro-action markers. Requires the raw
// platform Call surface (userProfiler); gated on that at injection.
const actionPromptNote = `- 小动作（不占文字气泡，单独一行写）：[[REACT:表情名]] 给对方这条消息贴表情回应（比如：爱心、笑、哭、疑问、生气、鼓掌、得意、白眼、坏笑、可怜、拥抱，也可写 [[REACT:数字id]]）；[[POKE]] 戳一戳对方、[[POKE:QQ号]] 戳指定成员；[[DICE]] 掷骰子、[[RPS]] 猜拳；[[OOPS]]内容[[/OOPS]] 假装手滑发出去再秒撤回。每个标记用一两次就够，别刷屏。`

// selfPokeNotice reports whether this event is someone poking the bot
// itself (NapCat: notice/notify + sub_type=poke + target_id=self).
func (s *Service) selfPokeNotice(state *flowState) bool {
	if state.event.PostType != "notice" ||
		valueString(state.event.Metadata, "notice_type") != "notify" ||
		state.event.SubType != "poke" ||
		state.userID == "" ||
		state.userID == state.selfID ||
		state.chatID == "" {
		return false
	}
	return valueString(state.event.Metadata, "target_id") == state.selfID
}

// voiceTranscriber turns a local audio file into text; *asr.Client is the
// production implementation, tests stub it.
type voiceTranscriber interface {
	Transcribe(ctx context.Context, path string) (string, error)
}

var errNoProfiler = errors.New("no platform call surface")

// callFor is the optional multi-account Call surface — MultiAdapter routes
// by selfID/account name so actions hit the account that saw the message.
type callFor interface {
	CallFor(ctx context.Context, accountID, action string, params map[string]any) (any, error)
}

// callAction runs a platform action on the account that received this
// event when the profiler supports CallFor, else falls back to the
// default account.
func (s *Service) callAction(
	ctx context.Context, state *flowState, action string, params map[string]any,
) (any, error) {
	if s.userProfiler == nil {
		return nil, errNoProfiler
	}
	if caller, ok := s.userProfiler.(callFor); ok && state != nil && state.selfID != "" {
		return caller.CallFor(ctx, state.selfID, action, params)
	}
	return s.userProfiler.Call(ctx, action, params)
}

// enrichInboundMedia makes non-text segments human-readable before the
// chain reaches PlainText: voice clips get a transcript via ASR, merged
// forwards get flattened into readable lines. Both are best-effort — a
// failed fetch leaves the original [语音]/[转发聊天记录] marker.
func (s *Service) enrichInboundMedia(ctx context.Context, state *flowState, chain message.Chain) {
	if s.userProfiler == nil || chain == nil {
		return
	}
	for i, component := range chain {
		// Data can be nil for hand-built segments — map writes panic on
		// nil, reads don't, so guard only before storing.
		ensureData := func() {
			if chain[i].Data == nil {
				chain[i].Data = map[string]any{}
			}
		}
		switch component.Type {
		case message.TypeRecord:
			if strings.TrimSpace(valueString(component.Data, "text")) != "" {
				continue // already carries a transcript
			}
			fileID := valueString(component.Data, "file")
			if fileID == "" || s.transcriber == nil {
				continue
			}
			transcript := s.transcribeRecord(ctx, state, fileID)
			if transcript != "" {
				ensureData()
				chain[i].Data["text"] = transcript
			}
		case message.TypeForward:
			if strings.TrimSpace(valueString(component.Data, "text")) != "" {
				continue
			}
			id := valueString(component.Data, "id")
			if id == "" {
				continue
			}
			flat := s.flattenForward(ctx, state, id)
			if flat != "" {
				ensureData()
				chain[i].Data["text"] = flat
			}
		}
	}
}

// transcribeRecord pulls the voice file via get_record (NapCat converts to
// mp3 on request) and hands it to the configured ASR endpoint.
func (s *Service) transcribeRecord(ctx context.Context, state *flowState, fileID string) string {
	callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	raw, err := s.callAction(callCtx, state, "get_record", map[string]any{
		"file":       fileID,
		"out_format": "mp3",
	})
	if err != nil {
		s.logger.Info("get_record failed",
			"chat_id", state.chatID, "error", err.Error())
		return ""
	}
	path := ""
	if m := actionResultMap(raw); m != nil {
		path = valueString(m, "file")
		if path == "" {
			path = valueString(m, "path")
		}
	}
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 || info.Size() > 8<<20 {
		return ""
	}
	text, err := s.transcriber.Transcribe(callCtx, path)
	if err != nil {
		s.logger.Info("voice transcribe failed",
			"chat_id", state.chatID, "error", err.Error())
		return ""
	}
	return strings.TrimSpace(text)
}

// flattenForward pulls a merged forward bundle and renders it as
// "昵称: 内容" lines so the persona can actually read the pasted chat.
// Capped at ~500 runes / 15 lines — huge forwards get clipped, not skipped.
func (s *Service) flattenForward(ctx context.Context, state *flowState, id string) string {
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := s.callAction(callCtx, state, "get_forward_msg", map[string]any{
		"message_id": id,
	})
	if err != nil {
		return ""
	}
	payload, err := json.Marshal(actionResultMap(raw))
	if err != nil || len(payload) == 0 || string(payload) == "null" {
		return ""
	}
	var body struct {
		Messages []struct {
			Sender struct {
				Nickname string `json:"nickname"`
			} `json:"sender"`
			Content json.RawMessage `json:"content"`
			Message json.RawMessage `json:"message"`
		} `json:"messages"`
		// Flattened-array variants some impls return instead.
		Message json.RawMessage `json:"message"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return ""
	}
	var lines []string
	addLine := func(nick string, segs json.RawMessage) {
		text := flattenSegments(segs, state.selfID)
		if text == "" {
			return
		}
		if nick == "" {
			nick = "?"
		}
		lines = append(lines, nick+": "+text)
	}
	switch {
	case len(body.Messages) > 0:
		for _, m := range body.Messages {
			segs := m.Content
			if len(segs) == 0 {
				segs = m.Message
			}
			addLine(m.Sender.Nickname, segs)
		}
	case len(body.Message) > 0:
		addLine("", body.Message)
	case len(body.Content) > 0:
		addLine("", body.Content)
	}
	if len(lines) == 0 {
		return ""
	}
	flat := strings.Join(lines, "\n")
	runes := []rune(flat)
	if len(lines) > 15 || len(runes) > 500 {
		lines = lines[:15]
		flat = strings.Join(lines, "\n")
		runes = []rune(flat)
		if len(runes) > 500 {
			flat = string(runes[:500])
		}
		flat += "\n…（后面还有）"
	}
	return flat
}

// flattenSegments renders one forwarded message's segment array into a
// single readable line: text stays, rich parts degrade to markers.
func flattenSegments(raw json.RawMessage, selfID string) string {
	if len(raw) == 0 {
		return ""
	}
	var segs []map[string]any
	if err := json.Unmarshal(raw, &segs); err != nil {
		var single map[string]any
		if err := json.Unmarshal(raw, &single); err == nil && single != nil {
			segs = []map[string]any{single}
		} else {
			var plain string
			if err := json.Unmarshal(raw, &plain); err == nil {
				return strings.TrimSpace(plain)
			}
			return ""
		}
	}
	var b strings.Builder
	for _, seg := range segs {
		t, _ := seg["type"].(string)
		data, _ := seg["data"].(map[string]any)
		switch t {
		case "text":
			b.WriteString(valueString(data, "text"))
		case "at":
			b.WriteString("@" + valueString(data, "qq"))
		case "image":
			b.WriteString("[图片]")
		case "face", "mface":
			b.WriteString("[表情]")
		case "record":
			b.WriteString("[语音]")
		case "video":
			b.WriteString("[视频]")
		case "file":
			b.WriteString("[文件]")
		case "forward":
			b.WriteString("[套娃转发]")
		case "json", "xml":
			b.WriteString("[卡片]")
		case "reply":
			// quoting inside a forward — skip the marker noise
		default:
			if t != "" {
				b.WriteString("[" + t + "]")
			}
		}
	}
	return strings.TrimSpace(b.String())
}
