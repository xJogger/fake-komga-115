package localfs

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// DocumentBackend is supplied only by the embedded Android app. List returns
// directory metadata; Open transfers ownership of a read-only descriptor.
type DocumentBackend interface {
	List(root, rel string) (string, error)
	Open(root, rel string) (int32, error)
}

var documents struct {
	sync.RWMutex
	backend DocumentBackend
}

func SetDocumentBackend(backend DocumentBackend) {
	documents.Lock()
	defer documents.Unlock()
	documents.backend = backend
}

func documentBackend() DocumentBackend {
	documents.RLock()
	defer documents.RUnlock()
	return documents.backend
}

func IsDocumentRoot(s string) bool { return strings.HasPrefix(s, "content://") }

func validDocumentWord(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00")
}

func validDocumentRel(s string) bool {
	for _, part := range strings.Split(s, "/") {
		if !validDocumentWord(part) {
			return false
		}
	}
	return true
}

func listDocuments(root, rel string) ([]Entry, error) {
	b := documentBackend()
	if b == nil {
		return nil, errors.New("Android document folders are only supported by the Android app")
	}
	u, err := url.Parse(root)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid Android document folder")
	}
	if rel == "." {
		rel = ""
	} else if !validDocumentRel(rel) {
		return nil, ErrPathOutsideRoot
	}
	raw, err := b.List(root, rel)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID, Name    string
		IsDir       bool
		Size, MTime int64
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil, errors.New("invalid Android document listing")
	}
	out := make([]Entry, 0, len(rows))
	names := make(map[string]bool)
	for _, row := range rows {
		if !validDocumentWord(row.Name) || isIgnoredSystemName(row.Name) {
			continue
		}
		if names[row.Name] {
			return nil, errors.New("Android folder contains duplicate names")
		}
		names[row.Name] = true
		e := Entry{RelPath: row.Name, Name: row.Name, IsDir: row.IsDir, Size: max(0, row.Size)}
		if rel != "" {
			e.RelPath = rel + "/" + row.Name
		}
		if row.MTime > 0 {
			e.ModifiedAt = time.UnixMilli(row.MTime).UTC()
		}
		if row.IsDir && row.ID != "" {
			e.RealDirKey = "android-doc:" + row.ID
		}
		out = append(out, e)
	}
	return out, nil
}

func OpenBook(root, rel string) (*os.File, error) {
	if !IsDocumentRoot(root) {
		fullPath, err := ResolveSafePath(root, rel)
		if err != nil {
			return nil, err
		}
		return os.Open(fullPath)
	}
	if !validDocumentRel(rel) {
		return nil, ErrPathOutsideRoot
	}
	b := documentBackend()
	if b == nil {
		return nil, errors.New("Android document access is unavailable")
	}
	fd, err := b.Open(root, rel)
	if err != nil {
		return nil, err
	}
	if fd < 0 {
		return nil, errors.New("Android document provider returned an invalid descriptor")
	}
	file := os.NewFile(uintptr(fd), "android-document")
	if _, err := file.Seek(0, io.SeekCurrent); err != nil {
		file.Close()
		return nil, errors.New("Android document provider does not support random access to this archive")
	}
	return file, nil
}
