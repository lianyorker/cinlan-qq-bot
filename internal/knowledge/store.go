package knowledge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lianyorker/cinlan-qq-bot/internal/plugin"
	"github.com/lianyorker/cinlan-qq-bot/internal/vecstore"
)

const (
	PromptContextKey = "agent.prompt_context"
	maxDocumentBytes = 256 << 10
	maxSnippetRunes  = 600
	// remoteArmSkipScore: when the best local (BM25+concept) score already
	// clears this bar the remote embedding arm is skipped entirely — the
	// dense vector would only reshuffle an already-confident ranking.
	remoteArmSkipScore = 2.5
	remoteArmTimeout   = 1200 * time.Millisecond
)

type Document struct {
	ID         string `json:"id"`
	Collection string `json:"collection"`
	Title      string `json:"title"`
	Source     string `json:"source"`
	Content    string `json:"content"`
}

type Hit struct {
	DocumentID string  `json:"document_id"`
	Collection string  `json:"collection"`
	Title      string  `json:"title"`
	Source     string  `json:"source"`
	Snippet    string  `json:"snippet"`
	Score      float64 `json:"score"`
}

type indexedDocument struct {
	Document
	terms    map[string]int
	length   int
	localVec []float32
	// embed is the stable text fed to the remote embedder for this doc.
	// Precomputed at index time — rebuilding it per query meant slicing
	// a 600-rune string (and re-hashing it inside CachedVector) for every
	// candidate on every search.
	embed string
}

type Store struct {
	mu   sync.RWMutex
	docs map[string]indexedDocument
	vec  *vecstore.Engine
}

func NewStore() *Store {
	return &Store{docs: make(map[string]indexedDocument)}
}

// SetVecEngine attaches an optional dual-mode vector engine for hybrid BM25 +
// dense embedding retrieval. Already-indexed documents are warmed so the
// remote cosine arm is hot regardless of engine wiring order.
func (s *Store) SetVecEngine(v *vecstore.Engine) {
	s.mu.Lock()
	s.vec = v
	if v != nil && v.RemoteEnabled() {
		for _, document := range s.docs {
			v.WarmAsync(embedText(document.Document))
		}
	}
	s.mu.Unlock()
}

// embedText is the stable text a document is embedded with — title plus a
// fixed head slice, independent of the query. Query-dependent windows
// (snippet at the keyword hit) change the cache key every search and feed
// the embedder mid-sentence fragments; a fixed prefix keeps cache hits and
// coherent semantics.
func embedText(document Document) string {
	runes := []rune(strings.TrimSpace(document.Content))
	if len(runes) > maxSnippetRunes {
		runes = runes[:maxSnippetRunes]
	}
	return document.Title + "\n" + string(runes)
}

func (s *Store) Add(document Document) error {
	document.ID = strings.TrimSpace(document.ID)
	if document.ID == "" {
		return errors.New("knowledge document ID is empty")
	}
	document.Title = strings.TrimSpace(document.Title)
	if document.Title == "" {
		document.Title = document.ID
	}
	document.Content = strings.TrimSpace(document.Content)
	if document.Content == "" {
		return errors.New("knowledge document content is empty")
	}
	document.Collection = strings.TrimSpace(document.Collection)
	if document.Collection == "" {
		document.Collection = "default"
	}
	fullText := document.Title + "\n" + document.Content
	terms := tokenize(fullText)
	if len(terms) == 0 {
		return errors.New("knowledge document has no searchable terms")
	}
	index := make(map[string]int, len(terms))
	for _, term := range terms {
		index[term]++
	}
	s.mu.Lock()
	s.docs[document.ID] = indexedDocument{
		Document: document,
		terms:    index,
		length:   len(terms),
		localVec: vecstore.LocalVector(fullText),
		embed:    embedText(document),
	}
	vec := s.vec
	s.mu.Unlock()
	// Warm the remote embedding at index time on the stable text so the
	// search hot path can read the dense arm from cache only — remote
	// calls happen once per document, not once per query per document.
	if vec != nil {
		vec.WarmAsync(embedText(document))
	}
	return nil
}

func (s *Store) Remove(id string) {
	s.mu.Lock()
	delete(s.docs, id)
	s.mu.Unlock()
}

func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.docs)
}

func (s *Store) Search(query string, limit int) []Hit {
	return s.search(query, limit, nil)
}

func (s *Store) Collections() []string {
	s.mu.RLock()
	seen := make(map[string]struct{}, len(s.docs))
	for _, document := range s.docs {
		seen[document.Collection] = struct{}{}
	}
	s.mu.RUnlock()
	result := make([]string, 0, len(seen))
	for collection := range seen {
		result = append(result, collection)
	}
	sort.Strings(result)
	return result
}

func (s *Store) SearchScoped(query string, limit int, collections []string) []Hit {
	allowed := make(map[string]struct{}, len(collections))
	for _, collection := range collections {
		collection = strings.TrimSpace(collection)
		if collection != "" {
			allowed[collection] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil
	}
	if len(allowed) == 1 {
		return s.search(query, limit, allowed)
	}
	// Multi-collection scope: rank each collection separately and
	// interleave, ordered by each collection's best score. A large
	// corpus (e.g. a source tree) otherwise drowns a small doc set
	// out of every top-k slot under global BM25.
	perCollection := make([][]Hit, 0, len(allowed))
	for collection := range allowed {
		hits := s.search(query, limit, map[string]struct{}{collection: {}})
		if len(hits) > 0 {
			perCollection = append(perCollection, hits)
		}
	}
	sort.Slice(perCollection, func(i, j int) bool {
		return perCollection[i][0].Score > perCollection[j][0].Score
	})
	merged := make([]Hit, 0, limit)
	for round := 0; len(merged) < limit; round++ {
		advanced := false
		for _, hits := range perCollection {
			if round < len(hits) {
				merged = append(merged, hits[round])
				advanced = true
				if len(merged) == limit {
					break
				}
			}
		}
		if !advanced {
			break
		}
	}
	return merged
}

func (s *Store) search(
	query string,
	limit int,
	allowed map[string]struct{},
) []Hit {
	if limit <= 0 {
		return nil
	}
	queryTerms := tokenize(query)
	if len(queryTerms) == 0 {
		return nil
	}
	s.mu.RLock()
	vecEng := s.vec
	docs := make([]indexedDocument, 0, len(s.docs))
	for _, document := range s.docs {
		if allowed != nil {
			if _, ok := allowed[document.Collection]; !ok {
				continue
			}
		}
		docs = append(docs, document)
	}
	s.mu.RUnlock()
	if len(docs) == 0 {
		return nil
	}

	queryTerms = uniqueTerms(queryTerms)
	averageLength := 0.0
	for _, document := range docs {
		averageLength += float64(document.length)
	}
	averageLength /= float64(len(docs))
	queryVec := vecstore.LocalVector(query)

	// Document frequency per term, computed once over the scoped set.
	// The old code rescanned every document for every term inside the
	// per-document scoring loop — O(N²·T) map lookups per message.
	df := make(map[string]int, len(queryTerms))
	for _, term := range queryTerms {
		count := 0
		for _, document := range docs {
			if document.terms[term] > 0 {
				count++
			}
		}
		df[term] = count
	}

	type scored struct {
		doc   indexedDocument
		score float64
	}
	results := make([]scored, 0, len(docs))
	best := 0.0
	for _, document := range docs {
		score := bm25(document, queryTerms, df, len(docs), averageLength)
		if cos := vecstore.Cosine(queryVec, document.localVec); cos >= 0.18 {
			score += cos * 1.5
		}
		if score > best {
			best = score
		}
		results = append(results, scored{doc: document, score: score})
	}

	// Remote dense arm, adaptive: a confident local hit skips the
	// per-search embedding HTTP call entirely (the query is still warmed
	// so a later ambiguous phrasing finds the cache hot). Weak local
	// retrieval is exactly where the dense arm earns its keep, so only
	// then do we spend up to remoteArmTimeout on the query vector.
	if vecEng != nil && vecEng.RemoteEnabled() {
		if best >= remoteArmSkipScore {
			vecEng.WarmAsync(query)
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), remoteArmTimeout)
			queryRemoteVec, _ := vecEng.RemoteVector(ctx, query)
			cancel()
			if len(queryRemoteVec) > 0 {
				for index := range results {
					// Cache-only doc arm: docs were warmed at index time on
					// their stable embedText, so this never hits the network.
					if docRemote, ok := vecEng.CachedVector(results[index].doc.embed); ok && len(docRemote) == len(queryRemoteVec) {
						if rcos := vecstore.Cosine(queryRemoteVec, docRemote); rcos >= 0.35 {
							results[index].score += rcos * 3.0
						}
					}
				}
			}
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].score == results[j].score {
			return results[i].doc.ID < results[j].doc.ID
		}
		return results[i].score > results[j].score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	// Snippets only materialize for the top-K winners — building them
	// per candidate meant a full rune slice of every document's content
	// on every search.
	hits := make([]Hit, 0, len(results))
	for _, result := range results {
		if result.score <= 0 {
			continue
		}
		hits = append(hits, Hit{
			DocumentID: result.doc.ID,
			Collection: result.doc.Collection,
			Title:      result.doc.Title,
			Source:     result.doc.Source,
			Snippet:    snippet(result.doc.Content, queryTerms),
			Score:      result.score,
		})
	}
	return hits
}

// uniqueTerms dedupes query terms once — callers pass a fresh tokenize()
// slice so the in-place filter is safe.
func uniqueTerms(terms []string) []string {
	seen := make(map[string]struct{}, len(terms))
	out := terms[:0]
	for _, term := range terms {
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		out = append(out, term)
	}
	return out
}

func (s *Store) LoadDir(root string) (int, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return 0, nil
	}
	count := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		extension := strings.ToLower(filepath.Ext(path))
		if !indexableExtension(extension) {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		if info.Size() > maxDocumentBytes {
			return nil // oversized source files are skipped, not fatal
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			relative = filepath.Base(path)
		}
		title := filepath.Base(path)
		scanner := bufio.NewScanner(strings.NewReader(content))
		if scanner.Scan() {
			first := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(first, "#") {
				title = strings.TrimSpace(strings.TrimLeft(first, "#"))
			}
		}
		if addErr := s.Add(Document{
			ID:         filepath.ToSlash(relative),
			Collection: documentCollection(relative),
			Title:      title,
			Source:     filepath.ToSlash(relative),
			Content:    content,
		}); addErr != nil {
			return addErr
		}
		count++
		return nil
	})
	return count, err
}

type Plugin struct {
	Store *Store
	TopK  int
}

func (p Plugin) Name() string {
	return "knowledge"
}

func (p Plugin) BeforeMessage(_ context.Context, event *plugin.MessageContext) (plugin.Decision, error) {
	if p.Store == nil {
		return plugin.Decision{}, nil
	}
	topK := p.TopK
	if topK <= 0 {
		topK = 3
	}
	collections, _ := event.Values["scope.knowledge_bases"].([]string)
	hits := p.Store.SearchScoped(event.Text, topK, collections)
	if len(hits) == 0 {
		return plugin.Decision{}, nil
	}
	var builder strings.Builder
	for index, hit := range hits {
		if index > 0 {
			builder.WriteString("\n\n")
		}
		fmt.Fprintf(&builder, "[资料 %d] %s\n%s", index+1, hit.Title, hit.Snippet)
	}
	contextText := builder.String()
	if utf8.RuneCountInString(contextText) > 5000 {
		runes := []rune(contextText)
		contextText = string(runes[:5000])
	}
	if event.Values == nil {
		event.Values = make(map[string]any)
	}
	event.Values[PromptContextKey] = contextText
	return plugin.Decision{}, nil
}

// indexableExtension reports whether a file extension should be indexed for
// retrieval: docs plus common source/config formats.
func indexableExtension(ext string) bool {
	switch ext {
	case ".md", ".markdown", ".txt",
		".java", ".kt", ".go", ".py", ".js", ".ts", ".tsx", ".jsx",
		".vue", ".sql", ".xml", ".yml", ".yaml", ".properties", ".json",
		".html", ".css", ".scss", ".less", ".sh", ".bat", ".gradle", ".toml",
		".ini", ".conf", ".cfg", ".proto", ".thrift", ".mdx":
		return true
	}
	return false
}

func documentCollection(relative string) string {
	relative = filepath.ToSlash(strings.TrimSpace(relative))
	if relative == "" || !strings.Contains(relative, "/") {
		return "default"
	}
	return strings.SplitN(relative, "/", 2)[0]
}

func bm25(document indexedDocument, query []string, df map[string]int, nDocs int, averageLength float64) float64 {
	const (
		k1 = 1.2
		b  = 0.75
	)
	score := 0.0
	for _, term := range query {
		tf := document.terms[term]
		if tf == 0 {
			continue
		}
		docFreq := df[term]
		idf := 1.0
		if docFreq > 0 {
			idf = mathLog((float64(nDocs)-float64(docFreq)+0.5)/(float64(docFreq)+0.5) + 1)
		}
		denominator := float64(tf) + k1*(1-b+b*float64(document.length)/averageLength)
		score += idf * (float64(tf) * (k1 + 1) / denominator)
	}
	return score
}

func tokenize(value string) []string {
	value = strings.ToLower(value)
	var terms []string
	var ascii strings.Builder
	flushASCII := func() {
		if ascii.Len() > 0 {
			terms = append(terms, ascii.String())
			ascii.Reset()
		}
	}
	runes := []rune(value)
	for index, current := range runes {
		if (current >= 'a' && current <= 'z') || (current >= '0' && current <= '9') {
			ascii.WriteRune(current)
			continue
		}
		flushASCII()
		if current >= 0x4e00 && current <= 0x9fff {
			terms = append(terms, string(current))
			if index+1 < len(runes) && runes[index+1] >= 0x4e00 && runes[index+1] <= 0x9fff {
				terms = append(terms, string([]rune{current, runes[index+1]}))
			}
		}
	}
	flushASCII()
	terms = append(terms, vecstore.ExpandQueryTerms(value)...)
	return terms
}

func snippet(content string, terms []string) string {
	runes := []rune(strings.TrimSpace(content))
	if len(runes) <= maxSnippetRunes {
		return string(runes)
	}
	best := 0
	lower := strings.ToLower(content)
	for _, term := range terms {
		if index := strings.Index(lower, term); index >= 0 {
			best = len([]rune(content[:index]))
			break
		}
	}
	start := best - maxSnippetRunes/4
	if start < 0 {
		start = 0
	}
	end := start + maxSnippetRunes
	if end > len(runes) {
		end = len(runes)
	}
	return string(runes[start:end])
}

// Kept local to avoid bringing a third-party math dependency into the
// knowledge package's public API.
func mathLog(value float64) float64 {
	return math.Log(value)
}
