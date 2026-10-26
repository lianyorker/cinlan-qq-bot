package message

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Type is the platform-neutral message component type.
type Type string

const (
	TypeText     Type = "text"
	TypeAt       Type = "at"
	TypeReply    Type = "reply"
	TypeImage    Type = "image"
	TypeRecord   Type = "record"
	TypeVideo    Type = "video"
	TypeFile     Type = "file"
	TypeFace     Type = "face"
	TypeJson     Type = "json"
	TypeXml      Type = "xml"
	TypeNode     Type = "node"
	TypeMarkdown Type = "markdown"
	TypeKeyboard Type = "keyboard"
	TypeShare    Type = "share"
	TypeContact  Type = "contact"
	TypeLocation Type = "location"
	TypeMusic    Type = "music"
	TypeDice     Type = "dice"
	TypeRPS      Type = "rps"
	TypeMFace    Type = "mface"
	TypeLightApp Type = "lightapp"
	TypeForward  Type = "forward"
	TypeRedBag   Type = "redbag"
)

// Component keeps the original platform fields so plugins can inspect rich
// messages without making the core depend on a particular transport.
type Component struct {
	Type Type           `json:"type"`
	Data map[string]any `json:"data,omitempty"`
}

type Chain []Component

func Text(value string) Component {
	return Component{Type: TypeText, Data: map[string]any{"text": value}}
}

func At(userID string) Component {
	return Component{Type: TypeAt, Data: map[string]any{"qq": userID}}
}

func AtNamed(userID, name string) Component {
	data := map[string]any{"qq": userID}
	if name = strings.TrimSpace(name); name != "" {
		data["name"] = name
	}
	return Component{Type: TypeAt, Data: data}
}

func Reply(messageID string) Component {
	return Component{Type: TypeReply, Data: map[string]any{"id": messageID}}
}

func Attachment(kind Type, data map[string]any) Component {
	if data == nil {
		data = make(map[string]any)
	}
	return Component{Type: kind, Data: data}
}

// Segment creates an opaque platform segment while keeping the common
// MessageChain representation independent from OneBot-specific fields.
func Segment(kind Type, data map[string]any) Component {
	return Attachment(kind, data)
}

func Image(file string) Component {
	return Attachment(TypeImage, map[string]any{"file": file})
}

func Record(file string) Component {
	return Attachment(TypeRecord, map[string]any{"file": file})
}

func Video(file string) Component {
	return Attachment(TypeVideo, map[string]any{"file": file})
}

func File(file, name string) Component {
	data := map[string]any{"file": file}
	if strings.TrimSpace(name) != "" {
		data["name"] = name
	}
	return Attachment(TypeFile, data)
}

func Face(id string) Component {
	return Attachment(TypeFace, map[string]any{"id": id})
}

func Markdown(content string) Component {
	return Attachment(TypeMarkdown, map[string]any{"content": content})
}

func JSON(value string) Component {
	return Attachment(TypeJson, map[string]any{"data": value})
}

func XML(value string) Component {
	return Attachment(TypeXml, map[string]any{"data": value})
}

func (c Chain) Clone() Chain {
	if c == nil {
		return nil
	}
	cloned := make(Chain, len(c))
	for index, component := range c {
		cloned[index] = Component{
			Type: component.Type,
			Data: cloneMap(component.Data),
		}
	}
	return cloned
}

func (c Chain) Empty() bool {
	return len(c) == 0
}

func (c Chain) HasMention(userID string) bool {
	for _, component := range c {
		if component.Type != TypeAt {
			continue
		}
		if valueString(component.Data["qq"]) == userID {
			return true
		}
	}
	return false
}

// FetchedMessage is a platform-fetched message (e.g. a reply-quote target)
// with its sender attached so the prompt can attribute who originally
// said it — without attribution she reads her own quoted words as leaked
// inner monologue ("你怎么偷看人家心里在想什么").
type FetchedMessage struct {
	Chain      Chain
	SenderID   string
	SenderName string
}

// PlainText converts a chain to a safe prompt representation. Reply segments
// are metadata and are intentionally omitted from the prompt body.
func (c Chain) PlainText(selfID string) (text string, mentioned bool) {
	var builder strings.Builder
	for _, component := range c {
		switch component.Type {
		case TypeText:
			builder.WriteString(valueString(component.Data["text"]))
		case TypeAt:
			target := valueString(component.Data["qq"])
			if target == "all" {
				appendMarker(&builder, "@全体成员")
			} else if target == selfID {
				mentioned = true
			} else if target != "" {
				// data.name carries the resolved display name when the
				// inbound enrichment looked it up — "@阿茶" not "@123456".
				if name := strings.TrimSpace(valueString(component.Data["name"])); name != "" {
					appendMarker(&builder, "@"+name)
				} else {
					appendMarker(&builder, "@"+target)
				}
			}
		case TypeReply:
			continue
		case TypeImage:
			appendMarker(&builder, "[图片]")
		case TypeRecord:
			if transcript := strings.TrimSpace(valueString(component.Data["text"])); transcript != "" {
				builder.WriteString(transcript)
			} else {
				appendMarker(&builder, "[语音]")
			}
		case TypeVideo:
			// NapCat carries the source filename in data.name.
			if n := strings.TrimSpace(valueString(component.Data["name"])); n != "" {
				appendMarker(&builder, "[视频:"+n+"]")
			} else {
				appendMarker(&builder, "[视频]")
			}
		case TypeFile:
			if n := strings.TrimSpace(valueString(component.Data["name"])); n != "" {
				appendMarker(&builder, "[文件:"+n+"]")
			} else {
				appendMarker(&builder, "[文件]")
			}
		case TypeRedBag:
			// Red packets show a cover title like "恭喜发财".
			if t := strings.TrimSpace(valueString(component.Data["title"])); t != "" {
				appendMarker(&builder, "[红包:"+t+"]")
			} else {
				appendMarker(&builder, "[红包]")
			}
		case TypeFace:
			// A human knows which face was sent, not just that one was.
			if name := FaceNameForID(valueString(component.Data["id"])); name != "" {
				appendMarker(&builder, "[表情:"+name+"]")
			} else {
				appendMarker(&builder, "[表情]")
			}
		case TypeJson:
			if card := JSONCardSummary(valueString(component.Data["data"])); card != "" {
				appendMarker(&builder, "[JSON:"+card+"]")
			} else {
				appendMarker(&builder, "[JSON]")
			}
		case TypeXml:
			appendMarker(&builder, "[XML]")
		case TypeMarkdown:
			appendMarker(&builder, "[Markdown]")
		case TypeKeyboard:
			appendMarker(&builder, "[Keyboard]")
		case TypeShare:
			// Link shares carry title+url — the title is what a person
			// would glance at before deciding to open it.
			if t := strings.TrimSpace(valueString(component.Data["title"])); t != "" {
				appendMarker(&builder, "[链接:"+t+"]")
			} else if u := strings.TrimSpace(valueString(component.Data["url"])); u != "" {
				appendMarker(&builder, "[链接:"+truncateRunes(u, 60)+"]")
			} else {
				appendMarker(&builder, "[Share]")
			}
		case TypeContact:
			if id := strings.TrimSpace(valueString(component.Data["id"])); id != "" {
				appendMarker(&builder, "[名片:"+id+"]")
			} else {
				appendMarker(&builder, "[Contact]")
			}
		case TypeLocation:
			parts := []string{}
			if t := strings.TrimSpace(valueString(component.Data["title"])); t != "" {
				parts = append(parts, t)
			}
			if c := strings.TrimSpace(valueString(component.Data["content"])); c != "" {
				parts = append(parts, c)
			}
			if len(parts) > 0 {
				appendMarker(&builder, "[位置:"+strings.Join(parts, " ")+"]")
			} else {
				appendMarker(&builder, "[Location]")
			}
		case TypeMusic:
			if t := strings.TrimSpace(valueString(component.Data["title"])); t != "" {
				appendMarker(&builder, "[音乐:"+t+"]")
			} else {
				appendMarker(&builder, "[Music]")
			}
		case TypeDice:
			if v := strings.TrimSpace(valueString(component.Data["value"])); v != "" {
				appendMarker(&builder, "[骰子:"+v+"]")
			} else {
				appendMarker(&builder, "[Dice]")
			}
		case TypeRPS:
			if v := rpsName(valueString(component.Data["value"])); v != "" {
				appendMarker(&builder, "[猜拳:"+v+"]")
			} else {
				appendMarker(&builder, "[RPS]")
			}
		case TypeMFace:
			// NapCat mface carries a readable summary like "[摸鱼]".
			if s := strings.TrimSpace(valueString(component.Data["summary"])); s != "" {
				appendMarker(&builder, "[表情:"+strings.Trim(s, "[]")+"]")
			} else {
				appendMarker(&builder, "[表情]")
			}
		case TypeLightApp:
			if p := strings.TrimSpace(valueString(component.Data["prompt"])); p != "" {
				appendMarker(&builder, "[小程序:"+truncateRunes(p, 60)+"]")
			} else {
				appendMarker(&builder, "[LightApp]")
			}
		case TypeForward:
			// Forwarded chat bundles get flattened into data.text by the
			// inbound enrichment (get_forward_msg) — render it so the
			// persona actually sees what was forwarded.
			if flat := strings.TrimSpace(valueString(component.Data["text"])); flat != "" {
				appendMarker(&builder, "[转发的聊天记录]")
				builder.WriteString(flat)
			} else {
				appendMarker(&builder, "[转发聊天记录]")
			}
		default:
			if component.Type != "" {
				appendMarker(&builder, "["+string(component.Type)+"]")
			}
		}
	}
	return strings.TrimSpace(builder.String()), mentioned
}

func (c Chain) ReplyID() string {
	for _, component := range c {
		if component.Type == TypeReply {
			if id := valueString(component.Data["id"]); id != "" {
				return id
			}
		}
	}
	return ""
}

// faceIDToName maps QQ system face ids to readable names — the inverse of
// the send-side faceNameToID table in bot. Kept in this package so both
// direction stays consistent: she sees "[表情:憨笑]", and answering
// [[FACE:憨笑]] posts id 28 back.
var faceIDToName = map[int]string{
	0: "惊讶", 1: "撇嘴", 2: "色", 3: "发呆", 4: "得意",
	5: "流泪", 6: "害羞", 7: "闭嘴", 8: "睡", 9: "大哭",
	10: "尴尬", 11: "发怒", 12: "调皮", 13: "呲牙", 14: "微笑",
	15: "难过", 16: "酷", 18: "抓狂", 20: "吐", 21: "偷笑",
	22: "白眼", 23: "傲慢", 24: "饥饿", 25: "困", 26: "惊恐",
	27: "流汗", 28: "憨笑", 30: "奋斗", 31: "咒骂", 32: "疑问",
	33: "嘘", 34: "晕", 35: "折磨", 36: "衰", 37: "骷髅",
	38: "敲打", 39: "再见", 41: "发抖", 42: "爱情", 43: "跳跳",
	44: "猪头", 45: "拥抱", 46: "蛋糕", 47: "闪电", 48: "炸弹",
	49: "刀", 50: "足球", 51: "瓢虫", 52: "便便", 53: "咖啡",
	54: "饭", 55: "药", 56: "玫瑰", 57: "凋谢", 58: "示爱",
	59: "爱心", 60: "心碎", 62: "礼物", 63: "太阳", 64: "月亮",
	66: "握手", 67: "胜利", 68: "抱拳", 69: "勾引", 70: "拳头",
	71: "差劲", 72: "爱你", 76: "飞吻", 79: "怄火", 80: "转圈",
	81: "磕头", 82: "回头", 83: "跳绳", 84: "挥手", 85: "激动",
	86: "街舞", 87: "献吻", 96: "冷汗", 97: "擦汗", 98: "抠鼻",
	99: "鼓掌", 100: "糗大了", 101: "坏笑", 104: "哈欠", 105: "鄙视",
	106: "委屈", 107: "快哭了", 108: "阴险", 109: "亲亲", 110: "吓",
	111: "可怜", 112: "菜刀", 113: "西瓜", 114: "啤酒", 115: "篮球",
	116: "乒乓",
}

// FaceNameForID resolves a face segment's numeric id to its QQ face name.
func FaceNameForID(raw string) string {
	id, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return faceIDToName[id]
}

// rpsName maps NapCat rps values (1=剪刀 2=石头 3=布) to readable names.
func rpsName(raw string) string {
	switch strings.TrimSpace(raw) {
	case "1":
		return "剪刀"
	case "2":
		return "石头"
	case "3":
		return "布"
	}
	return ""
}

// truncateRunes clips s to n runes with an ellipsis tail.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// JSONCardSummary pulls a human-readable title out of a qq-app/json card:
// NapCat cards expose a "prompt" preview line or meta.{news,music}.title.
func JSONCardSummary(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var card map[string]any
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		return ""
	}
	if p := strings.TrimSpace(valueString(card["prompt"])); p != "" {
		return truncateRunes(p, 60)
	}
	meta, _ := card["meta"].(map[string]any)
	for _, key := range []string{"news", "detail_1", "detail_2", "music", "video", "miniapp"} {
		m, _ := meta[key].(map[string]any)
		if m == nil {
			continue
		}
		if t := strings.TrimSpace(valueString(m["title"])); t != "" {
			return truncateRunes(t, 60)
		}
	}
	if t := strings.TrimSpace(valueString(card["desc"])); t != "" {
		return truncateRunes(t, 60)
	}
	return ""
}

// ImageReferences returns image sources in message order. Platform adapters
// use different field names, so prefer public URLs before local cache paths.
func (c Chain) ImageReferences() []string {
	keys := [...]string{
		"url",
		"file",
		"path",
		"filePath",
		"file_path",
		"sourcePath",
		"source_path",
		"originImageUrl",
		"origin_image_url",
	}
	references := make([]string, 0)
	seen := make(map[string]struct{})
	for _, component := range c {
		if component.Type != TypeImage {
			continue
		}
		var localReference string
		var remoteReference string
		for _, key := range keys {
			reference := strings.TrimSpace(valueString(component.Data[key]))
			if reference == "" {
				continue
			}
			if isRemoteImageReference(reference) {
				remoteReference = reference
				break
			}
			if localReference == "" ||
				(isLocalFileURI(localReference) && !isLocalFileURI(reference)) {
				localReference = reference
			}
		}
		if remoteReference != "" {
			if _, ok := seen[remoteReference]; !ok {
				seen[remoteReference] = struct{}{}
				references = append(references, remoteReference)
			}
			continue
		}
		if localReference != "" {
			if _, ok := seen[localReference]; !ok {
				seen[localReference] = struct{}{}
				references = append(references, localReference)
			}
		}
	}
	return references
}

func isRemoteImageReference(reference string) bool {
	lower := strings.ToLower(strings.TrimSpace(reference))
	return strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "data:") ||
		strings.HasPrefix(lower, "base64://")
}

func isLocalFileURI(reference string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(reference)), "file://")
}

func (c Chain) MarshalJSON() ([]byte, error) {
	type wireComponent struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data,omitempty"`
	}
	wire := make([]wireComponent, len(c))
	for index, component := range c {
		wire[index] = wireComponent{Type: string(component.Type), Data: component.Data}
	}
	return json.Marshal(wire)
}

func ParseOneBot(raw json.RawMessage, rawMessage, selfID string) (Chain, error) {
	if len(raw) > 0 && string(raw) != "null" {
		var segments []Component
		if err := json.Unmarshal(raw, &segments); err == nil {
			return segments, nil
		}

		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			return ParseCQ(text)
		}
	}
	return ParseCQ(rawMessage)
}

func ParseCQ(input string) (Chain, error) {
	chain := make(Chain, 0, 2)
	cursor := 0
	for cursor < len(input) {
		start := strings.Index(input[cursor:], "[CQ:")
		if start < 0 {
			if tail := cqUnescape(input[cursor:]); tail != "" {
				chain = append(chain, Text(tail))
			}
			break
		}
		start += cursor
		if prefix := cqUnescape(input[cursor:start]); prefix != "" {
			chain = append(chain, Text(prefix))
		}
		end := strings.IndexByte(input[start:], ']')
		if end < 0 {
			chain = append(chain, Text(cqUnescape(input[start:])))
			break
		}
		end += start
		content := input[start+4 : end]
		kind, params, ok := parseCQContent(content)
		if !ok {
			chain = append(chain, Text(cqUnescape(input[start:end+1])))
		} else {
			data := make(map[string]any, len(params))
			for key, value := range params {
				data[key] = value
			}
			chain = append(chain, Component{Type: Type(kind), Data: data})
		}
		cursor = end + 1
	}
	if len(input) == 0 {
		return nil, nil
	}
	return chain, nil
}

func parseCQContent(content string) (string, map[string]string, bool) {
	parts := strings.Split(content, ",")
	if len(parts) == 0 || parts[0] == "" {
		return "", nil, false
	}
	params := make(map[string]string)
	for _, pair := range parts[1:] {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			continue
		}
		params[key] = cqUnescape(value)
	}
	return parts[0], params, true
}

func appendMarker(builder *strings.Builder, marker string) {
	if builder.Len() > 0 {
		builder.WriteByte(' ')
	}
	builder.WriteString(marker)
	builder.WriteByte(' ')
}

func cqUnescape(value string) string {
	replacer := strings.NewReplacer(
		"&#91;", "[",
		"&#93;", "]",
		"&#44;", ",",
		"&amp;", "&",
	)
	return replacer.Replace(value)
}

func valueString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case fmt.Stringer:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	target := make(map[string]any, len(source))
	for key, value := range source {
		target[key] = value
	}
	return target
}
