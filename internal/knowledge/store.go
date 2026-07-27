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
	"unicode/utf8"

	"github.com/lianyorker/cinlan-qq-bot/internal/plugin"
)

const (
	PromptContextKey = "agent.prompt_context"
	maxDocumentBytes = 256 << 10
	maxSnippetRunes  = 600
)

type Document struct {
	ID      string
	Title   string
	Source  string
	Content string
}

type Hit struct {
	DocumentID string  `json:"document_id"`
	Title      string  `json:"title"`
	Source     string  `json:"source"`
	Snippet    string  `json:"snippet"`
	Score      float64 `json:"score"`
}

type indexedDocument struct {
	Document
	terms  map[string]int
	length int
}

type Store struct {
	mu   sync.RWMutex
	docs map[string]indexedDocument
}

func NewStore() *Store {
	return &Store{docs: make(map[string]indexedDocument)}
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
	terms := tokenize(document.Title + "\n" + document.Content)
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
	}
	s.mu.Unlock()
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
	if limit <= 0 {
		return nil
	}
	queryTerms := tokenize(query)
	if len(queryTerms) == 0 {
		return nil
	}
	s.mu.RLock()
	docs := make([]indexedDocument, 0, len(s.docs))
	for _, document := range s.docs {
		docs = append(docs, document)
	}
	s.mu.RUnlock()
	if len(docs) == 0 {
		return nil
	}

	averageLength := 0.0
	for _, document := range docs {
		averageLength += float64(document.length)
	}
	averageLength /= float64(len(docs))
	type scored struct {
		hit Hit
	}
	results := make([]scored, 0, len(docs))
	for _, document := range docs {
		score := bm25(document, queryTerms, docs, averageLength)
		if score <= 0 {
			continue
		}
		results = append(results, scored{hit: Hit{
			DocumentID: document.ID,
			Title:      document.Title,
			Source:     document.Source,
			Snippet:    snippet(document.Content, queryTerms),
			Score:      score,
		}})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].hit.Score == results[j].hit.Score {
			return results[i].hit.DocumentID < results[j].hit.DocumentID
		}
		return results[i].hit.Score > results[j].hit.Score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	hits := make([]Hit, len(results))
	for index, result := range results {
		hits[index] = result.hit
	}
	return hits
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
		if extension != ".md" && extension != ".markdown" && extension != ".txt" {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		if info.Size() > maxDocumentBytes {
			return fmt.Errorf("knowledge file %q exceeds %d bytes", path, maxDocumentBytes)
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
			ID:      filepath.ToSlash(relative),
			Title:   title,
			Source:  filepath.ToSlash(relative),
			Content: content,
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
	hits := p.Store.Search(event.Text, topK)
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

func bm25(document indexedDocument, query []string, all []indexedDocument, averageLength float64) float64 {
	const (
		k1 = 1.2
		b  = 0.75
	)
	score := 0.0
	seen := make(map[string]struct{}, len(query))
	for _, term := range query {
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		tf := document.terms[term]
		if tf == 0 {
			continue
		}
		df := 0
		for _, candidate := range all {
			if candidate.terms[term] > 0 {
				df++
			}
		}
		idf := 1.0
		if df > 0 {
			idf = mathLog((float64(len(all))-float64(df)+0.5)/(float64(df)+0.5) + 1)
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
