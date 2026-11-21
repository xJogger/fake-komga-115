package bangumi

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/diagnostics"
)

const settingArchiveSyncedDump = "bangumi_archive_synced_dump"

type syncRequest struct {
	force bool
}

type storedSeriesMetaRow struct {
	SeriesID            string
	SeriesName          string
	SeriesRelativePath  string
	BgmSubjectID        int64
	BgmScore            sql.NullFloat64
	BgmRank             sql.NullInt64
	Status              string
	StatusLock          bool
	Title               string
	TitleLock           bool
	TitleSort           string
	TitleSortLock       bool
	Summary             string
	SummaryLock         bool
	Publisher           string
	PublisherLock       bool
	TotalBookCount      sql.NullInt64
	TotalBookCountLock  bool
	ReleaseDate         string
	ReleaseDateLock     bool
	GenresJSON          string
	GenresLock          bool
	TagsJSON            string
	TagsLock            bool
	AuthorsJSON         string
	AuthorsLock         bool
	AlternateTitlesJSON string
	AlternateTitlesLock bool
	LinksJSON           string
	LinksLock           bool
	IsNSFW              bool
	NSFWLock            bool
}

// syncOfflineMetadata incrementally diffs and updates all unlocked Bangumi
// metadata fields for every series bound to a bgm_subject_id, purely locally
// from bangumi-archive.db (zero network requests, no cover re-downloads,
// non-empty protection on text/list fields, zero DB writes for unchanged rows).
func (s *Service) syncOfflineMetadata(ctx context.Context, force bool) (int, error) {
	a := s.archive
	a.mu.RLock()
	db := a.db
	status := a.status
	a.mu.RUnlock()
	if db == nil || !status.Ready {
		return 0, nil
	}

	dumpSig := archiveDumpSignature(status)
	if !force && dumpSig != "" {
		if prev, err := s.store.GetSetting(ctx, settingArchiveSyncedDump); err == nil && prev == dumpSig {
			return 0, nil
		}
	}

	started := time.Now()
	rows, err := s.store.DB().QueryContext(ctx, `
SELECT sm.series_id, s.name, s.relative_path, sm.bgm_subject_id,
 sm.bgm_score, sm.bgm_rank,
 sm.status, sm.status_lock,
 sm.title, sm.title_lock,
 sm.title_sort, sm.title_sort_lock,
 sm.summary, sm.summary_lock,
 sm.publisher, sm.publisher_lock,
 sm.total_book_count, sm.total_book_count_lock,
 sm.release_date, sm.release_date_lock,
 sm.genres_json, sm.genres_lock,
 sm.tags_json, sm.tags_lock,
 sm.authors_json, sm.authors_lock,
 sm.alternate_titles_json, sm.alternate_titles_lock,
 sm.links_json, sm.links_lock,
 sm.is_nsfw, sm.nsfw_lock
FROM series_metadata sm
JOIN series s ON s.id = sm.series_id
WHERE sm.bgm_subject_id > 0`)
	if err != nil {
		return 0, err
	}

	bySubject := make(map[int64][]storedSeriesMetaRow)
	var subjectIDs []int64
	for rows.Next() {
		var r storedSeriesMetaRow
		var statusLock, titleLock, titleSortLock, summaryLock, pubLock, volsLock, relLock int
		var genresLock, tagsLock, authorsLock, altLock, linksLock, isNSFW, nsfwLock int
		if err := rows.Scan(
			&r.SeriesID, &r.SeriesName, &r.SeriesRelativePath, &r.BgmSubjectID,
			&r.BgmScore, &r.BgmRank,
			&r.Status, &statusLock,
			&r.Title, &titleLock,
			&r.TitleSort, &titleSortLock,
			&r.Summary, &summaryLock,
			&r.Publisher, &pubLock,
			&r.TotalBookCount, &volsLock,
			&r.ReleaseDate, &relLock,
			&r.GenresJSON, &genresLock,
			&r.TagsJSON, &tagsLock,
			&r.AuthorsJSON, &authorsLock,
			&r.AlternateTitlesJSON, &altLock,
			&r.LinksJSON, &linksLock,
			&isNSFW, &nsfwLock,
		); err != nil {
			rows.Close()
			return 0, err
		}
		r.StatusLock = statusLock != 0
		r.TitleLock = titleLock != 0
		r.TitleSortLock = titleSortLock != 0
		r.SummaryLock = summaryLock != 0
		r.PublisherLock = pubLock != 0
		r.TotalBookCountLock = volsLock != 0
		r.ReleaseDateLock = relLock != 0
		r.GenresLock = genresLock != 0
		r.TagsLock = tagsLock != 0
		r.AuthorsLock = authorsLock != 0
		r.AlternateTitlesLock = altLock != 0
		r.LinksLock = linksLock != 0
		r.IsNSFW = isNSFW != 0
		r.NSFWLock = nsfwLock != 0
		if _, seen := bySubject[r.BgmSubjectID]; !seen {
			subjectIDs = append(subjectIDs, r.BgmSubjectID)
		}
		bySubject[r.BgmSubjectID] = append(bySubject[r.BgmSubjectID], r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	updatedSeries := 0
	for start := 0; start < len(subjectIDs); start += 250 {
		if err := ctx.Err(); err != nil {
			return updatedSeries, err
		}
		chunk := subjectIDs[start:min(start+250, len(subjectIDs))]
		subjects, err := loadArchiveSubjectsBatch(ctx, db, chunk)
		if err != nil {
			return updatedSeries, err
		}
		if len(subjects) == 0 {
			continue
		}
		n, err := s.applyOfflineMetadataBatch(ctx, subjects, bySubject)
		if err != nil {
			return updatedSeries, err
		}
		updatedSeries += n
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)
	if dumpSig != "" {
		_ = s.store.SetSetting(ctx, settingArchiveSyncedDump, dumpSig)
	}
	a.mu.Lock()
	a.status.LastSyncAt = nowStr
	a.status.LastSyncDump = status.DumpName
	a.status.LastSyncUpdatedSeries = updatedSeries
	a.status.LastSyncError = ""
	a.mu.Unlock()

	if s.logger != nil {
		s.logger.Info("synced offline Bangumi metadata",
			"dump", status.DumpName, "subjects", len(subjectIDs),
			"updated_series", updatedSeries, "duration_ms", time.Since(started).Milliseconds())
	}
	diagnostics.Default().Record(diagnostics.Event{
		Level:      diagnostics.LevelInfo,
		Component:  "bangumi",
		Stage:      "bangumi_archive",
		Action:     "metadata_sync",
		DurationMs: time.Since(started).Milliseconds(),
		Detail:     "incremental offline Bangumi metadata sync completed",
		Meta: map[string]any{
			"dump":          status.DumpName,
			"series":        len(subjectIDs),
			"updatedSeries": updatedSeries,
		},
	})
	return updatedSeries, nil
}

func archiveDumpSignature(st ArchiveStatus) string {
	if st.DumpName == "" && st.DumpCreatedAt == "" && st.DumpDigest == "" {
		return ""
	}
	return st.DumpName + "|" + st.DumpCreatedAt + "|" + st.DumpDigest
}

func loadArchiveSubjectsBatch(ctx context.Context, db *sql.DB, ids []int64) (map[int64]SubjectCandidate, error) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.QueryContext(ctx, `
SELECT id,platform,is_series,is_nsfw,name,name_cn,summary,publisher,status,release_date,
 total_volumes,score,rank,genres_json,tags_json,authors_json,aliases_json
FROM bgm_subjects WHERE id IN `+database.Placeholders(len(ids)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]SubjectCandidate, len(ids))
	for rows.Next() {
		cand, err := scanSubjectCandidateRow(rows)
		if err != nil {
			return nil, err
		}
		out[cand.ID] = cand
	}
	return out, rows.Err()
}

type pendingMetaUpdate struct {
	seriesID            string
	bgmScore            any
	bgmRank             any
	status              string
	title               string
	titleSort           string
	summary             string
	publisher           string
	totalBookCount      any
	releaseDate         string
	genresJSON          string
	tagsJSON            string
	authorsJSON         string
	alternateTitlesJSON string
	linksJSON           string
	isNSFW              int

	genresChanged  bool
	genres         []string
	tagsChanged    bool
	tags           []string
	authorsChanged bool
	authors        []database.Author
}

func (s *Service) applyOfflineMetadataBatch(
	ctx context.Context,
	subjects map[int64]SubjectCandidate,
	bySubject map[int64][]storedSeriesMetaRow,
) (int, error) {
	var updates []pendingMetaUpdate
	for subjectID, cand := range subjects {
		for _, cur := range bySubject[subjectID] {
			if upd, changed := diffOfflineMetadataRow(cur, cand); changed {
				updates = append(updates, upd)
			}
		}
	}
	if len(updates) == 0 {
		return 0, nil
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE shelf_revision SET bulk_mode=1 WHERE id=1`); err != nil {
		return 0, err
	}

	stmt, err := tx.PrepareContext(ctx, `
UPDATE series_metadata SET
 bgm_score=?,bgm_rank=?,
 status=?,title=?,title_sort=?,summary=?,publisher=?,
 total_book_count=?,release_date=?,
 genres_json=?,tags_json=?,authors_json=?,alternate_titles_json=?,links_json=?,
 is_nsfw=?,updated_at=?
WHERE series_id=?`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	delGenres, err := tx.PrepareContext(ctx, `DELETE FROM series_genres WHERE series_id=?`)
	if err != nil {
		return 0, err
	}
	defer delGenres.Close()
	insGenres, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO series_genres(series_id,genre) VALUES(?,?)`)
	if err != nil {
		return 0, err
	}
	defer insGenres.Close()

	delTags, err := tx.PrepareContext(ctx, `DELETE FROM series_tags WHERE series_id=?`)
	if err != nil {
		return 0, err
	}
	defer delTags.Close()
	insTags, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO series_tags(series_id,tag) VALUES(?,?)`)
	if err != nil {
		return 0, err
	}
	defer insTags.Close()

	delAuthors, err := tx.PrepareContext(ctx, `DELETE FROM series_authors WHERE series_id=?`)
	if err != nil {
		return 0, err
	}
	defer delAuthors.Close()
	insAuthors, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO series_authors(series_id,name,role) VALUES(?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer insAuthors.Close()

	for _, u := range updates {
		if _, err := stmt.ExecContext(ctx,
			u.bgmScore, u.bgmRank,
			u.status, u.title, u.titleSort, u.summary, u.publisher,
			u.totalBookCount, u.releaseDate,
			u.genresJSON, u.tagsJSON, u.authorsJSON, u.alternateTitlesJSON, u.linksJSON,
			u.isNSFW, now, u.seriesID,
		); err != nil {
			return 0, err
		}
		if u.genresChanged {
			if _, err := delGenres.ExecContext(ctx, u.seriesID); err != nil {
				return 0, err
			}
			for _, g := range u.genres {
				if _, err := insGenres.ExecContext(ctx, u.seriesID, g); err != nil {
					return 0, err
				}
			}
		}
		if u.tagsChanged {
			if _, err := delTags.ExecContext(ctx, u.seriesID); err != nil {
				return 0, err
			}
			for _, tg := range u.tags {
				if _, err := insTags.ExecContext(ctx, u.seriesID, tg); err != nil {
					return 0, err
				}
			}
		}
		if u.authorsChanged {
			if _, err := delAuthors.ExecContext(ctx, u.seriesID); err != nil {
				return 0, err
			}
			for _, au := range u.authors {
				if _, err := insAuthors.ExecContext(ctx, u.seriesID, au.Name, au.Role); err != nil {
					return 0, err
				}
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE shelf_revision SET bulk_mode=0, catalog=catalog+1 WHERE id=1`); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(updates), nil
}

func diffOfflineMetadataRow(cur storedSeriesMetaRow, cand SubjectCandidate) (pendingMetaUpdate, bool) {
	parsed := ParseSeriesWithPath(cur.SeriesName, cur.SeriesRelativePath)
	inc := ToScrapedMetadataWithQuery(cur.SeriesName, parsed, cand)

	upd := pendingMetaUpdate{
		seriesID:            cur.SeriesID,
		bgmScore:            nullFloatToAny(cur.BgmScore),
		bgmRank:             nullIntToAny(cur.BgmRank),
		status:              cur.Status,
		title:               cur.Title,
		titleSort:           cur.TitleSort,
		summary:             cur.Summary,
		publisher:           cur.Publisher,
		totalBookCount:      nullIntToAny(cur.TotalBookCount),
		releaseDate:         cur.ReleaseDate,
		genresJSON:          cur.GenresJSON,
		tagsJSON:            cur.TagsJSON,
		authorsJSON:         cur.AuthorsJSON,
		alternateTitlesJSON: cur.AlternateTitlesJSON,
		linksJSON:           cur.LinksJSON,
		isNSFW:              boolToInt(cur.IsNSFW),
	}
	changed := false

	// Score & rank always track the latest Bangumi snapshot when present.
	if inc.Score != nil {
		if !cur.BgmScore.Valid || math.Abs(cur.BgmScore.Float64-*inc.Score) > 1e-6 {
			upd.bgmScore = *inc.Score
			changed = true
		}
	}
	if inc.Rank > 0 {
		if !cur.BgmRank.Valid || int(cur.BgmRank.Int64) != inc.Rank {
			upd.bgmRank = inc.Rank
			changed = true
		}
	}

	// Unlocked scalar fields (non-empty guard: never wipe existing values with blanks).
	if !cur.StatusLock {
		if normStatus := normalizeStatusForSync(inc.Status); normStatus != "" && normStatus != cur.Status {
			upd.status = normStatus
			changed = true
		}
	}
	if !cur.TitleLock {
		if nextTitle := strings.TrimSpace(inc.Title); nextTitle != "" && nextTitle != cur.Title {
			upd.title = nextTitle
			changed = true
		}
	}
	if !cur.TitleSortLock {
		nextSort := strings.TrimSpace(inc.TitleSort)
		if nextSort == "" {
			nextSort = upd.title
		}
		if nextSort != "" && nextSort != cur.TitleSort {
			upd.titleSort = nextSort
			changed = true
		}
	}
	if !cur.SummaryLock {
		if nextSummary := strings.TrimSpace(inc.Summary); nextSummary != "" && nextSummary != cur.Summary {
			upd.summary = nextSummary
			changed = true
		}
	}
	if !cur.PublisherLock {
		if nextPub := strings.TrimSpace(inc.Publisher); nextPub != "" && nextPub != cur.Publisher {
			upd.publisher = nextPub
			changed = true
		}
	}
	if !cur.TotalBookCountLock && inc.TotalBookCount != nil && *inc.TotalBookCount > 0 {
		if !cur.TotalBookCount.Valid || int(cur.TotalBookCount.Int64) != *inc.TotalBookCount {
			upd.totalBookCount = *inc.TotalBookCount
			changed = true
		}
	}
	if !cur.ReleaseDateLock {
		if nextRel := strings.TrimSpace(inc.ReleaseDate); nextRel != "" && nextRel != cur.ReleaseDate {
			upd.releaseDate = nextRel
			changed = true
		}
	}

	// Unlocked list fields (non-empty guard: never replace populated lists with empty lists).
	if !cur.GenresLock {
		clean := cleanSyncStrings(inc.Genres)
		if len(clean) > 0 {
			if nextJSON := marshalCompact(clean); nextJSON != cur.GenresJSON {
				upd.genresJSON = nextJSON
				upd.genresChanged = true
				upd.genres = clean
				changed = true
			}
		}
	}
	if !cur.TagsLock {
		clean := cleanSyncStrings(inc.Tags)
		if len(clean) > 0 {
			if nextJSON := marshalCompact(clean); nextJSON != cur.TagsJSON {
				upd.tagsJSON = nextJSON
				upd.tagsChanged = true
				upd.tags = clean
				changed = true
			}
		}
	}
	if !cur.AuthorsLock {
		clean := cleanSyncAuthors(inc.Authors)
		if len(clean) > 0 {
			if nextJSON := marshalCompact(clean); nextJSON != cur.AuthorsJSON {
				upd.authorsJSON = nextJSON
				upd.authorsChanged = true
				upd.authors = clean
				changed = true
			}
		}
	}
	if !cur.AlternateTitlesLock {
		clean := cleanSyncAltTitles(inc.AlternateTitles)
		if len(clean) > 0 {
			if nextJSON := marshalCompact(clean); nextJSON != cur.AlternateTitlesJSON {
				upd.alternateTitlesJSON = nextJSON
				changed = true
			}
		}
	}
	if !cur.LinksLock && len(inc.Links) > 0 {
		if nextJSON := marshalCompact(inc.Links); nextJSON != cur.LinksJSON && (cur.LinksJSON == "" || cur.LinksJSON == "[]") {
			upd.linksJSON = nextJSON
			changed = true
		}
	}
	if !cur.NSFWLock && inc.IsNSFW != cur.IsNSFW {
		upd.isNSFW = boolToInt(inc.IsNSFW)
		changed = true
	}

	return upd, changed
}

func normalizeStatusForSync(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "ENDED", "ONGOING", "ABANDONED", "HIATUS":
		return strings.ToUpper(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func cleanSyncStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		k := strings.ToLower(v)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, v)
	}
	return out
}

func cleanSyncAuthors(values []database.Author) []database.Author {
	seen := make(map[string]bool, len(values))
	out := make([]database.Author, 0, len(values))
	for _, a := range values {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(a.Role))
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

func cleanSyncAltTitles(values []database.AlternateTitle) []database.AlternateTitle {
	seen := make(map[string]bool, len(values))
	out := make([]database.AlternateTitle, 0, len(values))
	for _, a := range values {
		title := strings.TrimSpace(a.Title)
		if title == "" {
			continue
		}
		label := strings.TrimSpace(a.Label)
		if label == "" {
			label = "别名"
		}
		key := strings.ToLower(label) + "\x00" + strings.ToLower(title)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, database.AlternateTitle{Label: label, Title: title})
	}
	return out
}

func marshalCompact(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func nullFloatToAny(v sql.NullFloat64) any {
	if !v.Valid {
		return nil
	}
	return v.Float64
}

func nullIntToAny(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (s *Service) rankWorker(pending <-chan syncRequest) {
	defer close(s.rankDone)
	for {
		select {
		case <-s.ctx.Done():
			return
		case req := <-pending:
			if _, err := s.syncOfflineMetadata(s.ctx, req.force); err != nil && s.ctx.Err() == nil {
				s.archive.mu.Lock()
				s.archive.status.LastSyncError = err.Error()
				s.archive.mu.Unlock()
				if s.logger != nil {
					s.logger.Warn("sync offline Bangumi metadata", "error", err)
				}
				diagnostics.Default().Record(diagnostics.Event{
					Level:     diagnostics.LevelError,
					Component: "bangumi",
					Stage:     "bangumi_archive",
					Action:    "metadata_sync",
					Code:      diagnostics.ClassifyError(err),
					Detail:    err.Error(),
				})
			}
		}
	}
}
