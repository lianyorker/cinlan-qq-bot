package message

import (
	"encoding/json"
	"fmt"
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
			if target == selfID {
				mentioned = true
			} else if target != "" {
				appendMarker(&builder, "@"+target)
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
			appendMarker(&builder, "[视频]")
		case TypeFile:
			appendMarker(&builder, "[文件]")
		case TypeFace:
			appendMarker(&builder, "[表情]")
		case TypeJson:
			appendMarker(&builder, "[JSON]")
		case TypeXml:
			appendMarker(&builder, "[XML]")
		case TypeMarkdown:
			appendMarker(&builder, "[Markdown]")
		case TypeKeyboard:
			appendMarker(&builder, "[Keyboard]")
		case TypeShare:
			appendMarker(&builder, "[Share]")
		case TypeContact:
			appendMarker(&builder, "[Contact]")
		case TypeLocation:
			appendMarker(&builder, "[Location]")
		case TypeMusic:
			appendMarker(&builder, "[Music]")
		case TypeDice:
			appendMarker(&builder, "[Dice]")
		case TypeRPS:
			appendMarker(&builder, "[RPS]")
		case TypeMFace:
			appendMarker(&builder, "[MFace]")
		case TypeLightApp:
			appendMarker(&builder, "[LightApp]")
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
