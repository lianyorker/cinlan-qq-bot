// Package vecstore provides a dual-engine vector & semantic retrieval store:
//
//  1. Local Semantic Engine (zero external model): projects Chinese/English text
//     into a 256-dim L2-normalized vector combining 32 curated daily-life semantic
//     concept clusters (sleep/work/mood/health/food/drink/tech/pet/etc.) with
//     signed random-projection CJK n-gram hashing. Cross-word queries like
//     "天天熬夜写代码" <-> "睡眠不足好累想休息" match without an embedding API.
//  2. Remote Embedding Engine (OpenAI-compatible /v1/embeddings): when configured
//     via EMBEDDING_API_URL, computes dense embeddings with on-disk SHA-256 cache
//     and falls back to the local engine transparently on timeout or when unset.
package vecstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	localDim       = 256
	conceptDim     = 64
	ngramDim       = localDim - conceptDim
	conceptBoost   = 2.6
	ngramWeight    = 1.0
	defaultTimeout = 6 * time.Second
	maxCacheItems  = 6000
)

// Config configures the optional remote OpenAI-compatible embedding client
// and on-disk vector cache. When APIURL is empty, Engine runs in pure local
// semantic vector mode with zero network calls.
type Config struct {
	APIURL    string
	APIKey    string
	Model     string
	CachePath string
	Timeout   time.Duration
}

// Engine computes, caches, and compares semantic vectors using the remote
// embedding API when configured and the built-in local concept+ngram vector
// space at all times.
type Engine struct {
	cfg        Config
	httpClient *http.Client

	mu    sync.RWMutex
	cache map[string][]float32
	dirty int
	// cacheDim records the vector dimension the cache was built with.
	// A model swap that changes dims silently invalidates stored
	// vectors — comparing against cacheDim catches that and purges
	// instead of blending mismatched spaces.
	cacheDim int
}

// ScoredItem represents a ranked item returned by TopK search.
type ScoredItem struct {
	Index int
	ID    string
	Score float64
}

// New creates a dual-mode vector engine and loads any persisted embedding cache.
func New(cfg Config) *Engine {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = "text-embedding-3-small"
	}
	e := &Engine{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: cfg.Timeout},
		cache:      make(map[string][]float32),
	}
	if cfg.CachePath != "" {
		_ = e.loadCache()
	}
	return e
}

// RemoteEnabled reports whether an external /v1/embeddings endpoint is configured.
func (e *Engine) RemoteEnabled() bool {
	return e != nil && strings.TrimSpace(e.cfg.APIURL) != ""
}

// Flush persists the remote embedding cache to disk if dirty.
func (e *Engine) Flush() error {
	if e == nil || strings.TrimSpace(e.cfg.CachePath) == "" {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dirty == 0 {
		return nil
	}
	return e.saveCacheLocked()
}

// Similarity returns the semantic similarity in [0, 1] between two texts.
// It always computes the local concept+ngram cosine similarity, and when a
// remote embedding endpoint is enabled, blends in the remote dense cosine.
func (e *Engine) Similarity(ctx context.Context, a, b string) float64 {
	va := LocalVector(a)
	vb := LocalVector(b)
	localSim := Cosine(va, vb)
	if !e.RemoteEnabled() {
		return localSim
	}
	ra, errA := e.RemoteVector(ctx, a)
	rb, errB := e.RemoteVector(ctx, b)
	if errA != nil || errB != nil || len(ra) == 0 || len(ra) != len(rb) {
		return localSim
	}
	remoteSim := Cosine(ra, rb)
	// Blend remote dense cosine with local concept cosine so explicit
	// synonym hits and deep embeddings reinforce each other.
	return 0.7*remoteSim + 0.3*localSim
}

// CachedSimilarity blends remote dense cosine ONLY when both texts
// already have cached embeddings — it never touches the network, so it
// is safe on hot paths like fact recall. Falls back to pure local
// similarity whenever either side is a cache miss.
func (e *Engine) CachedSimilarity(a, b string) float64 {
	localSim := LocalSimilarity(a, b)
	if e == nil || !e.RemoteEnabled() {
		return localSim
	}
	e.mu.RLock()
	ra, okA := e.cache[hashKey(e.cfg.Model, a)]
	rb, okB := e.cache[hashKey(e.cfg.Model, b)]
	e.mu.RUnlock()
	if !okA || !okB || len(ra) == 0 || len(ra) != len(rb) {
		return localSim
	}
	return 0.7*Cosine(ra, rb) + 0.3*localSim
}

// LocalSimilarity returns the fast, zero-IO semantic similarity in [0, 1]
// using only the built-in concept clusters + CJK n-gram projection.
func LocalSimilarity(a, b string) float64 {
	return Cosine(LocalVector(a), LocalVector(b))
}

// ConceptOverlap counts how many semantic concept clusters are shared between
// two texts — useful as a deterministic semantic boost on top of lexical match.
func ConceptOverlap(a, b string) int {
	ca := matchConcepts(a)
	if len(ca) == 0 {
		return 0
	}
	cb := matchConcepts(b)
	overlap := 0
	for id := range ca {
		if cb[id] > 0 {
			overlap++
		}
	}
	return overlap
}

// ExpandQueryTerms returns the original tokens plus canonical concept tags
// triggered by the text, letting BM25 / inverted indexes recall synonyms
// without any external vector DB.
func ExpandQueryTerms(text string) []string {
	concepts := matchConcepts(text)
	if len(concepts) == 0 {
		return nil
	}
	tags := make([]string, 0, len(concepts))
	for id := range concepts {
		tags = append(tags, fmt.Sprintf("__concept_%d__", id))
	}
	sort.Strings(tags)
	return tags
}

// LocalVector projects text into a 256-dim L2-normalized float32 vector.
// Dimensions [0..63] encode semantic concept clusters; [64..255] encode
// signed feature-hashed character unigrams, bigrams, trigrams, and ASCII words.
func LocalVector(text string) []float32 {
	vec := make([]float32, localDim)
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return vec
	}

	for id, weight := range matchConcepts(text) {
		if id >= 0 && id < conceptDim {
			vec[id] += float32(weight) * conceptBoost
		}
	}

	for _, tok := range extractFeatures(text) {
		h := fnv32a(tok)
		slot := conceptDim + int(h%uint32(ngramDim))
		sign := float32(1.0)
		if (h>>31)&1 == 1 {
			sign = -1.0
		}
		vec[slot] += sign * ngramWeight
	}

	normalizeL2(vec)
	return vec
}

// RemoteVector fetches or looks up a cached dense embedding from the configured
// OpenAI-compatible /v1/embeddings endpoint.
func (e *Engine) RemoteVector(ctx context.Context, text string) ([]float32, error) {
	if !e.RemoteEnabled() {
		return nil, errors.New("remote embedding not configured")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("empty text")
	}
	key := hashKey(e.cfg.Model, text)

	e.mu.RLock()
	if cached, ok := e.cache[key]; ok {
		e.mu.RUnlock()
		return cached, nil
	}
	e.mu.RUnlock()

	vec, err := e.fetchEmbedding(ctx, text)
	if err != nil {
		return nil, err
	}
	normalizeL2(vec)

	e.mu.Lock()
	if e.cacheDim == 0 {
		e.cacheDim = len(vec)
	} else if len(vec) != e.cacheDim {
		// The remote model started returning a different dimension
		// (silent model swap) — every stored vector is now in a foreign
		// space, so purge rather than blend garbage.
		e.cache = make(map[string][]float32)
		e.cacheDim = len(vec)
		e.dirty++
	}
	if len(e.cache) >= maxCacheItems {
		// Evict ~10% random entries when full.
		victim := maxCacheItems / 10
		for k := range e.cache {
			delete(e.cache, k)
			victim--
			if victim <= 0 {
				break
			}
		}
	}
	e.cache[key] = vec
	e.dirty++
	shouldSave := e.dirty >= 8 && e.cfg.CachePath != ""
	if shouldSave {
		_ = e.saveCacheLocked()
	}
	e.mu.Unlock()
	return vec, nil
}

func (e *Engine) fetchEmbedding(ctx context.Context, text string) ([]float32, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(e.cfg.APIURL), "/")
	if !strings.HasSuffix(endpoint, "/embeddings") {
		if strings.HasSuffix(endpoint, "/v1") {
			endpoint += "/embeddings"
		} else {
			endpoint += "/v1/embeddings"
		}
	}
	body, err := json.Marshal(map[string]any{
		"model": e.cfg.Model,
		"input": text,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey := strings.TrimSpace(e.cfg.APIKey); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("embedding http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	if len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) == 0 {
		return nil, errors.New("empty embedding response")
	}
	return parsed.Data[0].Embedding, nil
}

// Cosine computes the cosine similarity between two L2-normalized vectors.
func Cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	if dot <= 0 {
		return 0
	}
	if dot > 1 {
		return 1
	}
	return float64(dot)
}

func normalizeL2(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum <= 1e-12 {
		return
	}
	inv := float32(1.0 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
}

func fnv32a(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

func hashKey(model, text string) string {
	sum := sha256.Sum256([]byte(model + "\x00" + text))
	return hex.EncodeToString(sum[:16])
}

type persistedCache struct {
	Model   string               `json:"model"`
	Vectors map[string][]float32 `json:"vectors"`
}

func (e *Engine) loadCache() error {
	raw, err := os.ReadFile(e.cfg.CachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var p persistedCache
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if p.Model != "" && p.Model != e.cfg.Model {
		return nil
	}
	if p.Vectors != nil {
		// Keep only vectors that share one consistent dimension — a
		// mixed-dim cache means the model changed under us.
		dim := 0
		for _, v := range p.Vectors {
			if len(v) > 0 {
				dim = len(v)
				break
			}
		}
		for k, v := range p.Vectors {
			if dim > 0 && len(v) != dim {
				delete(p.Vectors, k)
			}
		}
		e.cache = p.Vectors
		e.cacheDim = dim
	}
	return nil
}

func (e *Engine) saveCacheLocked() error {
	if e.cfg.CachePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(e.cfg.CachePath), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(persistedCache{
		Model:   e.cfg.Model,
		Vectors: e.cache,
	})
	if err != nil {
		return err
	}
	tmp := e.cfg.CachePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	e.dirty = 0
	return os.Rename(tmp, e.cfg.CachePath)
}

func extractFeatures(text string) []string {
	runes := []rune(text)
	var out []string
	var word strings.Builder
	isCJK := func(r rune) bool {
		return r >= 0x4e00 && r <= 0x9fff
	}
	flushWord := func() {
		if w := word.String(); len(w) >= 2 {
			out = append(out, "w:"+w)
		}
		word.Reset()
	}
	var cjkRun []rune
	flushCJK := func() {
		n := len(cjkRun)
		for i := 0; i < n; i++ {
			out = append(out, "c1:"+string(cjkRun[i]))
			if i+1 < n {
				out = append(out, "c2:"+string(cjkRun[i:i+2]))
			}
			if i+2 < n {
				out = append(out, "c3:"+string(cjkRun[i:i+3]))
			}
		}
		cjkRun = cjkRun[:0]
	}
	for _, r := range runes {
		if isCJK(r) {
			flushWord()
			cjkRun = append(cjkRun, r)
			continue
		}
		flushCJK()
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			word.WriteRune(r)
			continue
		}
		flushWord()
	}
	flushWord()
	flushCJK()
	return out
}

// semanticClusters maps 32 daily-life & technical domains to synonym roots.
// Hitting any root in cluster i activates concept dimension i, allowing
// zero-model cross-word semantic recall.
var semanticClusters = [][]string{
	// 0: 睡眠 / 熬夜 / 疲惫 / 作息
	{"熬夜", "通宵", "失眠", "睡眠", "睡觉", "困", "累", "疲惫", "休息", "早睡", "晚睡", "起床", "午睡", "补觉", "作息", "黑眼圈", "打盹"},
	// 1: 工作 / 加班 / 赶工 / 职场
	{"加班", "赶工", "上班", "下班", "工作", "开会", "老板", "同事", "项目", "上线", "工期", "忙", "周报", "绩效", "出差", "通勤", "打工"},
	// 2: 编程 / 代码 / 技术开发
	{"代码", "写码", "编程", "开发", "调试", "bug", "报错", "部署", "服务器", "接口", "api", "数据库", "前端", "后端", "算法", "重构", "编译"},
	// 3: 咖啡 / 饮品 / 奶茶
	{"咖啡", "美式", "拿铁", "瑞幸", "星巴克", "冰美式", "生椰", "奶茶", "果茶", "霸王茶姬", "喜茶", "浓缩", "咖啡因", "喝茶", "饮料"},
	// 4: 肠胃 / 消化 / 忌口
	{"胃", "胃痛", "胃疼", "胃病", "胃药", "肚子疼", "拉肚子", "肠胃", "消化", "反酸", "护胃", "空腹", "忌口", "怕冷", "怕苦", "过敏"},
	// 5: 身体 / 生病 / 医疗健康
	{"感冒", "发烧", "头疼", "头痛", "咳嗽", "嗓子", "生病", "不舒服", "医院", "看病", "吃药", "体检", "身体", "退烧", "发炎", "健康"},
	// 6: 负向情绪 / 低落 / 压力
	{"难过", "低落", "郁闷", "烦躁", "焦虑", "委屈", "崩溃", "压力", "心烦", "不开心", "丧", "叹气", "emo", "破防", "伤心", "沮丧", "生气"},
	// 7: 正向情绪 / 开心 / 庆祝
	{"开心", "高兴", "快乐", "兴奋", "爽", "幸福", "惊喜", "期待", "哈哈", "满足", "顺利", "好消息", "庆祝"},
	// 8: 饮食 / 正餐 / 夜宵 / 做饭
	{"吃饭", "外卖", "午饭", "晚饭", "早饭", "夜宵", "烧烤", "火锅", "面条", "米饭", "做饭", "下厨", "饿", "饱", "好吃", "餐厅", "食堂"},
	// 9: 甜品 / 零食 / 口味
	{"甜品", "蛋糕", "冰淇淋", "巧克力", "零食", "薯片", "奶茶", "甜", "辣", "酸", "苦", "咸", "少糖", "无糖", "去冰"},
	// 10: 运动 / 健身 / 手表体征
	{"运动", "健身", "跑步", "撸铁", "减肥", "体重", "步数", "心率", "血氧", "手表", "华为手表", "手环", "卡路里", "锻炼", "游泳", "打球"},
	// 11: 游戏 / 电竞 / 娱乐
	{"游戏", "打游戏", "开黑", "上分", "排位", "原神", "王者", "联盟", "lol", "steam", "通关", "抽卡", "副本", "坐牢", "连跪", "吃鸡"},
	// 12: 宠物 / 猫狗
	{"猫", "猫咪", "小猫", "狗", "小狗", "宠物", "铲屎", "猫粮", "撸猫", "遛狗", "喵", "汪", "兽医"},
	// 13: 天气 / 温度 / 穿搭
	{"下雨", "暴雨", "晴天", "降温", "冷", "热", "刮风", "台风", "下雪", "天气", "带伞", "外套", "秋裤", "空调", "暖气"},
	// 14: 出行 / 旅游 / 交通
	{"旅游", "旅行", "出门", "高铁", "飞机", "机票", "酒店", "地铁", "打车", "堵车", "自驾", "景点", "放假", "周末", "假期"},
	// 15: 金钱 / 消费 / 购物
	{"买", "下单", "快递", "淘宝", "京东", "花钱", "省钱", "工资", "发薪", "报销", "预算", "贵", "便宜", "打折", "退款", "账单"},
	// 16: 影视 / 音乐 / 动漫
	{"电影", "看剧", "追剧", "动漫", "番剧", "音乐", "听歌", "歌单", "演唱会", "小说", "看书", "b站", "抖音"},
	// 17: 生日 / 节日 / 纪念日
	{"生日", "礼物", "纪念日", "过年", "春节", "中秋", "国庆", "圣诞", "跨年", "情人节", "祝福", "蜡烛", "许愿"},
	// 18: 家人 / 亲属 / 家务
	{"爸", "妈", "父母", "家里", "回家", "老家", "亲戚", "打扫", "洗衣服", "搬家", "租房", "房东", "室友"},
	// 19: 学习 / 考试 / 论文
	{"学习", "考试", "复习", "论文", "毕业", "作业", "课程", "考研", "考公", "英语", "单词", "刷题", "图书馆"},
	// 20: 商城 / 订单 / 售前售后 (zkmall / 业务知识库)
	{"商城", "订单", "发货", "物流", "售后", "退货", "换货", "客服", "优惠券", "会员", "积分", "支付", "付款", "库存", "上架", "价格"},
	// 21: AI / 模型 / 提示词 / 机器人
	{"模型", "ai", "人工智能", "claude", "gpt", "gemini", "deepseek", "提示词", "prompt", "机器人", "bot", "token", "上下文", "记忆", "向量"},
	// 22: 摄影 / 照片 / 画图
	{"拍照", "照片", "自拍", "画图", "生图", "壁纸", "头像", "好看", "穿搭", "裙子", "发型", "相机", "风景"},
	// 23: 时间近期 / 频率状态
	{"最近", "这几天", "经常", "老是", "总是", "天天", "每次", "习惯", "一直", "从来"},
}

func matchConcepts(text string) map[int]int {
	text = strings.ToLower(text)
	var hits map[int]int
	for clusterID, words := range semanticClusters {
		for _, w := range words {
			if strings.Contains(text, w) {
				if hits == nil {
					hits = make(map[int]int)
				}
				hits[clusterID]++
			}
		}
	}
	return hits
}
