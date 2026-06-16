package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type wsHub struct {
	clients   map[*websocket.Conn]struct{}
	broadcast chan []byte
	register  chan *websocket.Conn
	unregister chan *websocket.Conn
}

func newHub() *wsHub {
	return &wsHub{
		clients:    make(map[*websocket.Conn]struct{}),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *websocket.Conn),
		unregister: make(chan *websocket.Conn),
	}
}

func (h *wsHub) run() {
	for {
		select {
		case c := <-h.register:
			h.clients[c] = struct{}{}
		case c := <-h.unregister:
			delete(h.clients, c)
			c.Close()
		case msg := <-h.broadcast:
			for c := range h.clients {
				if err := c.WriteMessage(websocket.TextMessage, msg); err != nil {
					delete(h.clients, c)
					c.Close()
				}
			}
		}
	}
}

func (h *wsHub) send(v interface{}) {
	data, _ := json.Marshal(v)
	select {
	case h.broadcast <- data:
	default:
	}
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func readBody(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func errResp(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ---- local filesystem browsing ----

type LocalEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

func localRoots() []LocalEntry {
	var roots []LocalEntry
	if runtime.GOOS == "windows" {
		for _, letter := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
			drive := string(letter) + ":\\"
			if _, err := os.Stat(drive); err == nil {
				roots = append(roots, LocalEntry{Name: drive, Path: drive, IsDir: true})
			}
		}
	} else {
		roots = append(roots, LocalEntry{Name: "/", Path: "/", IsDir: true})
	}
	return roots
}

func handleLocalBrowse(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, 200, localRoots())
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		errResp(w, 404, "path not found")
		return
	}
	if !info.IsDir() {
		errResp(w, 400, "not a directory")
		return
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		errResp(w, 500, err.Error())
		return
	}

	var result []LocalEntry
	for _, e := range entries {
		info, _ := e.Info()
		size := int64(0)
		if info != nil && !e.IsDir() {
			size = info.Size()
		}
		result = append(result, LocalEntry{
			Name:  e.Name(),
			Path:  filepath.Join(path, e.Name()),
			IsDir: e.IsDir(),
			Size:  size,
		})
	}
	writeJSON(w, 200, result)
}

func handleAndroidView(w http.ResponseWriter, r *http.Request) {
	serial := r.URL.Query().Get("serial")
	remotePath := r.URL.Query().Get("path")
	if serial == "" || remotePath == "" {
		errResp(w, 400, "serial and path required")
		return
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(remotePath), "."))
	ct := ExtContentType(ext)

	cachedPath := ViewCachePath(serial, remotePath)

	// Re-pull when caller requests a refresh or file is not in cache
	if r.URL.Query().Get("refresh") == "1" {
		_ = os.Remove(cachedPath)
	}
	if _, err := os.Stat(cachedPath); os.IsNotExist(err) {
		if err := PullFile(serial, remotePath, cachedPath); err != nil {
			errResp(w, 502, "adb pull failed: "+err.Error())
			return
		}
	}

	f, err := os.Open(cachedPath)
	if err != nil {
		errResp(w, 500, err.Error())
		return
	}
	defer f.Close()

	stat, _ := f.Stat()
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, filepath.Base(remotePath), stat.ModTime(), f)
}

func handleLocalMkdir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"` // full path of the new folder to create
	}
	if err := readBody(r, &body); err != nil || body.Path == "" {
		errResp(w, 400, "path required")
		return
	}
	if err := os.MkdirAll(body.Path, 0755); err != nil {
		errResp(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"path": body.Path})
}

// ---- device handlers ----

func handleListDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := ListDevices()
	if err != nil {
		errResp(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, devices)
}

func handleConnectWifi(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Address string `json:"address"`
	}
	if err := readBody(r, &body); err != nil || body.Address == "" {
		errResp(w, 400, "address required")
		return
	}
	msg, err := ConnectWifi(body.Address)
	if err != nil {
		slog.Warn("wifi connect failed", "address", body.Address, "err", err)
		errResp(w, 500, err.Error())
		return
	}
	slog.Info("wifi connected", "address", body.Address, "result", msg)
	writeJSON(w, 200, map[string]string{"message": msg})
}

func handleDisconnectWifi(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Address string `json:"address"`
	}
	if err := readBody(r, &body); err != nil || body.Address == "" {
		errResp(w, 400, "address required")
		return
	}
	msg, err := DisconnectWifi(body.Address)
	if err != nil {
		errResp(w, 500, err.Error())
		return
	}
	slog.Info("wifi disconnected", "address", body.Address, "result", msg)
	writeJSON(w, 200, map[string]string{"message": msg})
}

// ---- Android filesystem handlers ----

func handleAndroidBrowse(w http.ResponseWriter, r *http.Request) {
	serial := r.URL.Query().Get("serial")
	path := r.URL.Query().Get("path")
	if serial == "" {
		errResp(w, 400, "serial required")
		return
	}
	if path == "" {
		path = "/storage"
	}
	entries, err := BrowseDir(serial, path)
	if err != nil {
		errResp(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, entries)
}

func handleAndroidDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Serial string `json:"serial"`
		Path   string `json:"path"`
	}
	if err := readBody(r, &body); err != nil || body.Serial == "" || body.Path == "" {
		errResp(w, 400, "serial and path required")
		return
	}
	if body.Path == "/" || body.Path == "/sdcard" {
		errResp(w, 400, "refusing to delete root path")
		return
	}
	if err := DeletePath(body.Serial, body.Path); err != nil {
		slog.Warn("delete failed", "path", body.Path, "err", err)
		errResp(w, 500, err.Error())
		return
	}
	slog.Info("deleted", "serial", body.Serial, "path", body.Path)
	writeJSON(w, 200, map[string]string{"status": "deleted"})
}

func handleAndroidRename(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Serial  string `json:"serial"`
		OldPath string `json:"oldPath"`
		NewPath string `json:"newPath"`
	}
	if err := readBody(r, &body); err != nil || body.Serial == "" || body.OldPath == "" || body.NewPath == "" {
		errResp(w, 400, "serial, oldPath and newPath required")
		return
	}
	if err := RenamePath(body.Serial, body.OldPath, body.NewPath); err != nil {
		slog.Warn("rename failed", "from", body.OldPath, "to", body.NewPath, "err", err)
		errResp(w, 500, err.Error())
		return
	}
	slog.Info("renamed", "serial", body.Serial, "from", body.OldPath, "to", body.NewPath)
	writeJSON(w, 200, map[string]string{"status": "renamed"})
}

// ---- transfer handlers ----

func makeTransferHandlers(tm *TransferManager) func(mux *http.ServeMux) {
	return func(mux *http.ServeMux) {
		mux.HandleFunc("/api/transfers", func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				writeJSON(w, 200, tm.ListSummaries())
			case http.MethodPost:
				var body struct {
					DeviceSerial string `json:"deviceSerial"`
					RemoteDir    string `json:"remoteDir"`
					LocalDir     string `json:"localDir"`
				}
				if err := readBody(r, &body); err != nil || body.DeviceSerial == "" || body.RemoteDir == "" || body.LocalDir == "" {
					errResp(w, 400, "deviceSerial, remoteDir and localDir required")
					return
				}
				t := tm.Create(body.DeviceSerial, body.RemoteDir, body.LocalDir)
				if err := tm.Start(t.ID); err != nil {
					errResp(w, 500, err.Error())
					return
				}
				writeJSON(w, 201, t)
			default:
				errResp(w, 405, "method not allowed")
			}
		})

		mux.HandleFunc("/api/transfers/", func(w http.ResponseWriter, r *http.Request) {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/transfers/"), "/")
			id := parts[0]
			action := ""
			if len(parts) > 1 {
				action = parts[1]
			}

			// Bulk action — not a transfer-specific route
			if id == "clear" {
				if r.Method != http.MethodPost {
					errResp(w, 405, "method not allowed")
					return
				}
				n := tm.ClearFinished()
				writeJSON(w, 200, map[string]int{"removed": n})
				return
			}

			t, ok := tm.Get(id)
			if !ok {
				errResp(w, 404, "transfer not found")
				return
			}

			switch action {
			case "":
				writeJSON(w, 200, t)
			case "start", "resume":
				if err := tm.Start(id); err != nil {
					errResp(w, 400, err.Error())
					return
				}
				writeJSON(w, 200, t)
			case "pause":
				if err := tm.Pause(id); err != nil {
					errResp(w, 400, err.Error())
					return
				}
				writeJSON(w, 200, t)
			case "cancel":
				if err := tm.Cancel(id); err != nil {
					errResp(w, 400, err.Error())
					return
				}
				writeJSON(w, 200, t)
			case "files":
				if r.Method != http.MethodGet {
					errResp(w, 405, "method not allowed")
					return
				}
				q := r.URL.Query()
				filter := q.Get("filter")
				offset := 0
				limit := 100
				fmt.Sscan(q.Get("offset"), &offset)
				fmt.Sscan(q.Get("limit"), &limit)
				result, err := tm.GetFiles(id, filter, offset, limit)
				if err != nil {
					errResp(w, 404, err.Error())
					return
				}
				writeJSON(w, 200, result)
			case "remove":
				if err := tm.Remove(id); err != nil {
					errResp(w, 400, err.Error())
					return
				}
				writeJSON(w, 200, map[string]string{"status": "removed"})
			default:
				errResp(w, 404, "unknown action")
			}
		})
	}
}

func handleWS(hub *wsHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		hub.register <- conn
		// keep connection alive, unregister on close
		go func() {
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					hub.unregister <- conn
					return
				}
			}
		}()
	}
}
