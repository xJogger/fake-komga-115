package localfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

var (
	ErrPathOutsideRoot = errors.New("resolved path escapes library root")
	ErrNotDirectory    = errors.New("path is not a directory")
)

type Entry struct {
	RelPath    string
	Name       string
	Size       int64
	IsDir      bool
	ModifiedAt time.Time
	RealDirKey string
}

type BrowserFolder struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func CleanRoot(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("local root path is required")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", ErrNotDirectory
	}
	return abs, nil
}

func ResolveSafePath(rootPath, relSlash string) (string, error) {
	rootClean := filepath.Clean(rootPath)
	relSlash = strings.TrimSpace(strings.ReplaceAll(relSlash, "\\", "/"))
	if relSlash == "" || relSlash == "." || relSlash == "/" {
		return rootClean, nil
	}
	cleanSlash := path.Clean("/" + relSlash)
	relOS := filepath.FromSlash(strings.TrimPrefix(cleanSlash, "/"))
	joined := filepath.Clean(filepath.Join(rootClean, relOS))
	relCheck, err := filepath.Rel(rootClean, joined)
	if err != nil {
		return "", err
	}
	if relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", ErrPathOutsideRoot
	}
	return joined, nil
}

func ListDirectory(rootPath, relDir string) ([]Entry, error) {
	targetDir, err := ResolveSafePath(rootPath, relDir)
	if err != nil {
		return nil, err
	}
	dirEntries, err := os.ReadDir(targetDir)
	if err != nil {
		return nil, err
	}
	baseRel := strings.Trim(strings.ReplaceAll(relDir, "\\", "/"), "/")
	if baseRel == "." {
		baseRel = ""
	}
	out := make([]Entry, 0, len(dirEntries))
	for _, de := range dirEntries {
		name := de.Name()
		if isIgnoredSystemName(name) {
			continue
		}
		fullPath := filepath.Join(targetDir, name)
		isRegularOrDir := de.IsDir() || de.Type().IsRegular()
		var info fs.FileInfo
		if isRegularOrDir {
			info, err = de.Info()
		} else {
			info, err = os.Stat(fullPath)
		}
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
				continue
			}
			return nil, fmt.Errorf("stat %s: %w", name, err)
		}
		childRel := name
		if baseRel != "" {
			childRel = baseRel + "/" + name
		}
		realKey := ""
		if info.IsDir() && !isRegularOrDir {
			if resolved, err := filepath.EvalSymlinks(fullPath); err == nil {
				realKey = resolved
			} else {
				realKey = fullPath
			}
		}
		out = append(out, Entry{
			RelPath:    childRel,
			Name:       name,
			Size:       info.Size(),
			IsDir:      info.IsDir(),
			ModifiedAt: info.ModTime().UTC(),
			RealDirKey: realKey,
		})
	}
	return out, nil
}

func BrowseDirectories(rawDir string) (current string, parent string, folders []BrowserFolder, err error) {
	rawDir = strings.TrimSpace(rawDir)
	if rawDir == "" {
		if runtime.GOOS == "windows" {
			return "", "", listWindowsDrives(), nil
		}
		rawDir = "/"
	}
	clean, err := CleanRoot(rawDir)
	if err != nil {
		return "", "", nil, err
	}
	parentDir := filepath.Dir(clean)
	if parentDir == clean {
		if runtime.GOOS == "windows" {
			parentDir = ""
		} else {
			parentDir = ""
		}
	}
	entries, err := os.ReadDir(clean)
	if err != nil {
		return "", "", nil, err
	}
	folders = make([]BrowserFolder, 0)
	for _, entry := range entries {
		name := entry.Name()
		if isIgnoredSystemName(name) {
			continue
		}
		isDir := entry.IsDir()
		if !isDir && !entry.Type().IsRegular() {
			if info, err := os.Stat(filepath.Join(clean, name)); err == nil && info.IsDir() {
				isDir = true
			}
		}
		if !isDir {
			continue
		}
		folders = append(folders, BrowserFolder{
			Name: name,
			Path: filepath.Join(clean, name),
		})
	}
	slices.SortFunc(folders, func(a, b BrowserFolder) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return clean, parentDir, folders, nil
}

func listWindowsDrives() []BrowserFolder {
	var out []BrowserFolder
	for ch := 'A'; ch <= 'Z'; ch++ {
		drive := string(ch) + `:\`
		if info, err := os.Stat(drive); err == nil && info.IsDir() {
			out = append(out, BrowserFolder{Name: drive, Path: drive})
		}
	}
	return out
}

func isIgnoredSystemName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") {
		return true
	}
	switch strings.ToLower(name) {
	case "@eadir", "#recycle", "$recycle.bin", "system volume information", "__macosx", "thumbs.db", "ehthumbs.db", "desktop.ini":
		return true
	default:
		return false
	}
}
