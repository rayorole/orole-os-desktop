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

// errIsDir answers 400: the path names a directory, not a file.
var errIsDir = errors.New("is a directory")

// File operations run with the bot's permissions: with Config.RunAs set they happen in a helper process started
// as that user (filehelper.go), so a bot cannot use /files/* to read or write what its own commands could not
// (root-owned paths, symlinks to them). Without RunAs (host tests) they run in this process.

func (a *Agent) handleReadFile(w http.ResponseWriter, r *http.Request) {
	var req wire.ReadFileRequest
	if !readJSON(w, r, 64<<10, &req) {
		return
	}
	if req.Path == "" {
		http.Error(w, "empty path", http.StatusBadRequest)
		return
	}
	p := a.resolve(req.Path)
	var (
		buf []byte
		err error
	)
	if a.cfg.RunAs != nil {
		buf, err = a.readFileAs(r.Context(), p, a.cfg.RunAs)
	} else {
		buf, err = readFileCapped(p)
	}
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
	var err error
	if a.cfg.RunAs != nil {
		err = a.writeFileAs(r.Context(), p, req.Content, mode, a.cfg.RunAs)
	} else {
		err = writeFile(p, req.Content, mode)
	}
	if err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wire.WriteFileResult{Bytes: len(req.Content)})
}

// readFileCapped returns up to MaxReadFile+1 bytes of a regular file (one more than the cap shows truncation).
func readFileCapped(p string) ([]byte, error) {
	f, err := openForRead(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, wire.MaxReadFile+1))
}

func openForRead(p string) (*os.File, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if st.IsDir() {
		f.Close()
		return nil, errIsDir
	}
	return f, nil
}

// writeFile creates the parent directories and writes the file, as the current process's user.
func writeFile(p string, content []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, content, mode)
}

func fileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, fs.ErrPermission):
		http.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, errIsDir):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
