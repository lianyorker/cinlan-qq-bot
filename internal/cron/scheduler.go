package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
)

const (
	MinInterval       = 10 * time.Second
	MaxTextSize       = 4000
	maxPersistedBytes = 4 << 20
)

type Job struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	ChatType  string        `json:"chat_type"`
	ChatID    string        `json:"chat_id"`
	SelfID    string        `json:"self_id,omitempty"`
	Text      string        `json:"text"`
	Interval  time.Duration `json:"interval"`
	NextRun   time.Time     `json:"next_run"`
	Enabled   bool          `json:"enabled"`
	LastError string        `json:"last_error,omitempty"`
}

type Sender interface {
	Send(context.Context, platform.Outbound) error
}

type Scheduler struct {
	mu         sync.Mutex
	jobs       map[string]Job
	running    map[string]bool
	sender     Sender
	logger     *slog.Logger
	now        func() time.Time
	path       string
	persistErr error
}

func New(sender Sender, logger *slog.Logger) *Scheduler {
	return newScheduler(sender, logger, "")
}

// Open restores durable Cron jobs from path. An empty path keeps the
// in-memory behavior used by library callers and tests.
func Open(sender Sender, logger *slog.Logger, path string) (*Scheduler, error) {
	scheduler := newScheduler(sender, logger, strings.TrimSpace(path))
	if scheduler.path == "" {
		return scheduler, nil
	}
	if err := scheduler.load(); err != nil {
		return nil, err
	}
	return scheduler, nil
}

func newScheduler(sender Sender, logger *slog.Logger, path string) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		jobs:    make(map[string]Job),
		running: make(map[string]bool),
		sender:  sender,
		logger:  logger,
		now:     time.Now,
		path:    path,
	}
}

func (s *Scheduler) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

func (s *Scheduler) PersistenceError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistErr
}

func (s *Scheduler) Add(job Job) error {
	job.ID = strings.TrimSpace(job.ID)
	job.Name = strings.TrimSpace(job.Name)
	job.ChatType = strings.TrimSpace(job.ChatType)
	job.ChatID = strings.TrimSpace(job.ChatID)
	job.SelfID = strings.TrimSpace(job.SelfID)
	job.Text = strings.TrimSpace(job.Text)
	if job.ID == "" || job.Name == "" {
		return errors.New("job id and name are required")
	}
	if job.ChatType != platform.ChatGroup && job.ChatType != platform.ChatPrivate {
		return fmt.Errorf("unsupported job chat type %q", job.ChatType)
	}
	if job.ChatID == "" || job.Text == "" {
		return errors.New("job chat ID and text are required")
	}
	if len([]rune(job.Text)) > MaxTextSize {
		return fmt.Errorf("job text exceeds %d runes", MaxTextSize)
	}
	if job.Interval < MinInterval {
		return fmt.Errorf("job interval must be at least %s", MinInterval)
	}
	if job.NextRun.IsZero() {
		job.NextRun = s.now().Add(job.Interval)
	}
	job.Enabled = true
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.ID]; exists {
		return fmt.Errorf("job %q already exists", job.ID)
	}
	s.jobs[job.ID] = job
	if err := s.persistLocked(); err != nil {
		delete(s.jobs, job.ID)
		return fmt.Errorf("persist job %q: %w", job.ID, err)
	}
	return nil
}

func (s *Scheduler) Remove(id string) bool {
	removed, _ := s.RemoveWithError(id)
	return removed
}

func (s *Scheduler) RemoveWithError(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return false, nil
	}
	delete(s.jobs, id)
	delete(s.running, id)
	if err := s.persistLocked(); err != nil {
		s.jobs[id] = job
		return false, fmt.Errorf("persist job removal: %w", err)
	}
	return true, nil
}

func (s *Scheduler) List() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		result = append(result, job)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.RunDue(ctx)
		}
	}
}

// RunDue executes due jobs once. It is public so tests and an external
// supervisor can drive the scheduler without waiting for a ticker.
func (s *Scheduler) RunDue(ctx context.Context) {
	now := s.now()
	due := make([]Job, 0)
	s.mu.Lock()
	for _, job := range s.jobs {
		if !job.Enabled || job.NextRun.After(now) || s.running[job.ID] {
			continue
		}
		s.running[job.ID] = true
		job.NextRun = now.Add(job.Interval)
		s.jobs[job.ID] = job
		if err := s.persistLocked(); err != nil {
			s.logger.Error("cron job progress persistence failed", "job_id", job.ID, "error", err)
		}
		due = append(due, job)
	}
	s.mu.Unlock()

	for _, job := range due {
		err := error(nil)
		if s.sender == nil {
			err = errors.New("cron sender is not configured")
		} else {
			err = s.sender.Send(ctx, platform.Outbound{
				ChatType: job.ChatType,
				ChatID:   job.ChatID,
				SelfID:   job.SelfID,
				Chain:    message.Chain{message.Text(job.Text)},
			})
		}
		s.mu.Lock()
		delete(s.running, job.ID)
		current, exists := s.jobs[job.ID]
		if exists {
			if err != nil {
				current.LastError = err.Error()
				s.logger.Error("cron job failed", "job_id", job.ID, "error", err)
			} else {
				current.LastError = ""
			}
			s.jobs[job.ID] = current
			if persistErr := s.persistLocked(); persistErr != nil {
				s.logger.Error("cron job result persistence failed", "job_id", job.ID, "error", persistErr)
			}
		}
		s.mu.Unlock()
	}
}

type persistedState struct {
	Version int            `json:"version"`
	Jobs    map[string]Job `json:"jobs"`
}

func (s *Scheduler) load() error {
	info, statErr := os.Stat(s.path)
	if statErr == nil && info.Size() > maxPersistedBytes {
		return fmt.Errorf("cron store exceeds %d bytes", maxPersistedBytes)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("stat cron store: %w", statErr)
	}
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open cron store: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxPersistedBytes+1))
	if err != nil {
		return fmt.Errorf("read cron store: %w", err)
	}
	if len(data) > maxPersistedBytes {
		return fmt.Errorf("cron store exceeds %d bytes", maxPersistedBytes)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("decode cron store: %w", err)
	}
	if state.Version != 0 && state.Version != 1 {
		return fmt.Errorf("unsupported cron store version %d", state.Version)
	}
	for key, job := range state.Jobs {
		if key != job.ID {
			return fmt.Errorf("cron store key %q does not match job ID %q", key, job.ID)
		}
		if err := validateJob(job); err != nil {
			return fmt.Errorf("cron job %q: %w", key, err)
		}
		if job.NextRun.IsZero() {
			job.NextRun = s.now().Add(job.Interval)
		}
		s.jobs[key] = job
	}
	return nil
}

func (s *Scheduler) persistLocked() error {
	if s.path == "" {
		s.persistErr = nil
		return nil
	}
	state := persistedState{
		Version: 1,
		Jobs:    make(map[string]Job, len(s.jobs)),
	}
	for key, job := range s.jobs {
		state.Jobs[key] = job
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err == nil {
		dir := filepath.Dir(s.path)
		if dir != "." {
			err = os.MkdirAll(dir, 0o700)
		}
	}
	if err == nil {
		temp, createErr := os.CreateTemp(filepath.Dir(s.path), ".cron-*.tmp")
		if createErr != nil {
			err = createErr
		} else {
			tempName := temp.Name()
			_, writeErr := temp.Write(data)
			closeErr := temp.Close()
			if writeErr != nil {
				err = writeErr
			} else if closeErr != nil {
				err = closeErr
			} else if renameErr := os.Rename(tempName, s.path); renameErr != nil {
				// Windows cannot replace an existing file with Rename.
				_ = os.Remove(s.path)
				err = os.Rename(tempName, s.path)
			}
			if err != nil {
				_ = os.Remove(tempName)
			} else {
				_ = os.Chmod(s.path, 0o600)
			}
		}
	}
	s.persistErr = err
	return err
}

func validateJob(job Job) error {
	if strings.TrimSpace(job.ID) == "" || strings.TrimSpace(job.Name) == "" {
		return errors.New("job id and name are required")
	}
	if job.ChatType != platform.ChatGroup && job.ChatType != platform.ChatPrivate {
		return fmt.Errorf("unsupported job chat type %q", job.ChatType)
	}
	if strings.TrimSpace(job.ChatID) == "" || strings.TrimSpace(job.Text) == "" {
		return errors.New("job chat ID and text are required")
	}
	if len([]rune(job.Text)) > MaxTextSize {
		return fmt.Errorf("job text exceeds %d runes", MaxTextSize)
	}
	if job.Interval < MinInterval {
		return fmt.Errorf("job interval must be at least %s", MinInterval)
	}
	return nil
}
