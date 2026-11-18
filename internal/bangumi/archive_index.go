package bangumi

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/xJogger/fake-komga-115/internal/database"
)

const (
	DefaultArchiveMetaURL = "https://raw.githubusercontent.com/bangumi/Archive/master/aux/latest.json"
	archiveUserAgent      = "xJogger/fake-komga-115/2.0 (https://github.com/xJogger/fake-komga-115)"
)

type ArchiveStatus struct {
	Ready         bool   `json:"ready"`
	DumpName      string `json:"dumpName"`
	DumpCreatedAt string `json:"dumpCreatedAt"`
	ImportedAt    string `json:"importedAt"`
	SubjectCount  int64  `json:"subjectCount"`
	Updating      bool   `json:"updating"`
	Phase         string `json:"phase"`
	Downloaded    int64  `json:"downloadedBytes"`
	TotalBytes    int64  `json:"totalBytes"`
	LastError     string `json:"lastError"`
}

type ArchiveIndex struct {
	dir     string
	dbPath  string
	metaURL string
	logger  *slog.Logger

	clientMu   sync.RWMutex
	httpClient *http.Client
	proxyRaw   string

	mu     sync.RWMutex
	db     *sql.DB
	status ArchiveStatus
	cancel context.CancelFunc
}

func NewArchiveIndex(dir string, logger *slog.Logger) (*ArchiveIndex, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	tr, _ := NewBangumiTransport("", 30*time.Second)
	idx := &ArchiveIndex{
		dir:     dir,
		dbPath:  filepath.Join(dir, "bangumi-archive.db"),
		metaURL: DefaultArchiveMetaURL,
		httpClient: &http.Client{
			Transport: tr,
		},
		logger: logger,
	}
	idx.openExistingIfPresent()
	return idx, nil
}

func (a *ArchiveIndex) SetProxyURL(proxyURL string) error {
	tr, err := NewBangumiTransport(proxyURL, 30*time.Second)
	if err != nil {
		return err
	}
	a.clientMu.Lock()
	a.httpClient = &http.Client{
		Transport: tr,
	}
	a.proxyRaw = proxyURL
	a.clientMu.Unlock()
	return nil
}

func (a *ArchiveIndex) clientAndProxy() (*http.Client, string) {
	a.clientMu.RLock()
	defer a.clientMu.RUnlock()
	return a.httpClient, a.proxyRaw
}

func (a *ArchiveIndex) Close() error {
	a.mu.Lock()
	if a.cancel != nil {
		a.cancel()
	}
	db := a.db
	a.db = nil
	a.mu.Unlock()
	if db != nil {
		return db.Close()
	}
	return nil
}

func (a *ArchiveIndex) Status() ArchiveStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.status
}

func (a *ArchiveIndex) Ready() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.status.Ready && a.db != nil
}

func (a *ArchiveIndex) StartOnlineUpdate() (ArchiveStatus, error) {
	a.mu.Lock()
	if a.status.Updating {
		st := a.status
		a.mu.Unlock()
		return st, errors.New("Bangumi archive update is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.status.Updating = true
	a.status.Phase = "fetching_metadata"
	a.status.Downloaded = 0
	a.status.TotalBytes = 0
	a.status.LastError = ""
	st := a.status
	a.mu.Unlock()

	go a.runOnlineUpdate(ctx)
	return st, nil
}

func (a *ArchiveIndex) CancelUpdate() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.status.Updating || a.cancel == nil {
		return false
	}
	a.cancel()
	return true
}

type latestDumpMeta struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	CreatedAt          string `json:"created_at"`
	Digest             string `json:"digest"`
	Size               int64  `json:"size"`
}

func (a *ArchiveIndex) runOnlineUpdate(ctx context.Context) {
	client, proxyRaw := a.clientAndProxy()
	var finishErr error
	defer func() {
		a.mu.Lock()
		a.status.Updating = false
		a.cancel = nil
		if finishErr != nil {
			if errors.Is(finishErr, context.Canceled) {
				a.status.Phase = "canceled"
				a.status.LastError = "更新已取消"
			} else {
				a.status.Phase = "failed"
				a.status.LastError = SanitizeProxyError(finishErr, proxyRaw)
			}
		} else {
			a.status.Phase = "idle"
			a.status.LastError = ""
		}
		a.mu.Unlock()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.metaURL, nil)
	if err != nil {
		finishErr = err
		return
	}
	req.Header.Set("User-Agent", archiveUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		finishErr = fmt.Errorf("fetch latest.json: %w", err)
		return
	}
	metaBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if readErr != nil {
		finishErr = readErr
		return
	}
	if resp.StatusCode != http.StatusOK {
		finishErr = fmt.Errorf("latest.json returned HTTP %d", resp.StatusCode)
		return
	}
	var meta latestDumpMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		finishErr = fmt.Errorf("parse latest.json: %w", err)
		return
	}
	if meta.BrowserDownloadURL == "" {
		finishErr = errors.New("latest.json missing browser_download_url")
		return
	}

	a.mu.Lock()
	a.status.Phase = "downloading"
	a.status.TotalBytes = meta.Size
	a.mu.Unlock()

	zipTemp := filepath.Join(a.dir, ".bangumi-dump-download.zip")
	_ = os.Remove(zipTemp)
	defer os.Remove(zipTemp)

	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, meta.BrowserDownloadURL, nil)
	if err != nil {
		finishErr = err
		return
	}
	dlReq.Header.Set("User-Agent", archiveUserAgent)
	dlResp, err := client.Do(dlReq)
	if err != nil {
		finishErr = fmt.Errorf("download archive zip: %w", err)
		return
	}
	if dlResp.StatusCode != http.StatusOK {
		dlResp.Body.Close()
		finishErr = fmt.Errorf("archive download returned HTTP %d", dlResp.StatusCode)
		return
	}
	outFile, err := os.OpenFile(zipTemp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		dlResp.Body.Close()
		finishErr = err
		return
	}
	hasher := sha256.New()
	buf := make([]byte, 256<<10)
	var downloaded int64
	for {
		n, rErr := dlResp.Body.Read(buf)
		if n > 0 {
			if _, wErr := outFile.Write(buf[:n]); wErr != nil {
				outFile.Close()
				dlResp.Body.Close()
				finishErr = wErr
				return
			}
			_, _ = hasher.Write(buf[:n])
			downloaded += int64(n)
			a.mu.Lock()
			a.status.Downloaded = downloaded
			a.mu.Unlock()
		}
		if rErr == io.EOF {
			break
		}
		if rErr != nil {
			outFile.Close()
			dlResp.Body.Close()
			finishErr = rErr
			return
		}
	}
	dlResp.Body.Close()
	if err := outFile.Close(); err != nil {
		finishErr = err
		return
	}

	if expectedHex := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(meta.Digest)), "sha256:"); len(expectedHex) == 64 {
		actualHex := hex.EncodeToString(hasher.Sum(nil))
		if actualHex != expectedHex {
			finishErr = fmt.Errorf("archive SHA256 mismatch: got %s, expected %s", actualHex, expectedHex)
			return
		}
	}

	a.mu.Lock()
	a.status.Phase = "indexing"
	a.mu.Unlock()

	if err := a.ImportFromZipFile(ctx, zipTemp, meta.Name, meta.CreatedAt); err != nil {
		finishErr = err
		return
	}
	if a.logger != nil {
		a.logger.Info("Bangumi offline archive updated", "dump", meta.Name)
	}
}

type archiveSubjectLine struct {
	ID       int64      `json:"id"`
	Type     int        `json:"type"`
	Platform int        `json:"platform"`
	Name     string     `json:"name"`
	NameCN   string     `json:"name_cn"`
	Infobox  string     `json:"infobox"`
	Summary  string     `json:"summary"`
	NSFW     bool       `json:"nsfw"`
	Tags     []TagCount `json:"tags"`
	MetaTags []string   `json:"meta_tags"`
	Score    float64    `json:"score"`
	Rank     int        `json:"rank"`
	Date     string     `json:"date"`
	Series   bool       `json:"series"`
}

type archiveRelationLine struct {
	SubjectID        int64 `json:"subject_id"`
	RelationType     int   `json:"relation_type"`
	RelatedSubjectID int64 `json:"related_subject_id"`
}

type archiveSubjPersonLine struct {
	PersonID  int64 `json:"person_id"`
	SubjectID int64 `json:"subject_id"`
	Position  int   `json:"position"`
}

type archivePersonLine struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Infobox string `json:"infobox"`
}

func (a *ArchiveIndex) ImportFromZipFile(
	ctx context.Context,
	zipPath, dumpName, dumpCreatedAt string,
) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open archive zip: %w", err)
	}
	defer zr.Close()

	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[filepath.Base(f.Name)] = f
	}
	subjFile, ok := files["subject.jsonlines"]
	if !ok {
		return errors.New("subject.jsonlines not found in archive zip")
	}

	// Pass 1: collect non-NSFW book subject IDs and series IDs.
	bookIDs := make(map[int64]bool, 200000)
	if err := streamZipLines(ctx, subjFile, func(line []byte) error {
		var header struct {
			ID     int64 `json:"id"`
			Type   int   `json:"type"`
			NSFW   bool  `json:"nsfw"`
			Series bool  `json:"series"`
		}
		if err := json.Unmarshal(line, &header); err != nil {
			return nil
		}
		if header.Type == 1 && !header.NSFW && header.ID > 0 {
			bookIDs[header.ID] = header.Series
		}
		return nil
	}); err != nil {
		return err
	}

	// Pass 2: identify tankobon single volumes linked from a parent Series (relation_type == 1003).
	childTankobons := make(map[int64]struct{}, 150000)
	if relFile, ok := files["subject-relations.jsonlines"]; ok {
		if err := streamZipLines(ctx, relFile, func(line []byte) error {
			var rel archiveRelationLine
			if err := json.Unmarshal(line, &rel); err != nil {
				return nil
			}
			if rel.RelationType == 1003 {
				if isParentSeries, exists := bookIDs[rel.SubjectID]; exists && isParentSeries {
					if isChildSeries, childExists := bookIDs[rel.RelatedSubjectID]; childExists && !isChildSeries {
						childTankobons[rel.RelatedSubjectID] = struct{}{}
					}
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}

	// Pass 3: collect staff links for target book subjects.
	type staffRef struct {
		personID int64
		role     string
	}
	subjectStaff := make(map[int64][]staffRef)
	neededPersons := make(map[int64]string)
	if spFile, ok := files["subject-persons.jsonlines"]; ok {
		if err := streamZipLines(ctx, spFile, func(line []byte) error {
			var sp archiveSubjPersonLine
			if err := json.Unmarshal(line, &sp); err != nil {
				return nil
			}
			if _, ok := bookIDs[sp.SubjectID]; !ok {
				return nil
			}
			if _, isTankobon := childTankobons[sp.SubjectID]; isTankobon {
				return nil
			}
			role := mapStaffPosition(sp.Position)
			if role == "" {
				return nil
			}
			subjectStaff[sp.SubjectID] = append(subjectStaff[sp.SubjectID], staffRef{personID: sp.PersonID, role: role})
			neededPersons[sp.PersonID] = ""
			return nil
		}); err != nil {
			return err
		}
	}
	if personFile, ok := files["person.jsonlines"]; ok && len(neededPersons) > 0 {
		if err := streamZipLines(ctx, personFile, func(line []byte) error {
			var p archivePersonLine
			if err := json.Unmarshal(line, &p); err != nil {
				return nil
			}
			if _, needed := neededPersons[p.ID]; !needed {
				return nil
			}
			info := ParseRawInfobox(p.Infobox)
			displayName := strings.TrimSpace(info.TitleCN)
			if displayName == "" {
				displayName = strings.TrimSpace(p.Name)
			}
			neededPersons[p.ID] = displayName
			return nil
		}); err != nil {
			return err
		}
	}

	// Pass 4: build SQLite index in staging file.
	stagingPath := filepath.Join(a.dir, ".bangumi-archive.staging.db")
	_ = os.Remove(stagingPath)
	defer os.Remove(stagingPath)

	buildDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(stagingPath))
	if err != nil {
		return err
	}
	defer buildDB.Close()

	if _, err := buildDB.ExecContext(ctx, `
PRAGMA journal_mode = MEMORY;
PRAGMA synchronous = OFF;
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE bgm_subjects (
  id INTEGER PRIMARY KEY,
  platform INTEGER NOT NULL,
  is_series INTEGER NOT NULL,
  name TEXT NOT NULL,
  name_cn TEXT NOT NULL,
  summary TEXT NOT NULL,
  publisher TEXT NOT NULL,
  status TEXT NOT NULL,
  release_date TEXT NOT NULL,
  total_volumes INTEGER,
  score REAL,
  rank INTEGER NOT NULL,
  genres_json TEXT NOT NULL,
  tags_json TEXT NOT NULL,
  authors_json TEXT NOT NULL,
  aliases_json TEXT NOT NULL
);
CREATE TABLE bgm_terms (
  term_key TEXT NOT NULL,
  subject_id INTEGER NOT NULL,
  is_primary INTEGER NOT NULL,
  PRIMARY KEY(term_key, subject_id)
);
`); err != nil {
		return err
	}

	tx, err := buildDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	subjStmt, err := tx.PrepareContext(ctx, `
INSERT INTO bgm_subjects(
 id,platform,is_series,name,name_cn,summary,publisher,status,release_date,
 total_volumes,score,rank,genres_json,tags_json,authors_json,aliases_json
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	termStmt, err := tx.PrepareContext(ctx, `
INSERT INTO bgm_terms(term_key,subject_id,is_primary) VALUES(?,?,?)
ON CONFLICT(term_key,subject_id) DO UPDATE SET is_primary=MAX(bgm_terms.is_primary, excluded.is_primary)`)
	if err != nil {
		subjStmt.Close()
		tx.Rollback()
		return err
	}

	var importedCount int64
	err = streamZipLines(ctx, subjFile, func(line []byte) error {
		var s archiveSubjectLine
		if err := json.Unmarshal(line, &s); err != nil {
			return nil
		}
		if s.Type != 1 || s.NSFW || s.ID <= 0 {
			return nil
		}
		if _, isChild := childTankobons[s.ID]; isChild {
			return nil
		}
		wiki := ParseRawInfobox(s.Infobox)
		nameCN := strings.TrimSpace(s.NameCN)
		if nameCN == "" {
			nameCN = wiki.TitleCN
		}
		authors := wiki.Authors
		for _, ref := range subjectStaff[s.ID] {
			if pName := neededPersons[ref.personID]; pName != "" {
				authors = append(authors, database.Author{Name: pName, Role: ref.role})
			}
		}
		authors = dedupeAuthors(authors)
		genres, tags := ClassifyTagsAndGenres(s.MetaTags, s.Tags)
		relDate := strings.TrimSpace(s.Date)
		if relDate == "" {
			relDate = wiki.ReleaseDate
		}
		var scorePtr any
		if s.Score > 0 {
			scorePtr = s.Score
		}
		var volsPtr any
		if wiki.TotalVolumes != nil && *wiki.TotalVolumes > 0 {
			volsPtr = *wiki.TotalVolumes
		}
		genresJSON, _ := json.Marshal(genres)
		tagsJSON, _ := json.Marshal(tags)
		authorsJSON, _ := json.Marshal(authors)
		aliasesJSON, _ := json.Marshal(wiki.Aliases)
		isSeriesInt := 0
		if s.Series {
			isSeriesInt = 1
		}
		if _, err := subjStmt.ExecContext(ctx,
			s.ID, s.Platform, isSeriesInt, strings.TrimSpace(s.Name), nameCN,
			strings.TrimSpace(s.Summary), wiki.Publisher, wiki.Status, relDate,
			volsPtr, scorePtr, s.Rank,
			string(genresJSON), string(tagsJSON), string(authorsJSON), string(aliasesJSON),
		); err != nil {
			return err
		}

		insertTerm := func(raw string, primary int) error {
			k := NormalizeMatchKey(raw)
			if k == "" {
				return nil
			}
			if _, err := termStmt.ExecContext(ctx, k, s.ID, primary); err != nil {
				return err
			}
			stripped := NormalizeMatchKey(editionRe.ReplaceAllString(ToSimplified(raw), " "))
			if stripped != "" && stripped != k && len([]rune(stripped)) >= 2 {
				if _, err := termStmt.ExecContext(ctx, stripped, s.ID, 0); err != nil {
					return err
				}
			}
			return nil
		}
		if err := insertTerm(nameCN, 1); err != nil {
			return err
		}
		if err := insertTerm(s.Name, 1); err != nil {
			return err
		}
		for _, alt := range wiki.Aliases {
			if err := insertTerm(alt.Title, 0); err != nil {
				return err
			}
		}
		importedCount++
		return nil
	})
	subjStmt.Close()
	termStmt.Close()
	if err != nil {
		tx.Rollback()
		return err
	}
	importedAt := time.Now().UTC().Format(time.RFC3339)
	for k, v := range map[string]string{
		"dump_name":       dumpName,
		"dump_created_at": dumpCreatedAt,
		"imported_at":     importedAt,
		"subject_count":   fmt.Sprintf("%d", importedCount),
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?)`, k, v); err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if _, err := buildDB.ExecContext(ctx, `
CREATE INDEX idx_bgm_terms_key ON bgm_terms(term_key);
PRAGMA journal_mode = DELETE;
`); err != nil {
		return err
	}
	if err := buildDB.Close(); err != nil {
		return err
	}
	if err := os.Chmod(stagingPath, 0o600); err != nil {
		return err
	}

	a.mu.Lock()
	oldDB := a.db
	a.db = nil
	a.mu.Unlock()
	if oldDB != nil {
		_ = oldDB.Close()
	}

	if err := os.Rename(stagingPath, a.dbPath); err != nil {
		return err
	}
	a.openExistingIfPresent()
	return nil
}

func (a *ArchiveIndex) openExistingIfPresent() {
	if _, err := os.Stat(a.dbPath); err != nil {
		return
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(a.dbPath)+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("open Bangumi archive index", "error", err)
		}
		return
	}
	db.SetMaxOpenConns(4)
	meta := map[string]string{}
	rows, err := db.Query(`SELECT key,value FROM meta`)
	if err != nil {
		db.Close()
		return
	}
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			meta[k] = v
		}
	}
	rows.Close()
	var count int64
	_ = db.QueryRow(`SELECT count(*) FROM bgm_subjects`).Scan(&count)

	a.mu.Lock()
	if a.db != nil {
		_ = a.db.Close()
	}
	a.db = db
	a.status.Ready = count > 0
	a.status.DumpName = meta["dump_name"]
	a.status.DumpCreatedAt = meta["dump_created_at"]
	a.status.ImportedAt = meta["imported_at"]
	a.status.SubjectCount = count
	if !a.status.Updating {
		a.status.Phase = "idle"
	}
	a.mu.Unlock()
}

func (a *ArchiveIndex) Search(
	ctx context.Context,
	parsed ParsedQuery,
	targetIsOneShot bool,
	limit int,
) ([]SubjectCandidate, error) {
	a.mu.RLock()
	db := a.db
	a.mu.RUnlock()
	if db == nil {
		return nil, errors.New("Bangumi offline index is not initialized")
	}
	if limit <= 0 || limit > 50 {
		limit = 15
	}

	var keys []string
	for _, cand := range parsed.Candidates {
		if k := NormalizeMatchKey(cand); k != "" {
			keys = appendUniqueText(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}

	subjectIDSet := make(map[int64]bool)
	placeholders := database.Placeholders(len(keys))
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT subject_id FROM bgm_terms WHERE term_key IN `+placeholders+` LIMIT 120`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			subjectIDSet[id] = true
		}
	}
	rows.Close()

	// Prefix fallback on primary key if exact term_key returned few hits and key has >= 3 runes.
	if len(subjectIDSet) < 8 && len([]rune(keys[0])) >= 3 {
		pRows, err := db.QueryContext(ctx,
			`SELECT DISTINCT subject_id FROM bgm_terms WHERE term_key>=? AND term_key<? LIMIT 60`,
			keys[0], keys[0]+"\uffff")
		if err == nil {
			for pRows.Next() {
				var id int64
				if pRows.Scan(&id) == nil {
					subjectIDSet[id] = true
				}
			}
			pRows.Close()
		}
	}
	if len(subjectIDSet) == 0 {
		return nil, nil
	}

	idArgs := make([]any, 0, len(subjectIDSet))
	for id := range subjectIDSet {
		idArgs = append(idArgs, id)
	}
	sRows, err := db.QueryContext(ctx, `
SELECT id,platform,is_series,name,name_cn,summary,publisher,status,release_date,
 total_volumes,score,rank,genres_json,tags_json,authors_json,aliases_json
FROM bgm_subjects WHERE id IN `+database.Placeholders(len(idArgs)), idArgs...)
	if err != nil {
		return nil, err
	}
	defer sRows.Close()

	var candidates []SubjectCandidate
	for sRows.Next() {
		cand, err := scanSubjectCandidateRow(sRows)
		if err != nil {
			return nil, err
		}
		cand.Confidence = ScoreCandidate(parsed, cand, targetIsOneShot)
		if cand.Confidence > 0 {
			candidates = append(candidates, cand)
		}
	}
	if err := sRows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(candidates, func(a, b SubjectCandidate) int {
		if a.Confidence > b.Confidence {
			return -1
		}
		if a.Confidence < b.Confidence {
			return 1
		}
		if a.IsSeries != b.IsSeries {
			if a.IsSeries {
				return -1
			}
			return 1
		}
		return int(a.ID - b.ID)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

func (a *ArchiveIndex) SubjectByID(ctx context.Context, subjectID int64) (SubjectCandidate, bool, error) {
	a.mu.RLock()
	db := a.db
	a.mu.RUnlock()
	if db == nil {
		return SubjectCandidate{}, false, nil
	}
	row := db.QueryRowContext(ctx, `
SELECT id,platform,is_series,name,name_cn,summary,publisher,status,release_date,
 total_volumes,score,rank,genres_json,tags_json,authors_json,aliases_json
FROM bgm_subjects WHERE id=?`, subjectID)
	cand, err := scanSubjectCandidateRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SubjectCandidate{}, false, nil
	}
	if err != nil {
		return SubjectCandidate{}, false, err
	}
	cand.Confidence = 1.0
	return cand, true, nil
}

func scanSubjectCandidateRow(scanner interface{ Scan(...any) error }) (SubjectCandidate, error) {
	var (
		c                          SubjectCandidate
		isSeries                   int
		totalVols                  sql.NullInt64
		score                      sql.NullFloat64
		genresJSON, tagsJSON       string
		authorsJSON, altTitlesJSON string
	)
	if err := scanner.Scan(
		&c.ID, &c.Platform, &isSeries, &c.Name, &c.NameCN, &c.Summary,
		&c.Publisher, &c.Status, &c.ReleaseDate, &totalVols, &score, &c.Rank,
		&genresJSON, &tagsJSON, &authorsJSON, &altTitlesJSON,
	); err != nil {
		return SubjectCandidate{}, err
	}
	c.PlatformLabel = PlatformName(c.Platform)
	c.IsSeries = isSeries != 0
	c.CoverURL = CoverEndpointURL(c.ID)
	if totalVols.Valid && totalVols.Int64 > 0 {
		v := int(totalVols.Int64)
		c.TotalVolumes = &v
	}
	if score.Valid && score.Float64 > 0 {
		v := score.Float64
		c.Score = &v
	}
	_ = json.Unmarshal([]byte(genresJSON), &c.Genres)
	_ = json.Unmarshal([]byte(tagsJSON), &c.Tags)
	_ = json.Unmarshal([]byte(authorsJSON), &c.Authors)
	_ = json.Unmarshal([]byte(altTitlesJSON), &c.AlternateTitles)
	return c, nil
}

func streamZipLines(ctx context.Context, zf *zip.File, fn func([]byte) error) error {
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	reader := bufio.NewReaderSize(rc, 512<<10)
	lineCount := 0
	for {
		lineCount++
		if lineCount%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		line, err := reader.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			if callErr := fn(line); callErr != nil {
				return callErr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func mapStaffPosition(pos int) string {
	switch pos {
	case 2001, 2003, 2007: // 作者 / 原作 / 脚本
		return "writer"
	case 2002, 2004, 2005, 2008: // 作画 / 插图 / 人物原案
		return "penciller"
	default:
		return ""
	}
}

func dedupeAuthors(items []database.Author) []database.Author {
	out := make([]database.Author, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, a := range items {
		name := strings.TrimSpace(a.Name)
		role := strings.TrimSpace(a.Role)
		if name == "" {
			continue
		}
		if role == "" {
			role = "writer"
		}
		key := strings.ToLower(name) + "\x00" + role
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, database.Author{Name: name, Role: role})
	}
	return out
}
