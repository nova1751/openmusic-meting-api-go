package qishui

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

type diskCache struct {
	dir string
	mu  sync.Mutex
}

// EnableDiskCache enables the shared cache layout used by the standalone
// service. The caller supplies an application-owned directory.
func (a *Audio) EnableDiskCache(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	a.disk = &diskCache{dir: dir}
	return nil
}

func (d *diskCache) read(key [sha1.Size]byte) (data []byte, mime string, hit bool) {
	if d == nil {
		return nil, "", false
	}
	filename := filepath.Join(d.dir, hex.EncodeToString(key[:])+".audio")
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxAudio || time.Since(info.ModTime()) >= cacheTTL {
		return nil, "", false
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, "", false
	}
	defer file.Close()
	data, err = io.ReadAll(io.LimitReader(file, maxAudio+1))
	if err != nil || len(data) < 8 || len(data) > maxAudio {
		return nil, "", false
	}
	if bytes.HasPrefix(data, []byte("fLaC")) {
		return data, "audio/flac", true
	}
	if string(data[4:8]) == "ftyp" || string(data[4:8]) == "moov" {
		return data, "audio/mp4", true
	}
	return nil, "", false
}

func (d *diskCache) write(key [sha1.Size]byte, data []byte) {
	if d == nil || len(data) > maxAudio {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	file, err := os.CreateTemp(d.dir, ".audio-*")
	if err != nil {
		return
	}
	path := file.Name()
	defer os.Remove(path)
	_, err = file.Write(data)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return
	}
	if err := os.Rename(path, filepath.Join(d.dir, hex.EncodeToString(key[:])+".audio")); err != nil {
		return
	}
	d.prune()
}

func (d *diskCache) prune() {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return
	}
	type entry struct {
		name string
		time time.Time
		size int64
	}
	var files []entry
	var total int64
	for _, r := range entries {
		name := r.Name()
		key, ok := strings.CutSuffix(name, ".audio")
		if !ok || len(key) != 2*sha1.Size {
			continue
		}
		if _, err := hex.DecodeString(key); err != nil {
			continue
		}
		info, err := r.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if time.Since(info.ModTime()) >= cacheTTL {
			_ = os.Remove(filepath.Join(d.dir, name))
			continue
		}
		total += info.Size()
		files = append(files, entry{name: name, time: info.ModTime(), size: info.Size()})
	}
	slices.SortFunc(files, func(a, b entry) int { return a.time.Compare(b.time) })
	for _, file := range files {
		if total <= maxMemory {
			break
		}
		if os.Remove(filepath.Join(d.dir, file.name)) == nil {
			total -= file.size
		}
	}
}
