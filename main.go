package main

import (
	"embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

//go:embed index.html
var staticFiles embed.FS

func main() {
	port := "8765"
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}

	hub := newHub()
	go hub.run()

	stateFile := filepath.Join(os.TempDir(), "androidbackup_state.json")

	tm := NewTransferManager(stateFile, func(t Transfer) {
		hub.send(map[string]interface{}{
			"type":     "transfer_update",
			"transfer": t,
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

	// Transfer API
	registerTransfers := makeTransferHandlers(tm)
	registerTransfers(mux)

	addr := fmt.Sprintf(":%s", port)
	log.Printf("AndroidBackup server running at http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, corsMiddleware(mux)))
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
