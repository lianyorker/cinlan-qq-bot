# 批次 6/7/8 + 未识别标记 strip 实施方案

> 给实施 agent 的完整指令。四项互相独立,可并行改,最后统一编译测试提交。
> 项目路径：D:/Company/cinlan/apps/cinlan-qq-bot（qq-bot）+ D:/Company/cinlan/apps/cinlan-bot（cinlan-bot core+adapters）
> 两侧同步：qq-bot `internal/` = cinlan-bot `core/`，import 前缀 `cinlan-qq-bot/internal/` ↔ `cinlan-core/`

---

## 项目约定

- 改完必须 `go build ./...` + `go test ./...` 双侧全绿才能提交
- qq-bot 和 cinlan-bot core 同步改（叶子文件只差 import 前缀）
- adapters（cinlan-bot/adapters/cmd/cinlan-bot/）如涉及接线也要改
- 不自动 push，提交后等确认
- commit message 格式：`feat: 一句话描述` 或 `fix: 一句话描述`，末尾加 `Co-Authored-By: Claude <noreply@anthropic.com>`
- data/ 目录被 gitignore，运行时配置文件改动不进 git

---

## 一、History 刷屏折叠（项目6）

### 问题
高活跃群里有人连发 10+ 条不同内容（表情/段子/复读），全进 session history → 20 条上限很快刷满 → 压缩时把无用刷屏内容也喂给 LLM 浪费 token。

### 改法

**落点：`internal/bot/pipeline.go` stageSession 段（约 :797-811），拿到 `snapshot.History` 后、赋值给 `state.history` 前。**

新增 `foldSpam(history []domain.ChatMessage, selfID string) []domain.ChatMessage`：

```go
// foldSpam collapses runs of consecutive same-user messages that were
// never replied to — the prompt equivalent of "he kept talking to
// himself". Keeps first 2 + last 2 of each run, middle becomes one
// summary line: "[xxx 连续发了 N 条消息]". This saves token on
// high-chatter groups without losing the conversational edges.
func foldSpam(history []domain.ChatMessage, selfID string) []domain.ChatMessage {
    const threshold = 5  // 连续多少条才折叠
    const keepEdge  = 2  // 首尾各保留几条

    if len(history) <= threshold {
        return history
    }

    var result []domain.ChatMessage
    i := 0
    for i < len(history) {
        msg := history[i]
        // 找连续同一 role=user 的 run（assistant 消息打断 run）
        if msg.Role != "user" {
            result = append(result, msg)
            i++
            continue
        }
        // 从 i 开始找连续 user 消息（不含 assistant 打断）
        j := i + 1
        for j < len(history) && history[j].Role == "user" {
            j++
        }
        runLen := j - i
        if runLen <= threshold {
            // 不够长，原样保留
            result = append(result, history[i:j]...)
        } else {
            // 保留前 keepEdge 条
            result = append(result, history[i:i+keepEdge]...)
            // 折叠中间
            folded := runLen - 2*keepEdge
            result = append(result, domain.ChatMessage{
                Role:    "user",
                Content: fmt.Sprintf("[连续发了 %d 条消息，已省略]", folded),
            })
            // 保留后 keepEdge 条
            result = append(result, history[j-keepEdge:j]...)
        }
        i = j
    }
    return result
}
```

**调用点（pipeline.go stageSession）**：
```go
// 现有:
state.history = history

// 改为:
state.history = foldSpam(history, state.selfID)
```

### 注意事项
- `domain.ChatMessage` 只有 Role + Content 两个字段（确认一下，如果有 UserID 字段可以按 UserID 分 run 更精确）
- 折叠后 `state.history` 变短 → 后续 `len(state.history) >= 12` 的长聊收尾判断受影响，但折叠只发生在长 run 场景，收尾判断的 12 条阈值不变，影响可忽略
- 这个折叠只影响当次 prompt，不改 session store 里的原始 history（不丢数据）

### 测试
在 `internal/bot/` 加 `fold_spam_test.go`：
- 5 条以下不折叠
- 8 条连续 user → 保留前 2 + 折叠 4 + 后 2
- 中间有 assistant 打断 → 分两个 run 各自判断
- 空 history → 原样返回

---

## 二、TTS 缓存（项目7）

### 问题
"你好""晚安"等高频短句每次都重新合成 → 浪费 API quota + 增加延迟。

### 改法

**落点：`internal/bot/voice.go` `sendVoiceClips` 函数（约 :385-460），在 `provider.Synthesize` 调用前后。**

#### 2.1 缓存目录
config.go 新增：
```go
VoiceCachePath string  // env: VOICE_CACHE_PATH, default: "runtime/voice-cache"
VoiceCacheMaxMB int    // env: VOICE_CACHE_MAX_MB, default: 500
VoiceCacheTTL  time.Duration // env: VOICE_CACHE_TTL, default: 7*24h
```

#### 2.2 缓存 key
```go
func voiceCacheKey(text, voice string, rate float64) string {
    h := sha256.New()
    h.Write([]byte(text))
    h.Write([]byte{0})
    h.Write([]byte(voice))
    h.Write([]byte{0})
    binary.Write(h, binary.LittleEndian, rate)
    return hex.EncodeToString(h.Sum(nil)[:16]) + ".mp3"
}
```

#### 2.3 sendVoiceClips 改造

在现有 `provider.Synthesize(ctx, text, voice, rate)` 前后包一层：

```go
cacheDir := s.cfg.VoiceCachePath
if cacheDir != "" {
    key := voiceCacheKey(clip.Text, voice, rate)
    cachePath := filepath.Join(cacheDir, key)
    if _, err := os.Stat(cachePath); err == nil {
        // 缓存命中 → 直接发
        // touch mtime (os.Chtimes) 防止被淘汰
        return cachePath, nil
    }
}

// 缓存未命中 → 正常合成
path, err := provider.Synthesize(ctx, clip.Text, voice, rate)
if err != nil { return "", err }

// 写缓存
if cacheDir != "" {
    key := voiceCacheKey(clip.Text, voice, rate)
    cachePath := filepath.Join(cacheDir, key)
    os.MkdirAll(cacheDir, 0o755)
    // 复制 path → cachePath（或直接 Rename 如果同 volume）
    copyFile(path, cachePath)
}
return path, nil
```

#### 2.4 缓存淘汰

新增 `voiceCacheCleanup` goroutine（启动时 + 每小时一次）：
```go
func voiceCacheCleanup(dir string, maxMB int, ttl time.Duration) {
    entries, _ := os.ReadDir(dir)
    type fileInfo struct { path string; size int64; mtime time.Time }
    var files []fileInfo
    var totalSize int64
    now := time.Now()

    for _, e := range entries {
        info, _ := e.Info()
        f := fileInfo{filepath.Join(dir, e.Name()), info.Size(), info.ModTime()}
        if now.Sub(f.mtime) > ttl {
            os.Remove(f.path) // 过期删
            continue
        }
        files = append(files, f)
        totalSize += f.size
    }

    // 超大小限制 → 按 mtime 升序删最旧
    if maxBytes := int64(maxMB) * 1024 * 1024; totalSize > maxBytes {
        sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
        for _, f := range files {
            if totalSize <= maxBytes { break }
            os.Remove(f.path)
            totalSize -= f.size
        }
    }
}
```

**启动位置：`internal/bot/service.go` NewWithRuntime 或 `cmd/cinlan-qq-bot/main.go` voice 初始化附近。**

### 注意事项
- Synthesize 返回的 path 是临时文件（如 `runtime/voice/xxx.mp3`），缓存目录要和它分开（避免被语音临时文件清理误删）
- 缓存只对文本→语音有效，不缓存唱歌（歌词通常不重复,且每次想要"不一样的表演"）——可通过 `clip.Singing` 标志跳过缓存
- 确认 `provider.Synthesize` 的返回路径的生命周期——发送后是否会被清理?如果会,缓存必须是 copy 不是 rename

### 测试
- 单测 `voiceCacheKey` 的确定性（同输入同输出）
- 单测淘汰逻辑（超 TTL 删、超大小删最旧）
- 集成：同一文本二次调用不触发 Synthesize（mock provider 计数）

---

## 三、LLM 调用失败重试（项目8）

### 问题
`stageAgent` 中 LLM 调用失败一次即走 fallback（"发呆了"）。瞬时网络抖动/503 不应直接放弃。

### 改法

**落点：`internal/bot/pipeline.go` stageAgent 中 agent/provider 调用段（约 :1270-1310，找 `response, err` 赋值处）。**

当前结构大致是：
```go
if s.providers != nil {
    response, err = s.providers.ReplyWith(ctx, providerName, request)
} else if s.agent != nil {
    response, err = s.agent.Reply(ctx, request)
}
if err != nil {
    state.agentErr = err
    // → fallback
}
```

改为：
```go
var response domain.AgentResponse
var err error
const maxRetries = 1

for attempt := 0; attempt <= maxRetries; attempt++ {
    if s.providers != nil {
        if providerName != "" {
            response, err = s.providers.ReplyWith(agentCtx, providerName, request)
        } else {
            response, err = s.providers.Reply(agentCtx, request)
        }
    } else if s.agent != nil {
        response, err = s.agent.Reply(agentCtx, request)
    } else {
        err = errors.New("no provider available")
        break
    }
    if err == nil {
        break
    }
    // 不重试的错误类型：context cancelled（用户主动取消）、安全拒绝
    if ctx.Err() != nil {
        break
    }
    if attempt < maxRetries {
        s.logger.Warn("agent call failed, retrying",
            "attempt", attempt+1,
            "error", err.Error(),
            "chat", state.chatType+":"+state.chatID,
        )
        // 短退避：500ms
        select {
        case <-time.After(500 * time.Millisecond):
        case <-ctx.Done():
        }
    }
}
if err != nil {
    state.agentErr = err
    // 原有 fallback 逻辑不变
}
```

### 注意事项
- `agentCtx` 的超时：重试会消耗原始 ctx 的时间预算。如果原来 timeout 是 30s，重试一次等 500ms 后再跑一次,实际给第二次只剩约 29s——可接受
- **不要给 reflect/proactive/attention/moderation 的 LLM 调用加重试**——那些是后台/判断类调用,失败静默即可,重试只浪费成本。只给用户可见的主对话重试
- `maxRetries = 1` 写成常量,后续可配置化(`QQ_AGENT_MAX_RETRIES`)

### 测试
- 在 `service_test.go` 加用例：agent 第一次返回 error、第二次返回正常 → 应收到正常回复
- agent 两次都 error → 应走 fallback
- ctx 已取消 → 不重试

---

## 四、未识别 `[[]]` 标记 strip（项目3'）

### 问题
模型幻觉输出 `[[SECRET:xxx]]` / `[[SYSTEM:yyy]]` 等未匹配任何已知 parser 的标记 → 原样发给用户,暴露内部标记命名空间。

### 改法

**落点：`internal/bot/pipeline.go` stageDecorate 或 stageRespond，在所有已知标记解析完之后、发送前。**

找到最后一个标记处理（大约在 `parseFaceMarkers` 调用之后，:1709 附近）。

新增：
```go
// Strip unrecognized [[...]] markers that no parser consumed — these
// are model hallucinations (e.g. [[SECRET:xxx]]) that must not reach
// the user. Known markers are already consumed by extractVoiceMarkers,
// extractAffinityDelta, parseStoredImageMarker, parseFaceMarkers, etc.
var unrecognizedMarkerPattern = regexp.MustCompile(`\[\[[A-Z_]+(?::[^\]]{0,100})?\]\]`)

func stripUnrecognizedMarkers(text string) string {
    return strings.TrimSpace(unrecognizedMarkerPattern.ReplaceAllString(text, ""))
}
```

**调用点**：在 state.reply 的所有已知标记提取完毕后、发送前：
```go
// 现有流程：
// 1. extractAffinityDelta  → 剥 [[AFFINITY]]
// 2. extractVoiceMarkers   → 剥 [[VOICE]]/[[SING]]
// 3. planReplyParts         → 处理 [[NEXT]]
// 4. 各 parse*Marker        → 处理 [[IMG]]/[[MEME]]/[[SELFIE]]/[[REACT]]/[[POKE]]/[[DICE]]/[[RPS]]/[[OOPS]]/[[AT]]
// 5. parseFaceMarkers       → 处理 [表情:xxx]
// → 新增: stripUnrecognizedMarkers

// 在每个 reply part 发送前：
part = stripUnrecognizedMarkers(part)
if strings.TrimSpace(part) == "" {
    continue // 整个 part 被 strip 干净了就跳过
}
```

### 注意事项
- 正则只匹配 `[[大写字母_:可选内容]]` 格式——不会误删 `[[小写]]` 或 `[单方括号]`
- **不能在 extractVoiceMarkers 之前调**——否则会把 `[[VOICE]]` 删掉
- 要在**所有已知标记处理完之后**调——漏网的才是未识别的
- 如果模型合法输出双方括号文本（极罕见），会被误删——可接受,比泄漏好

### 测试
- `stripUnrecognizedMarkers("你好[[SECRET:abc]]再见")` → `"你好再见"`
- `stripUnrecognizedMarkers("正常文本")` → `"正常文本"`
- `stripUnrecognizedMarkers("[[A]][[B:xyz]]全删")` → `"全删"`
- 已知标记不该被删（在它之前就被各 extract/parse 处理掉了，到这步不会剩）

---

## 五、顺手可做的运维步骤（不涉及代码）

### 5.1 服务器配 judge provider
```bash
# 1. providers.json 注册便宜 provider
# 在服务器 data/providers.json 的 providers 数组里加一条：
{
  "name": "jev",
  "kind": "openai",
  "api_url": "https://api.deepseek.com/v1",
  "model": "deepseek-chat",
  "api_key": "你的 deepseek key"
}

# 2. .env 加：
JUDGE_PROVIDER=jev

# 3. 重启
systemctl restart cinlan-qq-bot
```

### 5.2 出站违禁词表初始外挂文件
```bash
# 在服务器 data/outbound-banned-words.txt 写入额外词（基础表已硬编码,这里是补充）
# 一行一个词，支持热更新(30s mtime 检查)
# 示例：
加微信
扫码领取
```

---

## 六、交付检查清单

- [ ] History 折叠：foldSpam 函数 + pipeline 调用 + 单测
- [ ] TTS 缓存：config 新增 3 字段 + voiceCacheKey + sendVoiceClips 包缓存层 + 淘汰 goroutine + 单测
- [ ] LLM 重试：stageAgent 重试循环 + 单测
- [ ] 标记 strip：unrecognizedMarkerPattern + stripUnrecognizedMarkers + 调用点 + 单测
- [ ] qq-bot `go build ./...` + `go test ./...` 全绿
- [ ] cinlan-bot core `go build ./...` + `go test ./...` 全绿
- [ ] adapters `go build ./cmd/...` 全绿（如涉及 config 改动需确认）
- [ ] 提交 qq-bot（1-2 个 commit）
- [ ] 镜像提交 cinlan-bot
- [ ] 交叉编译 `GOOS=linux GOARCH=amd64 go build -o cinlan-qq-bot-linux ./cmd/cinlan-qq-bot`
- [ ] 部署上线 + `systemctl is-active` 确认
