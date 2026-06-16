package main

import (
	"embed"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
)

//go:embed index.html
var staticFiles embed.FS

// version is set at build time via -ldflags "-X main.version=vX.Y.Z"
var version = "dev"

func main() {
	logLevel := flag.String("log-level", "", "log level: debug, info, warn, error  (env: LOG_LEVEL)")
	flag.Parse()

	level := *logLevel
	if level == "" {
		level = os.Getenv("LOG_LEVEL")
	}
	initLogger(level)

	port := "8765"
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}

	hub := newHub()
	go hub.run()

	stateFile := filepath.Join(os.TempDir(), "androidbackup_state.json")

	tm := NewTransferManager(stateFile, func(s TransferSummary) {
		hub.send(map[string]interface{}{
			"type":     "transfer_update",
			"transfer": s,
		})
	})

	mux := http.NewServeMux()

	// Static frontend
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data, _ := staticFiles.ReadFile("index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})

	// WebSocket
	mux.HandleFunc("/ws", handleWS(hub))

	// Device API
	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleListDevices(w, r)
		default:
			errResp(w, 405, "method not allowed")
		}
	})
	mux.HandleFunc("/api/devices/connect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			errResp(w, 405, "method not allowed")
			return
		}
		handleConnectWifi(w, r)
	})
	mux.HandleFunc("/api/devices/disconnect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			errResp(w, 405, "method not allowed")
			return
		}
		handleDisconnectWifi(w, r)
	})

	// Android filesystem API
	mux.HandleFunc("/api/android/view", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			errResp(w, 405, "method not allowed")
			return
		}
		handleAndroidView(w, r)
	})
	mux.HandleFunc("/api/android/browse", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			errResp(w, 405, "method not allowed")
			return
		}
		handleAndroidBrowse(w, r)
	})
	mux.HandleFunc("/api/android/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			errResp(w, 405, "method not allowed")
			return
		}
		handleAndroidDelete(w, r)
	})
	mux.HandleFunc("/api/android/rename", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			errResp(w, 405, "method not allowed")
			return
		}
		handleAndroidRename(w, r)
	})

	// Local filesystem browse
	mux.HandleFunc("/api/local/browse", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			errResp(w, 405, "method not allowed")
			return
		}
		handleLocalBrowse(w, r)
	})
	mux.HandleFunc("/api/local/mkdir", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			errResp(w, 405, "method not allowed")
			return
		}
		handleLocalMkdir(w, r)
	})

	// Transfer API
	registerTransfers := makeTransferHandlers(tm)
	registerTransfers(mux)

	// Version endpoint
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"version": version})
	})

	addr := fmt.Sprintf(":%s", port)
	slog.Info("AndroidBackup "+version, "url", "http://localhost"+addr, "state", stateFile)
	if err := http.ListenAndServe(addr, loggingMiddleware(corsMiddleware(mux))); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}
