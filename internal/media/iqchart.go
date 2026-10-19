package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/tool"
)

// IQChart periodically fetches codexradar model-IQ scores through the public
// summary API and renders a leaderboard PNG. The latest image is served via
// the model_iq_chart tool; only chats whose binding allows the tool get it.
type IQChart struct {
	apiBase  string
	interval time.Duration
	latest   string
	fontPath string
	client   *http.Client
	logger   *slog.Logger
	ready    atomic.Bool
}

type IQChartConfig struct {
	APIBase   string // e.g. https://codexradar.com/api/radar-bench-score
	Interval  time.Duration
	OutputDir string
	FontPath  string
	Logger    *slog.Logger
}

type scoreSummary struct {
	Model  string  `json:"model"`
	Effort string  `json:"effort"`
	Score  float64 `json:"score"`
	Cover  int     `json:"coverage"`
	Stat   struct {
		Metrics struct {
			Cost struct {
				Mean float64 `json:"mean"`
			} `json:"api_equivalent_usd"`
			Elapsed struct {
				Mean float64 `json:"mean"`
			} `json:"elapsed_ms"`
		} `json:"metrics"`
	} `json:"statistics"`
}

type iqCell struct {
	score    float64
	coverage int
	cost     float64
	minutes  float64
	ok       bool
}

// Ladder entries from the codexradar 模型 IQ 天梯 board (2026-10).
var iqModels = []string{
	"gpt-6-astra", "gpt-6.1-sol", "gpt-5.6-sol",
	"gpt-6-sol", "gpt-5.6-luna", "gpt-6-luna",
}

var iqEfforts = []string{"low", "medium", "high", "xhigh", "max", "ultra"}

func NewIQChart(cfg IQChartConfig) (*IQChart, error) {
	latest := filepath.Join(cfg.OutputDir, "model-iq-latest.png")
	abs, err := filepath.Abs(latest)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.OutputDir, 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(cfg.FontPath); err != nil {
		return nil, fmt.Errorf("iq chart font missing: %w", err)
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 90 * time.Minute
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://codexradar.com/api/radar-bench-score"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &IQChart{
		apiBase:  cfg.APIBase,
		interval: cfg.Interval,
		latest:   abs,
		fontPath: cfg.FontPath,
		client:   &http.Client{Timeout: 15 * time.Second},
		logger:   cfg.Logger,
	}, nil
}

func (c *IQChart) Run(ctx context.Context) {
	c.refresh(ctx)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.refresh(ctx)
		}
	}
}

func (c *IQChart) refresh(ctx context.Context) {
	type task struct {
		i, j   int
		model  string
		effort string
	}
	tasks := make(chan task)
	grid := make([][]iqCell, len(iqModels))
	for i := range grid {
		grid[i] = make([]iqCell, len(iqEfforts))
	}
	var (
		mu     sync.Mutex
		filled int
		wg     sync.WaitGroup
	)
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range tasks {
				cell, err := c.fetchCell(ctx, t.model, t.effort)
				if err == nil && cell.ok {
					mu.Lock()
					grid[t.i][t.j] = cell
					filled++
					mu.Unlock()
				} else if err != nil {
					c.logger.Debug("iq cell fetch failed",
						"model", t.model, "effort", t.effort, "error", err.Error())
				}
			}
		}()
	}
	for i, model := range iqModels {
		for j, effort := range iqEfforts {
			select {
			case <-ctx.Done():
				close(tasks)
				wg.Wait()
				return
			case tasks <- task{i: i, j: j, model: model, effort: effort}:
			}
		}
	}
	close(tasks)
	wg.Wait()
	if filled < len(iqModels) { // too little data to publish
		c.logger.Warn("model iq chart refresh skipped", "filled", filled)
		return
	}
	tmp := c.latest + ".tmp"
	if err := renderIQChart(grid, tmp, c.fontPath); err != nil {
		c.logger.Warn("model iq chart render failed", "error", err.Error())
		return
	}
	if err := os.Rename(tmp, c.latest); err != nil {
		c.logger.Warn("model iq chart refresh failed", "error", err.Error())
		return
	}
	c.ready.Store(true)
	c.logger.Info("model iq chart refreshed", "cells", filled, "path", c.latest)
}

func (c *IQChart) fetchCell(ctx context.Context, model, effort string) (iqCell, error) {
	url := fmt.Sprintf(
		"%s?model=%s&effort=%s&view=summary&selection=user-selected-v1",
		c.apiBase, model, effort,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return iqCell{}, err
	}
	req.Header.Set("User-Agent", "cinlan-qq-bot/iqchart")
	resp, err := c.client.Do(req)
	if err != nil {
		return iqCell{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return iqCell{}, fmt.Errorf("score api http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return iqCell{}, err
	}
	var summary scoreSummary
	if err := json.Unmarshal(body, &summary); err != nil {
		return iqCell{}, err
	}
	if summary.Score == 0 {
		return iqCell{}, errors.New("empty score")
	}
	return iqCell{
		score:    summary.Score,
		coverage: summary.Cover,
		cost:     summary.Stat.Metrics.Cost.Mean,
		minutes:  summary.Stat.Metrics.Elapsed.Mean / 60000,
		ok:       true,
	}, nil
}

func (c *IQChart) Tool() tool.Definition {
	return tool.Definition{
		Name: "model_iq_chart",
		Description: "发送最新的模型 IQ 天梯雷达分图片（codexradar 数据，定时自动更新）。" +
			"用户发「模型iq」「模型 IQ」「iq天梯」「雷达分」时使用。",
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
		Permission: tool.PermissionEveryone,
		Timeout:    10 * time.Second,
		Handler:    c.serve,
	}
}

func (c *IQChart) serve(_ context.Context, _ tool.Call) (tool.Result, error) {
	if !c.ready.Load() {
		return tool.Result{}, errors.New("model iq chart is not ready yet")
	}
	info, err := os.Stat(c.latest)
	if err != nil || !info.Mode().IsRegular() {
		return tool.Result{}, errors.New("model iq chart image is missing")
	}
	stamp := info.ModTime().Format("15:04")
	response := domain.AgentResponse{
		Chain: message.Chain{
			message.Image(c.latest),
		},
	}
	return tool.Result{
		Content:  map[string]any{"status": "sent", "updated_at": stamp},
		Response: &response,
	}, nil
}
