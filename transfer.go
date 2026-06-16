package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type TransferStatus string

const (
	StatusPending   TransferStatus = "pending"
	StatusScanning  TransferStatus = "scanning"
	StatusRunning   TransferStatus = "running"
	StatusPaused    TransferStatus = "paused"
	StatusCompleted TransferStatus = "completed"
	StatusFailed    TransferStatus = "failed"
	StatusCancelled TransferStatus = "cancelled"
)

type FileStatus string

const (
	FilePending   FileStatus = "pending"
	FileSkipped   FileStatus = "skipped"
	FileRunning   FileStatus = "running"
	FileDone      FileStatus = "done"
	FileFailed    FileStatus = "failed"
)

type TransferFile struct {
	RemotePath string     `json:"remotePath"`
	LocalPath  string     `json:"localPath"`
	Size       int64      `json:"size"`
	Status     FileStatus `json:"status"`
	Error      string     `json:"error,omitempty"`
}

type Transfer struct {
	ID           string         `json:"id"`
	DeviceSerial string         `json:"deviceSerial"`
	RemoteDir    string         `json:"remoteDir"`
	LocalDir     string         `json:"localDir"`
	Status       TransferStatus `json:"status"`
	Files        []*TransferFile `json:"files"`
	TotalFiles   int            `json:"totalFiles"`
	DoneFiles    int            `json:"doneFiles"`
	SkippedFiles int            `json:"skippedFiles"`
	FailedFiles  int            `json:"failedFiles"`
	TotalBytes   int64          `json:"totalBytes"`
	DoneBytes    int64          `json:"doneBytes"`
	CreatedAt    time.Time      `json:"createdAt"`
	UpdatedAt    time.Time      `json:"updatedAt"`
	Error        string         `json:"error,omitempty"`

	mu       sync.Mutex    `json:"-"`
	pauseCh  chan struct{}  `json:"-"`
	cancelCh chan struct{}  `json:"-"`
}

// TransferSummary is broadcast over WebSocket and returned by the list API.
// It omits the (potentially huge) Files slice so payloads stay small.
type TransferSummary struct {
	ID           string         `json:"id"`
	DeviceSerial string         `json:"deviceSerial"`
	RemoteDir    string         `json:"remoteDir"`
	LocalDir     string         `json:"localDir"`
	Status       TransferStatus `json:"status"`
	TotalFiles   int            `json:"totalFiles"`
	DoneFiles    int            `json:"doneFiles"`
	SkippedFiles int            `json:"skippedFiles"`
	FailedFiles  int            `json:"failedFiles"`
	TotalBytes   int64          `json:"totalBytes"`
	DoneBytes    int64          `json:"doneBytes"`
	CreatedAt    time.Time      `json:"createdAt"`
	UpdatedAt    time.Time      `json:"updatedAt"`
	Error        string         `json:"error,omitempty"`
}

func (t *Transfer) summary() TransferSummary {
	return TransferSummary{
		ID:           t.ID,
		DeviceSerial: t.DeviceSerial,
		RemoteDir:    t.RemoteDir,
		LocalDir:     t.LocalDir,
		Status:       t.Status,
		TotalFiles:   t.TotalFiles,
		DoneFiles:    t.DoneFiles,
		SkippedFiles: t.SkippedFiles,
		FailedFiles:  t.FailedFiles,
		TotalBytes:   t.TotalBytes,
		DoneBytes:    t.DoneBytes,
		CreatedAt:    t.CreatedAt,
		UpdatedAt:    t.UpdatedAt,
		Error:        t.Error,
	}
}

type FileListResult struct {
	Total int             `json:"total"`
	Items []*TransferFile `json:"items"`
}

type TransferManager struct {
	mu        sync.RWMutex
	transfers map[string]*Transfer
	stateFile string
	broadcast func(TransferSummary)
}

func NewTransferManager(stateFile string, broadcast func(TransferSummary)) *TransferManager {
	tm := &TransferManager{
		transfers: make(map[string]*Transfer),
		stateFile: stateFile,
		broadcast: broadcast,
	}
	tm.loadState()
	return tm
}

func (tm *TransferManager) loadState() {
	data, err := os.ReadFile(tm.stateFile)
	if err != nil {
		return
	}
	var transfers []*Transfer
	if err := json.Unmarshal(data, &transfers); err != nil {
		return
	}
	for _, t := range transfers {
		// Restore non-terminal transfers as paused so they can be resumed
		if t.Status == StatusRunning || t.Status == StatusScanning {
			t.Status = StatusPaused
		}
		t.pauseCh = make(chan struct{})
		t.cancelCh = make(chan struct{})
		tm.transfers[t.ID] = t
	}
	if len(transfers) > 0 {
		slog.Info("state loaded", "transfers", len(transfers))
	}
}

func (tm *TransferManager) saveState() {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	var transfers []*Transfer
	for _, t := range tm.transfers {
		transfers = append(transfers, t)
	}
	data, err := json.MarshalIndent(transfers, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(tm.stateFile, data, 0644)
}

func (tm *TransferManager) Create(deviceSerial, remoteDir, localDir string) *Transfer {
	t := &Transfer{
		ID:           fmt.Sprintf("%d", time.Now().UnixNano()),
		DeviceSerial: deviceSerial,
		RemoteDir:    remoteDir,
		LocalDir:     localDir,
		Status:       StatusPending,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		pauseCh:      make(chan struct{}),
		cancelCh:     make(chan struct{}),
	}
	tm.mu.Lock()
	tm.transfers[t.ID] = t
	tm.mu.Unlock()
	tm.saveState()
	slog.Info("transfer created", "id", t.ID, "device", deviceSerial, "remote", remoteDir, "local", localDir)
	return t
}

func (tm *TransferManager) Get(id string) (*Transfer, bool) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	t, ok := tm.transfers[id]
	return t, ok
}

func (tm *TransferManager) ListSummaries() []TransferSummary {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	var list []TransferSummary
	for _, t := range tm.transfers {
		t.mu.Lock()
		s := t.summary()
		t.mu.Unlock()
		list = append(list, s)
	}
	return list
}

// GetFiles returns paginated files for a transfer, optionally filtered by status.
// filter: "all", "failed", "recent" (default — newest-first, capped at limit).
func (tm *TransferManager) GetFiles(id, filter string, offset, limit int) (*FileListResult, error) {
	t, ok := tm.Get(id)
	if !ok {
		return nil, fmt.Errorf("transfer not found")
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if limit <= 0 {
		limit = 100
	}

	switch filter {
	case "failed":
		var out []*TransferFile
		for _, f := range t.Files {
			if f.Status == FileFailed {
				out = append(out, f)
			}
		}
		return paginate(out, offset, limit), nil

	case "recent", "":
		// Return files in reverse order so the most recently touched are first.
		// This gives a live view of what's happening right now.
		n := len(t.Files)
		reversed := make([]*TransferFile, n)
		for i, f := range t.Files {
			reversed[n-1-i] = f
		}
		return paginate(reversed, offset, limit), nil

	default: // "all"
		return paginate(t.Files, offset, limit), nil
	}
}

func paginate(files []*TransferFile, offset, limit int) *FileListResult {
	total := len(files)
	if offset >= total {
		return &FileListResult{Total: total, Items: []*TransferFile{}}
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return &FileListResult{Total: total, Items: files[offset:end]}
}

func (tm *TransferManager) Start(id string) error {
	t, ok := tm.Get(id)
	if !ok {
		return fmt.Errorf("transfer not found")
	}
	t.mu.Lock()
	if t.Status == StatusRunning || t.Status == StatusScanning {
		t.mu.Unlock()
		return fmt.Errorf("transfer already running")
	}
	if t.Status == StatusCompleted || t.Status == StatusCancelled {
		t.mu.Unlock()
		return fmt.Errorf("transfer is %s", t.Status)
	}
	// Re-create channels for fresh start/resume
	t.pauseCh = make(chan struct{})
	t.cancelCh = make(chan struct{})
	t.Status = StatusScanning
	t.UpdatedAt = time.Now()
	t.mu.Unlock()

	slog.Info("transfer starting", "id", id)
	go tm.run(t)
	return nil
}

func (tm *TransferManager) Pause(id string) error {
	t, ok := tm.Get(id)
	if !ok {
		return fmt.Errorf("transfer not found")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Status != StatusRunning && t.Status != StatusScanning {
		return fmt.Errorf("transfer is not running")
	}
	t.Status = StatusPaused
	t.UpdatedAt = time.Now()
	close(t.pauseCh)
	slog.Info("transfer paused", "id", id)
	return nil
}

func (tm *TransferManager) Cancel(id string) error {
	t, ok := tm.Get(id)
	if !ok {
		return fmt.Errorf("transfer not found")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Status == StatusCompleted || t.Status == StatusCancelled {
		return fmt.Errorf("transfer already %s", t.Status)
	}
	prevStatus := t.Status
	t.Status = StatusCancelled
	t.UpdatedAt = time.Now()
	if prevStatus == StatusRunning || prevStatus == StatusScanning {
		close(t.cancelCh)
	}
	slog.Info("transfer cancelled", "id", id)
	return nil
}

// ClearFinished removes all completed, failed, and cancelled transfers.
// Returns the number removed.
func (tm *TransferManager) ClearFinished() int {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	n := 0
	for id, t := range tm.transfers {
		t.mu.Lock()
		terminal := t.Status == StatusCompleted || t.Status == StatusFailed || t.Status == StatusCancelled
		t.mu.Unlock()
		if terminal {
			delete(tm.transfers, id)
			n++
		}
	}
	if n > 0 {
		go tm.saveState()
	}
	return n
}

func (tm *TransferManager) Remove(id string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t, ok := tm.transfers[id]
	if !ok {
		return fmt.Errorf("transfer not found")
	}
	t.mu.Lock()
	running := t.Status == StatusRunning || t.Status == StatusScanning
	t.mu.Unlock()
	if running {
		return fmt.Errorf("cancel the transfer before removing it")
	}
	delete(tm.transfers, id)
	go tm.saveState()
	return nil
}

func (tm *TransferManager) run(t *Transfer) {
	defer tm.saveState()

	// Ensure local dir exists
	if err := os.MkdirAll(t.LocalDir, 0755); err != nil {
		t.mu.Lock()
		t.Status = StatusFailed
		t.Error = err.Error()
		t.UpdatedAt = time.Now()
		t.mu.Unlock()
		tm.broadcast(t.summary())
		return
	}

	// Scan phase: build file list if we don't have one yet
	if len(t.Files) == 0 {
		slog.Debug("scan started", "id", t.ID, "remote", t.RemoteDir)
		if err := tm.scan(t); err != nil {
			slog.Error("scan failed", "id", t.ID, "err", err)
			t.mu.Lock()
			t.Status = StatusFailed
			t.Error = err.Error()
			t.UpdatedAt = time.Now()
			t.mu.Unlock()
			tm.broadcast(t.summary())
			return
		}
		if t.TotalFiles == 0 {
			slog.Warn("scan found no files", "id", t.ID, "remote", t.RemoteDir)
			t.mu.Lock()
			t.Status = StatusFailed
			t.Error = fmt.Sprintf("no files found in %s — check the path and device connection", t.RemoteDir)
			t.UpdatedAt = time.Now()
			t.mu.Unlock()
			tm.broadcast(t.summary())
			return
		}
		slog.Info("scan complete", "id", t.ID, "files", t.TotalFiles)
	}

	t.mu.Lock()
	// Check if cancelled during scan
	select {
	case <-t.cancelCh:
		t.mu.Unlock()
		return
	default:
	}
	t.Status = StatusRunning
	t.UpdatedAt = time.Now()
	t.mu.Unlock()
	tm.broadcast(t.summary())
	slog.Info("download started", "id", t.ID, "files", t.TotalFiles)

	// Download phase
	for _, f := range t.Files {
		// Check cancel
		select {
		case <-t.cancelCh:
			t.mu.Lock()
			t.Status = StatusCancelled
			t.UpdatedAt = time.Now()
			t.mu.Unlock()
			tm.broadcast(t.summary())
			return
		default:
		}

		// Check pause
		select {
		case <-t.pauseCh:
			tm.saveState()
			return
		default:
		}

		if f.Status == FileDone || f.Status == FileSkipped {
			continue
		}

		f.Status = FileRunning
		tm.broadcast(t.summary())

		// Check if local file already exists (skip / resume logic)
		localInfo, err := os.Stat(f.LocalPath)
		if err == nil {
			// File exists locally
			if localInfo.Size() == f.Size && f.Size > 0 {
				// Same size — skip
				slog.Debug("skip", "file", filepath.Base(f.RemotePath))
				f.Status = FileSkipped
				t.mu.Lock()
				t.SkippedFiles++
				t.DoneFiles++
				t.DoneBytes += f.Size
				t.UpdatedAt = time.Now()
				t.mu.Unlock()
				tm.broadcast(t.summary())
				tm.saveState()
				continue
			}
			// Size mismatch — partial download, re-pull
		}

		if err := os.MkdirAll(filepath.Dir(f.LocalPath), 0755); err != nil {
			f.Status = FileFailed
			f.Error = err.Error()
			t.mu.Lock()
			t.FailedFiles++
			t.UpdatedAt = time.Now()
			t.mu.Unlock()
			tm.broadcast(t.summary())
			continue
		}

		slog.Debug("pull", "file", filepath.Base(f.RemotePath))
		if err := PullFile(t.DeviceSerial, f.RemotePath, f.LocalPath); err != nil {
			slog.Warn("file failed", "file", f.RemotePath, "err", err)
			f.Status = FileFailed
			f.Error = err.Error()
			t.mu.Lock()
			t.FailedFiles++
			t.UpdatedAt = time.Now()
			t.mu.Unlock()
		} else {
			f.Status = FileDone
			t.mu.Lock()
			t.DoneFiles++
			t.DoneBytes += f.Size
			t.UpdatedAt = time.Now()
			t.mu.Unlock()
		}
		tm.broadcast(t.summary())
		tm.saveState()
	}

	t.mu.Lock()
	if t.FailedFiles > 0 {
		t.Status = StatusFailed
		t.Error = fmt.Sprintf("%d file(s) failed to download", t.FailedFiles)
		slog.Warn("transfer finished with errors", "id", t.ID, "done", t.DoneFiles, "skipped", t.SkippedFiles, "failed", t.FailedFiles)
	} else {
		t.Status = StatusCompleted
		slog.Info("transfer completed", "id", t.ID, "done", t.DoneFiles, "skipped", t.SkippedFiles)
	}
	t.UpdatedAt = time.Now()
	t.mu.Unlock()
	tm.broadcast(t.summary())
}

// scan recursively lists all files under remoteDir and populates t.Files.
func (tm *TransferManager) scan(t *Transfer) error {
	return tm.scanDir(t, t.RemoteDir, t.LocalDir)
}

func (tm *TransferManager) scanDir(t *Transfer, remoteDir, localDir string) error {
	select {
	case <-t.cancelCh:
		return fmt.Errorf("cancelled")
	default:
	}

	slog.Debug("scanning", "dir", remoteDir)
	entries, err := BrowseDir(t.DeviceSerial, remoteDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir {
			subLocal := filepath.Join(localDir, entry.Name)
			if err := tm.scanDir(t, entry.Path, subLocal); err != nil {
				return err
			}
		} else {
			localPath := filepath.Join(localDir, entry.Name)
			f := &TransferFile{
				RemotePath: entry.Path,
				LocalPath:  localPath,
				Size:       entry.Size,
				Status:     FilePending,
			}
			t.mu.Lock()
			t.Files = append(t.Files, f)
			t.TotalFiles++
			t.TotalBytes += entry.Size
			t.UpdatedAt = time.Now()
			t.mu.Unlock()
			tm.broadcast(t.summary())
		}
	}
	return nil
}
