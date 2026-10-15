package guestagent

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

func (a *Agent) handleReadFile(w http.ResponseWriter, r *http.Request) {
	var req wire.ReadFileRequest
	if !readJSON(w, r, 64<<10, &req) {
		return
	}
	if req.Path == "" {
		http.Error(w, "empty path", http.StatusBadRequest)
		return
	}
	f, err := os.Open(a.resolve(req.Path))
	if err != nil {
		fileError(w, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		fileError(w, err)
		return
	}
	if st.IsDir() {
		http.Error(w, "is a directory", http.StatusBadRequest)
		return
	}
	buf, err := io.ReadAll(io.LimitReader(f, wire.MaxReadFile+1))
	if err != nil {
		fileError(w, err)
		return
	}
	res := wire.ReadFileResult{Content: buf}
	if len(buf) > wire.MaxReadFile {
		res.Content, res.Truncated = buf[:wire.MaxReadFile], true
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *Agent) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	var req wire.WriteFileRequest
	// base64 makes the content 4/3 larger; 1 MiB of slack covers the rest of the JSON.
	if !readJSON(w, r, wire.MaxWriteFile*4/3+(1<<20), &req) {
		return
	}
	if req.Path == "" {
		http.Error(w, "empty path", http.StatusBadRequest)
		return
	}
	if len(req.Content) > wire.MaxWriteFile {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}
	mode := fs.FileMode(req.Mode).Perm()
	if mode == 0 {
		mode = 0o644
	}
	p := a.resolve(req.Path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		fileError(w, err)
		return
	}
	if err := os.WriteFile(p, req.Content, mode); err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wire.WriteFileResult{Bytes: len(req.Content)})
}

func fileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, fs.ErrPermission):
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
