package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Device struct {
	Serial    string `json:"serial"`
	State     string `json:"state"`
	Model     string `json:"model"`
	Transport string `json:"transport"` // "usb" or "wifi"
}

type FileEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	IsDir   bool      `json:"isDir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

func adbRun(serial string, args ...string) (string, error) {
	cmdArgs := []string{"-s", serial}
	cmdArgs = append(cmdArgs, args...)
	out, err := exec.Command("adb", cmdArgs...).Output()
	return strings.TrimSpace(string(out)), err
}

func adbRunNoSerial(args ...string) (string, error) {
	out, err := exec.Command("adb", args...).Output()
	return strings.TrimSpace(string(out)), err
}

// ListDevices returns all connected ADB devices with model info.
func ListDevices() ([]Device, error) {
	out, err := exec.Command("adb", "devices", "-l").Output()
	if err != nil {
		return nil, fmt.Errorf("adb devices: %w", err)
	}

	var devices []Device
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "List of") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		serial := parts[0]
		state := parts[1]
		if state != "device" {
			continue
		}

		model := ""
		for _, p := range parts[2:] {
			if strings.HasPrefix(p, "model:") {
				model = strings.TrimPrefix(p, "model:")
				model = strings.ReplaceAll(model, "_", " ")
			}
		}

		transport := "usb"
		// WiFi serials look like IP:port
		if strings.Contains(serial, ":") {
			transport = "wifi"
		}

		devices = append(devices, Device{
			Serial:    serial,
			State:     state,
			Model:     model,
			Transport: transport,
		})
	}
	return devices, nil
}

// ConnectWifi runs adb connect <host:port>.
func ConnectWifi(address string) (string, error) {
	if !strings.Contains(address, ":") {
		address = address + ":5555"
	}
	out, err := adbRunNoSerial("connect", address)
	if err != nil {
		return "", err
	}
	if strings.Contains(out, "cannot") || strings.Contains(out, "failed") || strings.Contains(out, "error") {
		return out, fmt.Errorf("connect failed: %s", out)
	}
	return out, nil
}

// DisconnectWifi runs adb disconnect <host:port>.
func DisconnectWifi(address string) (string, error) {
	return adbRunNoSerial("disconnect", address)
}

// BrowseDir lists files and directories at the given path on the device.
func BrowseDir(serial, path string) ([]FileEntry, error) {
	// Normalize: adb shell requires an absolute path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	// Use ls -la --color=never for detailed listing
	raw, err := adbRun(serial, "shell", "ls", "-la", "--color=never", path)
	if err != nil {
		// Try without --color=never (older Android)
		raw, err = adbRun(serial, "shell", "ls", "-la", path)
		if err != nil {
			return nil, fmt.Errorf("ls %s: %w", path, err)
		}
	}

	var entries []FileEntry
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "total") {
			continue
		}
		entry := parseLsLine(line, path)
		if entry == nil {
			continue
		}
		entries = append(entries, *entry)
	}
	return entries, nil
}

// parseLsLine parses a single line from `ls -la` output.
//
// Two date formats exist:
//   Toybox (Android 6+, Samsung A52): "YYYY-MM-DD HH:MM" → name at index 7
//   BusyBox (older Android):          "Mon DD  HH:MM"    → name at index 8
func parseLsLine(line, parentPath string) *FileEntry {
	fields := strings.Fields(line)
	if len(fields) < 7 {
		return nil
	}

	perms := fields[0]
	// Must look like a permission string (e.g. -rw-rw---- or drwxr-xr-x)
	if len(perms) < 10 {
		return nil
	}
	isDir := strings.HasPrefix(perms, "d")
	isLink := strings.HasPrefix(perms, "l")

	// Size is field index 4
	size, _ := strconv.ParseInt(fields[4], 10, 64)

	// Detect date format by checking whether field[5] is an ISO date (contains "-").
	// Toybox:  fields[5]="2024-01-15"  fields[6]="10:30"  name at 7
	// BusyBox: fields[5]="Jan"         fields[6]="15"      fields[7]="10:30"  name at 8
	nameIdx := 8
	if strings.Contains(fields[5], "-") {
		nameIdx = 7
	}
	if len(fields) <= nameIdx {
		return nil
	}
	name := strings.Join(fields[nameIdx:], " ")

	// Handle symlinks: "name -> target" — take just the name part
	if isLink {
		if idx := strings.Index(name, " -> "); idx != -1 {
			name = name[:idx]
		}
	}

	// Some Android ls variants output the full absolute path as the name when
	// given an absolute directory argument (e.g. "/sdcard" instead of "sdcard").
	// Filenames can never legitimately contain '/', so extract the basename.
	if strings.Contains(name, "/") {
		name = name[strings.LastIndex(name, "/")+1:]
	}

	// Skip . and ..
	if name == "" || name == "." || name == ".." {
		return nil
	}

	fullPath := strings.TrimRight(parentPath, "/") + "/" + name

	return &FileEntry{
		Name:  name,
		Path:  fullPath,
		IsDir: isDir || isLink, // treat symlinked dirs as dirs
		Size:  size,
	}
}

// FileSize returns the size of a single file on the device in bytes.
func FileSize(serial, remotePath string) (int64, error) {
	out, err := adbRun(serial, "shell", "stat", "-c", "%s", remotePath)
	if err != nil {
		return 0, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, err
	}
	return size, nil
}

// DeletePath deletes a file or directory on the device.
func DeletePath(serial, remotePath string) error {
	_, err := adbRun(serial, "shell", "rm", "-rf", remotePath)
	return err
}

// RenamePath renames/moves a file or directory on the device.
func RenamePath(serial, oldPath, newPath string) error {
	_, err := adbRun(serial, "shell", "mv", oldPath, newPath)
	return err
}

// PullFile runs adb pull for a single file.
func PullFile(serial, remotePath, localPath string) error {
	_, err := exec.Command("adb", "-s", serial, "pull", remotePath, localPath).CombinedOutput()
	return err
}

// ViewCachePath returns the local cache path for a remote file preview.
func ViewCachePath(serial, remotePath string) string {
	cacheDir := filepath.Join(os.TempDir(), "androidbackup_view")
	_ = os.MkdirAll(cacheDir, 0755)
	base := filepath.Base(remotePath)
	return filepath.Join(cacheDir, fmt.Sprintf("%08x_%s", fnvHash(serial+":"+remotePath), sanitizeFilename(base)))
}

func fnvHash(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	s := b.String()
	if len(s) > 80 {
		s = s[len(s)-80:]
	}
	return s
}

// ExtContentType maps a lowercase file extension (without dot) to a MIME type.
func ExtContentType(ext string) string {
	m := map[string]string{
		"jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png",
		"gif": "image/gif", "webp": "image/webp", "bmp": "image/bmp",
		"heic": "image/heic", "heif": "image/heif",
		"mp4": "video/mp4", "m4v": "video/mp4", "mov": "video/quicktime",
		"mkv": "video/x-matroska", "avi": "video/x-msvideo", "3gp": "video/3gpp",
		"webm": "video/webm",
		"mp3": "audio/mpeg", "aac": "audio/aac", "m4a": "audio/mp4",
		"ogg": "audio/ogg", "flac": "audio/flac", "wav": "audio/wav",
		"pdf":  "application/pdf",
		"txt":  "text/plain; charset=utf-8",
		"log":  "text/plain; charset=utf-8",
		"xml":  "text/xml; charset=utf-8",
		"json": "application/json; charset=utf-8",
		"csv":  "text/csv; charset=utf-8",
		"md":   "text/plain; charset=utf-8",
	}
	if ct, ok := m[ext]; ok {
		return ct
	}
	return "application/octet-stream"
}
