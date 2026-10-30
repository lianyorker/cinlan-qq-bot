package bot

import "strings"

// Marker feature toggles: every interactive [[ACTION]] marker the model
// can emit resolves through one enabled-set so operators can switch
// gestures off globally (ACTIONS env), per binding (actions/actions_off
// in chat-bindings.json), or both — a binding allowlist replaces the
// global set, blacklist entries always subtract.
//
// Toggleable ids:
//
//	at react poke dice rps oops meme img selfie voice
//
// Control-plane markers ([[NEXT]] reply splitting, [[PASS]] abstain,
// [[AFFINITY]] mood deltas, [[QUOTE]]) stay always-on — disabling them
// would break reply mechanics rather than a feature.
type markerSet map[string]bool

// markerIDs lists every operator-toggleable marker id, in doc order.
var markerIDs = []string{
	"at", "react", "poke", "dice", "rps", "oops",
	"meme", "img", "selfie", "voice",
}

// gestureDocs feeds the generated protocol block — one doc fragment per
// gesture marker, keeping the contract text next to the parsers it
// describes. Order matters for prompt stability. Docs carry the usage
// policy too, so a disabled marker is neither documented nor emitted.
var gestureOrder = []string{
	"at", "react", "poke", "dice", "rps", "oops",
	"meme", "img", "selfie",
}

var gestureDocs = map[string]string{
	"at":     "[[AT]]在群里@回说话的人、[[AT:QQ号]]@别人（@完接正文 别只发@没话说）",
	"react":  "[[REACT:表情名]]给那条消息贴表情（赞同就贴个赞，每群一小时最多3次——能用表情回应就不打字）",
	"poke":   "[[POKE]]戳一下对方、[[POKE:QQ号]]戳别人（每人一天最多1次）",
	"dice":   "[[DICE]]扔骰子",
	"rps":    "[[RPS]]猜拳",
	"oops":   "发错话用[[OOPS]]内容[[/OOPS]]发出后立刻撤回装手滑（一天最多1次，别连着用）",
	"meme":   "想发自己存的表情包时 单独一条打 [[MEME:情绪词]] 比如 [[MEME:嘲笑]] 或 [[MEME:无语]]",
	"img":    "对方要“刚才那张/存过的那张”才打 [[IMG:备注key]] 比如 [[IMG:七七自拍]]",
	"selfie": "想发自己的表情包（专属贴纸）单独一条打 [[SELFIE:情绪]]",
}

// gestureNeedsCall marks gestures that invoke a platform Call (emoji
// reaction, poke, recall). When the adapter exposes no Call surface the
// markers would land as nothing, so the doc stays out — the same gate
// the old actionPromptNote applied.
var gestureNeedsCall = map[string]bool{
	"react": true,
	"poke":  true,
	"oops":  true,
}

// protocolTagLegend documents inbound segment tags — it describes what
// the parser produces, not a toggleable feature, so it always ships.
const protocolTagLegend = `- 你看到的消息标记：[表情:憨笑] 是对方发的QQ表情名（你可以回打同款）[链接:x][音乐:x][位置:x][名片:x] 是对方分享的东西标题；[骰子:6][猜拳:石头] 是对方的游戏结果；[文件:x.pdf][视频:x.mp4][红包:恭喜发财] 是对方发的文件/视频/红包名；@阿茶 就是@了那个人；[转发的聊天记录] 后面是对方贴进来的聊天记录 里面"昵称: 内容"每行一条；[语音]是对方发的语音（配了转写时直接就是文字）；[图片][视频][文件]占位；消息开头的（回复 昵称：内容）表示那条在接那条消息的话茬 把上下文对上了再答`

// protocolPassDoc keeps [[PASS]] always documented — abstaining is part
// of the reply contract, not an optional gesture.
const protocolPassDoc = `[[PASS]]整条不打字弃权——没话说、不想接、刚说过话、这事跟你没关系时 只输出[[PASS]]别的什么都不写 这条就不发了对方看不到`

// protocolNextDoc teaches [[NEXT]] — control-plane reply splitting,
// always documented alongside PASS.
const protocolNextDoc = `想分泡就打[[NEXT]]——拆的是不同的事或节奏变化 不是把一句话拦腰切断；一般一两条就够 最多四条 别为了显得碎硬拆；[[NEXT]]拆开的每条都要在回应对方发的消息 不许凭空接自己上一句的话茬 更不许对自己刚发的语音台词/图片内容做出反应`

func validMarkerID(id string) bool {
	for _, known := range markerIDs {
		if id == known {
			return true
		}
	}
	return false
}

// resolveMarkersSpec parses the global ACTIONS spec into an enabled set:
// ""/"*"/"all"/"on" → every marker; "none"/"off" → none; otherwise a
// comma-separated allowlist of marker ids. Unknown names are ignored so
// a typo can't silently widen the surface.
func resolveMarkersSpec(spec string, off []string) markerSet {
	set := markerSet{}
	switch strings.ToLower(strings.TrimSpace(spec)) {
	case "", "*", "all", "on":
		for _, id := range markerIDs {
			set[id] = true
		}
	case "none", "off", "0":
		// stays empty
	default:
		for _, id := range strings.Split(spec, ",") {
			id = strings.ToLower(strings.TrimSpace(id))
			if validMarkerID(id) {
				set[id] = true
			}
		}
	}
	for _, id := range off {
		delete(set, strings.ToLower(strings.TrimSpace(id)))
	}
	return set
}

// resolveMarkersFor applies a binding's override on top of the global
// set: a non-empty actions allowlist replaces the global set entirely;
// actions_off entries always subtract.
func resolveMarkersFor(base markerSet, allow, off []string) markerSet {
	set := markerSet{}
	if len(allow) > 0 {
		for _, id := range allow {
			id = strings.ToLower(strings.TrimSpace(id))
			if validMarkerID(id) {
				set[id] = true
			}
		}
	} else {
		for k, v := range base {
			set[k] = v
		}
	}
	for _, id := range off {
		delete(set, strings.ToLower(strings.TrimSpace(id)))
	}
	return set
}

// markerOn reports whether a marker id is enabled for this turn —
// binding-resolved set first, global fallback when no binding matched
// (legacy allowlist mode), all-on when the service predates the field.
func (s *Service) markerOn(state *flowState, id string) bool {
	if state != nil && state.markers != nil {
		return state.markers[id]
	}
	if s.markers == nil {
		return true
	}
	return s.markers[id]
}

// markersFor returns the effective set for prompt generation.
func (s *Service) markersFor(state *flowState) markerSet {
	if state != nil && state.markers != nil {
		return state.markers
	}
	if s.markers != nil {
		return s.markers
	}
	all := markerSet{}
	for _, id := range markerIDs {
		all[id] = true
	}
	return all
}

// buildProtocolBlock assembles the model-facing marker contract from the
// enabled set: the inbound tag legend, one doc fragment per enabled
// gesture (call-requiring ones hidden when the adapter has no Call), and
// the always-on [[PASS]] clause. Disabled gestures disappear entirely —
// the model can't emit markers it was never told about.
func buildProtocolBlock(set markerSet, callOK bool) string {
	var b strings.Builder
	b.WriteString("## 消息与小动作标记\n\n")
	b.WriteString(protocolTagLegend)
	b.WriteString("\n- 你能用的小动作：")
	gestures := make([]string, 0, len(gestureOrder)+1)
	for _, id := range gestureOrder {
		if !set[id] {
			continue
		}
		if gestureNeedsCall[id] && !callOK {
			continue
		}
		gestures = append(gestures, gestureDocs[id])
	}
	gestures = append(gestures, protocolPassDoc, protocolNextDoc)
	b.WriteString(strings.Join(gestures, " "))
	if len(gestures) > 1 {
		b.WriteString("——每样用一两次就够，别刷屏")
	}
	return b.String()
}
