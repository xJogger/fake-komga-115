package webdav

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/xJogger/fake-komga-115/internal/database"
)

const UserAgent = "fake-komga-115/2.0"

var (
	ErrRangeNotSupported = errors.New("WebDAV server did not return 206 Partial Content")
	ErrAuthFailed        = errors.New("WebDAV authentication failed")
	ErrNotFound          = errors.New("WebDAV resource not found")
)

type Entry struct {
	RelPath    string    `json:"relPath"`
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	ETag       string    `json:"etag"`
	IsDir      bool      `json:"isDir"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

type Manager struct {
	mu      sync.Mutex
	clients map[string]*accountRuntime
}

type accountRuntime struct {
	signature string
	http      *http.Client
	limiter   *rate.Limiter
}

func NewManager() *Manager {
	return &Manager{
		clients: make(map[string]*accountRuntime),
	}
}

func ValidateBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("WebDAV base URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return "", errors.New("invalid WebDAV URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("WebDAV URL must start with http:// or https://")
	}
	if parsed.Host == "" {
		return "", errors.New("WebDAV URL must include a host")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("WebDAV base URL must not contain query parameters or fragments")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.User = nil
	return parsed.String(), nil
}

func CleanRemotePath(raw string) string {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if raw == "" || raw == "/" || raw == "." {
		return "/"
	}
	cleaned := path.Clean("/" + raw)
	if cleaned == "" {
		return "/"
	}
	return cleaned
}

func JoinRemotePath(basePath, subPath string) string {
	base := CleanRemotePath(basePath)
	sub := strings.Trim(strings.ReplaceAll(subPath, "\\", "/"), "/")
	if sub == "" || sub == "." {
		return base
	}
	return CleanRemotePath(path.Join(base, sub))
}

func (m *Manager) TestConnection(ctx context.Context, account database.WebDAVAccountSecret) error {
	_, err := m.propfind(ctx, account, "/", "0")
	return err
}

func (m *Manager) ListFolders(
	ctx context.Context,
	account database.WebDAVAccountSecret,
	remoteDir string,
) (current string, parent string, folders []Entry, err error) {
	current = CleanRemotePath(remoteDir)
	if current != "/" {
		parent = path.Dir(current)
		if parent == "." || parent == "" {
			parent = "/"
		}
	}
	entries, err := m.ListDirectory(ctx, account, current, "")
	if err != nil {
		return "", "", nil, err
	}
	folders = make([]Entry, 0)
	for _, entry := range entries {
		if entry.IsDir {
			entry.RelPath = JoinRemotePath(current, entry.Name)
			folders = append(folders, entry)
		}
	}
	slices.SortFunc(folders, func(a, b Entry) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return current, parent, folders, nil
}

// ListDirectory lists immediate children of targetRemoteDir.
// Returned Entry.RelPath is relative to libraryRootPath (using forward slashes).
func (m *Manager) ListDirectory(
	ctx context.Context,
	account database.WebDAVAccountSecret,
	targetRemoteDir string,
	libraryRootPath string,
) ([]Entry, error) {
	items, err := m.propfind(ctx, account, targetRemoteDir, "1")
	if err != nil {
		return nil, err
	}
	rootClean := CleanRemotePath(libraryRootPath)
	out := make([]Entry, 0, len(items))
	for _, item := range items {
		if isIgnoredName(item.Name) {
			continue
		}
		relToRoot := strings.TrimPrefix(item.RelPath, rootClean)
		relToRoot = strings.TrimPrefix(relToRoot, "/")
		if relToRoot == "" {
			continue
		}
		item.RelPath = relToRoot
		out = append(out, item)
	}
	return out, nil
}

func (m *Manager) FetchRange(
	ctx context.Context,
	account database.WebDAVAccountSecret,
	libraryRootPath string,
	fileRelPath string,
	start, end int64,
) ([]byte, error) {
	if end < start || start < 0 {
		return nil, errors.New("invalid range bounds")
	}
	expected := end - start + 1
	rt, err := m.runtimeFor(account)
	if err != nil {
		return nil, err
	}
	if err := rt.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	fullRemotePath := JoinRemotePath(libraryRootPath, fileRelPath)
	targetURL, _, err := buildTargetURL(account.BaseURL, fullRemotePath, false)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	req.Header.Set("Accept-Encoding", "identity")
	if account.Username != "" || account.Password != "" {
		req.SetBasicAuth(account.Username, account.Password)
	}
	resp, err := rt.http.Do(req)
	if err != nil {
		return nil, errors.New("WebDAV range request failed")
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil, ErrRangeNotSupported
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, ErrAuthFailed
	case http.StatusNotFound:
		return nil, ErrNotFound
	case http.StatusPartialContent:
		data, err := io.ReadAll(io.LimitReader(resp.Body, expected+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) != expected {
			return nil, fmt.Errorf("WebDAV range returned %d bytes, expected %d", len(data), expected)
		}
		return data, nil
	default:
		return nil, fmt.Errorf("WebDAV range returned HTTP %d", resp.StatusCode)
	}
}

var propfindBody = []byte(`<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:">
  <d:prop>
    <d:resourcetype/>
    <d:getcontentlength/>
    <d:getlastmodified/>
    <d:getetag/>
    <d:displayname/>
  </d:prop>
</d:propfind>`)

func (m *Manager) propfind(
	ctx context.Context,
	account database.WebDAVAccountSecret,
	remoteDir string,
	depth string,
) ([]Entry, error) {
	rt, err := m.runtimeFor(account)
	if err != nil {
		return nil, err
	}
	if err := rt.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	cleanDir := CleanRemotePath(remoteDir)
	targetURL, expectedSelfPath, err := buildTargetURL(account.BaseURL, cleanDir, true)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", targetURL, bytes.NewReader(propfindBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Depth", depth)
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	if account.Username != "" || account.Password != "" {
		req.SetBasicAuth(account.Username, account.Password)
	}
	resp, err := rt.http.Do(req)
	if err != nil {
		return nil, errors.New("failed to connect to WebDAV server")
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, ErrAuthFailed
	case http.StatusNotFound:
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusMultiStatus && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		return nil, fmt.Errorf("WebDAV PROPFIND returned HTTP %d", resp.StatusCode)
	}
	rawXML, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	baseParsed, err := url.Parse(account.BaseURL)
	if err != nil {
		return nil, err
	}
	basePrefix := strings.TrimRight(decodeURLPath(baseParsed.EscapedPath()), "/")
	return parseMultiStatus(rawXML, basePrefix, expectedSelfPath, cleanDir, depth == "0")
}

type xmlMultiStatus struct {
	Responses []xmlResponse `xml:"response"`
}

type xmlResponse struct {
	Href      string        `xml:"href"`
	PropStats []xmlPropStat `xml:"propstat"`
}

type xmlPropStat struct {
	Status string  `xml:"status"`
	Prop   xmlProp `xml:"prop"`
}

type xmlProp struct {
	ResourceType xmlResourceType `xml:"resourcetype"`
	Length       string          `xml:"getcontentlength"`
	LastModified string          `xml:"getlastmodified"`
	ETag         string          `xml:"getetag"`
	DisplayName  string          `xml:"displayname"`
}

type xmlResourceType struct {
	Collection *struct{} `xml:"collection"`
}

func parseMultiStatus(
	rawXML []byte,
	basePrefix string,
	expectedSelfPath string,
	cleanDir string,
	includeSelf bool,
) ([]Entry, error) {
	var doc xmlMultiStatus
	if err := xml.Unmarshal(rawXML, &doc); err != nil {
		return nil, fmt.Errorf("parse WebDAV XML: %w", err)
	}
	selfTrimmed := strings.TrimRight(expectedSelfPath, "/")
	if selfTrimmed == "" {
		selfTrimmed = "/"
	}
	out := make([]Entry, 0, len(doc.Responses))
	for _, resp := range doc.Responses {
		prop, ok := pickOKProp(resp.PropStats)
		if !ok {
			continue
		}
		decodedPath := extractDecodedPath(resp.Href)
		if decodedPath == "" {
			continue
		}
		trimmed := strings.TrimRight(decodedPath, "/")
		if trimmed == "" {
			trimmed = "/"
		}
		if trimmed == selfTrimmed {
			if !includeSelf {
				continue
			}
		}
		isDir := prop.ResourceType.Collection != nil || strings.HasSuffix(resp.Href, "/")
		relToBase := trimmed
		if basePrefix != "" && basePrefix != "/" {
			if !strings.HasPrefix(relToBase, basePrefix+"/") && relToBase != basePrefix {
				continue
			}
			relToBase = strings.TrimPrefix(relToBase, basePrefix)
		}
		relToBase = CleanRemotePath(relToBase)
		if !includeSelf {
			parentOfEntry := path.Dir(relToBase)
			if parentOfEntry == "." || parentOfEntry == "" {
				parentOfEntry = "/"
			}
			if parentOfEntry != cleanDir {
				continue
			}
		}
		name := path.Base(relToBase)
		if name == "/" || name == "." || name == "" {
			name = strings.TrimSpace(prop.DisplayName)
		}
		var size int64
		if s := strings.TrimSpace(prop.Length); s != "" {
			size, _ = strconv.ParseInt(s, 10, 64)
		}
		modTime := parseWebDAVTime(prop.LastModified)
		etag := strings.Trim(strings.TrimSpace(prop.ETag), `"`)
		out = append(out, Entry{
			RelPath:    relToBase,
			Name:       name,
			Size:       size,
			ETag:       etag,
			IsDir:      isDir,
			ModifiedAt: modTime,
		})
	}
	return out, nil
}

func pickOKProp(stats []xmlPropStat) (xmlProp, bool) {
	if len(stats) == 0 {
		return xmlProp{}, false
	}
	for _, st := range stats {
		s := strings.TrimSpace(st.Status)
		if s == "" || strings.Contains(s, " 200 ") || strings.HasSuffix(s, " 200") {
			return st.Prop, true
		}
	}
	return xmlProp{}, false
}

func extractDecodedPath(href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		parsed, err := url.Parse(href)
		if err != nil {
			return ""
		}
		return decodeURLPath(parsed.EscapedPath())
	}
	if idx := strings.IndexAny(href, "?#"); idx >= 0 {
		href = href[:idx]
	}
	return decodeURLPath(href)
}

func decodeURLPath(escaped string) string {
	if escaped == "" {
		return "/"
	}
	parts := strings.Split(escaped, "/")
	for i, part := range parts {
		if unescaped, err := url.PathUnescape(part); err == nil {
			parts[i] = unescaped
		}
	}
	joined := strings.Join(parts, "/")
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	return joined
}

func buildTargetURL(baseURL, remotePath string, trailingSlash bool) (fullURL string, decodedFullPath string, err error) {
	baseParsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil {
		return "", "", err
	}
	baseDecoded := strings.TrimRight(decodeURLPath(baseParsed.EscapedPath()), "/")
	cleanRel := CleanRemotePath(remotePath)

	var segments []string
	for _, seg := range strings.Split(strings.TrimPrefix(baseDecoded, "/"), "/") {
		if seg != "" {
			segments = append(segments, seg)
		}
	}
	for _, seg := range strings.Split(strings.TrimPrefix(cleanRel, "/"), "/") {
		if seg != "" {
			segments = append(segments, seg)
		}
	}

	escapedSegs := make([]string, len(segments))
	for i, seg := range segments {
		escapedSegs[i] = url.PathEscape(seg)
	}
	rawPath := "/" + strings.Join(escapedSegs, "/")
	decodedPath := "/" + strings.Join(segments, "/")
	if trailingSlash && rawPath != "/" {
		rawPath += "/"
		decodedPath += "/"
	}
	target := *baseParsed
	target.Path = decodedPath
	target.RawPath = rawPath
	return target.String(), decodedPath, nil
}

func parseWebDAVTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Now().UTC()
	}
	layouts := []string{
		http.TimeFormat,
		time.RFC1123,
		time.RFC1123Z,
		time.RFC3339,
		time.RFC3339Nano,
		time.RFC850,
		time.ANSIC,
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Now().UTC()
}

func (m *Manager) runtimeFor(account database.WebDAVAccountSecret) (*accountRuntime, error) {
	baseURL, err := ValidateBaseURL(account.BaseURL)
	if err != nil {
		return nil, err
	}
	rps := account.RatePerSecond
	if rps <= 0 {
		rps = 8
	}
	sig := fmt.Sprintf("%s|%s|%v|%f|%s", baseURL, account.Username, account.InsecureTLS, rps, account.UpdatedAt)
	key := account.ID
	if key == "" {
		key = sig
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.clients[key]; ok && existing.signature == sig {
		return existing, nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if account.InsecureTLS {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit opt-in for LAN self-signed NAS WebDAV
	}
	rt := &accountRuntime{
		signature: sig,
		http: &http.Client{
			Timeout:   90 * time.Second,
			Transport: transport,
		},
		limiter: rate.NewLimiter(rate.Limit(rps), max(1, int(rps))),
	}
	m.clients[key] = rt
	return rt, nil
}

func isIgnoredName(name string) bool {
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
