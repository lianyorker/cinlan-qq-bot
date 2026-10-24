package vecstore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestLocalSemanticCrossWordRecall(t *testing.T) {
	// Zero literal character overlap between memory and query,
	// but both hit the sleep/fatigue concept cluster.
	memory := "他天天熬夜通宵写代码"
	querySleep := "睡眠不足好困想早点休息"
	queryUnrelated := "今天股市大盘涨了多少"

	simSleep := LocalSimilarity(memory, querySleep)
	simUnrelated := LocalSimilarity(memory, queryUnrelated)

	if simSleep < 0.25 {
		t.Fatalf("expected cross-word sleep similarity >= 0.25, got %.4f", simSleep)
	}
	if simSleep <= simUnrelated*2 {
		t.Fatalf("expected sleep query (%.4f) >> unrelated query (%.4f)", simSleep, simUnrelated)
	}
	if ConceptOverlap(memory, querySleep) == 0 {
		t.Fatal("expected concept overlap > 0 for sleep/fatigue synonyms")
	}
}

func TestLocalSemanticStomachCoffeeRecall(t *testing.T) {
	memory := "肠胃不好，空腹喝冰美式会反酸"
	query := "肚子疼是不是该吃点胃药"
	if ConceptOverlap(memory, query) == 0 {
		t.Fatal("expected stomach concept cluster overlap")
	}
	if sim := LocalSimilarity(memory, query); sim < 0.25 {
		t.Fatalf("expected stomach similarity >= 0.25, got %.4f", sim)
	}
}

func TestRemoteEmbeddingAndDiskCache(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float32{0.6, 0.8, 0.0}},
			},
		})
	}))
	defer srv.Close()

	cacheFile := filepath.Join(t.TempDir(), "vec.json")
	eng := New(Config{
		APIURL:    srv.URL,
		APIKey:    "test-key",
		Model:     "test-embed",
		CachePath: cacheFile,
	})

	v1, err := eng.RemoteVector(context.Background(), "测试文本")
	if err != nil || len(v1) != 3 {
		t.Fatalf("RemoteVector failed: %v, len=%d", err, len(v1))
	}
	// Second lookup must hit memory cache without HTTP round-trip.
	_, err = eng.RemoteVector(context.Background(), "测试文本")
	if err != nil || calls != 1 {
		t.Fatalf("expected 1 HTTP call (cached), got %d, err=%v", calls, err)
	}
	if err := eng.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Reload from disk cache and verify zero HTTP calls.
	eng2 := New(Config{
		APIURL:    srv.URL,
		Model:     "test-embed",
		CachePath: cacheFile,
	})
	_, err = eng2.RemoteVector(context.Background(), "测试文本")
	if err != nil || calls != 1 {
		t.Fatalf("expected disk cache hit (calls=1), got %d, err=%v", calls, err)
	}
}
