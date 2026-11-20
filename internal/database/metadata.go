package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

type ScrapedMetadata struct {
	SubjectID       int64
	Confidence      float64
	CoverURL        string
	Score           *float64
	Rank            int
	Status          string
	Title           string
	TitleSort       string
	Summary         string
	Publisher       string
	Language        string
	TotalBookCount  *int
	ReleaseDate     string
	Genres          []string
	Tags            []string
	Authors         []Author
	AlternateTitles []AlternateTitle
	Links           []WebLink
	IsNSFW          bool
}

type SeriesMetadataPatch struct {
	Status              *string           `json:"status"`
	StatusLock          *bool             `json:"statusLock"`
	Title               *string           `json:"title"`
	TitleLock           *bool             `json:"titleLock"`
	TitleSort           *string           `json:"titleSort"`
	TitleSortLock       *bool             `json:"titleSortLock"`
	Summary             *string           `json:"summary"`
	SummaryLock         *bool             `json:"summaryLock"`
	ReadingDirection    *string           `json:"readingDirection"`
	ReadingDirLock      *bool             `json:"readingDirectionLock"`
	Publisher           *string           `json:"publisher"`
	PublisherLock       *bool             `json:"publisherLock"`
	AgeRating           *int              `json:"ageRating"`
	ClearAgeRating      bool              `json:"clearAgeRating"`
	AgeRatingLock       *bool             `json:"ageRatingLock"`
	Language            *string           `json:"language"`
	LanguageLock        *bool             `json:"languageLock"`
	TotalBookCount      *int              `json:"totalBookCount"`
	ClearTotalBookCount bool              `json:"clearTotalBookCount"`
	TotalBookCountLock  *bool             `json:"totalBookCountLock"`
	ReleaseDate         *string           `json:"releaseDate"`
	ReleaseDateLock     *bool             `json:"releaseDateLock"`
	Genres              *[]string         `json:"genres"`
	GenresLock          *bool             `json:"genresLock"`
	Tags                *[]string         `json:"tags"`
	TagsLock            *bool             `json:"tagsLock"`
	Authors             *[]Author         `json:"authors"`
	AuthorsLock         *bool             `json:"authorsLock"`
	AlternateTitles     *[]AlternateTitle `json:"alternateTitles"`
	AlternateTitlesLock *bool             `json:"alternateTitlesLock"`
	Links               *[]WebLink        `json:"links"`
	LinksLock           *bool             `json:"linksLock"`
	SharingLabels       *[]string         `json:"sharingLabels"`
	SharingLabelsLock   *bool             `json:"sharingLabelsLock"`
	CoverSource         *string           `json:"coverSource"`
	IsNSFW              *bool             `json:"isNsfw"`
	NSFWLock            *bool             `json:"nsfwLock"`
}

func defaultSeriesMetadata(series Series) SeriesMetadata {
	return SeriesMetadata{
		SeriesID:         series.ID,
		ScrapeStatus:     ScrapeStatusNone,
		CoverSource:      CoverSourceAuto,
		Status:           "ONGOING",
		Title:            series.Name,
		TitleSort:        series.Name,
		ReadingDirection: "LEFT_TO_RIGHT",
		Genres:           []string{},
		Tags:             []string{},
		Authors:          []Author{},
		AlternateTitles:  []AlternateTitle{},
		Links:            []WebLink{},
		SharingLabels:    []string{},
		UpdatedAt:        series.UpdatedAt,
	}
}

const seriesMetadataColumns = `
series_id,bgm_subject_id,scrape_status,scrape_confidence,scrape_synced_at,
cover_source,bgm_cover_url,bgm_cover_path,bgm_score,bgm_rank,
status,status_lock,title,title_lock,title_sort,title_sort_lock,
summary,summary_lock,reading_direction,reading_direction_lock,
publisher,publisher_lock,age_rating,age_rating_lock,
language,language_lock,total_book_count,total_book_count_lock,
release_date,release_date_lock,
genres_json,genres_lock,tags_json,tags_lock,
authors_json,authors_lock,alternate_titles_json,alternate_titles_lock,
links_json,links_lock,sharing_labels_json,sharing_labels_lock,is_nsfw,nsfw_lock,updated_at`

func scanSeriesMetadataRow(scanner interface{ Scan(...any) error }) (SeriesMetadata, error) {
	var (
		m                  SeriesMetadata
		bgmID              sql.NullInt64
		syncedAt           sql.NullString
		bgmScore           sql.NullFloat64
		bgmRank            sql.NullInt64
		ageRating          sql.NullInt64
		totalBooks         sql.NullInt64
		statusLock         int
		titleLock          int
		titleSortLock      int
		summaryLock        int
		readingDirLock     int
		publisherLock      int
		ageRatingLock      int
		languageLock       int
		totalBookCountLock int
		releaseDateLock    int
		genresJSON         string
		genresLock         int
		tagsJSON           string
		tagsLock           int
		authorsJSON        string
		authorsLock        int
		altTitlesJSON      string
		altTitlesLock      int
		linksJSON          string
		linksLock          int
		sharingJSON        string
		sharingLock        int
		isNSFW             int
		nsfwLock           int
		updatedAt          string
	)
	err := scanner.Scan(
		&m.SeriesID, &bgmID, &m.ScrapeStatus, &m.ScrapeConfidence, &syncedAt,
		&m.CoverSource, &m.BgmCoverURL, &m.BgmCoverPath, &bgmScore, &bgmRank,
		&m.Status, &statusLock, &m.Title, &titleLock, &m.TitleSort, &titleSortLock,
		&m.Summary, &summaryLock, &m.ReadingDirection, &readingDirLock,
		&m.Publisher, &publisherLock, &ageRating, &ageRatingLock,
		&m.Language, &languageLock, &totalBooks, &totalBookCountLock,
		&m.ReleaseDate, &releaseDateLock,
		&genresJSON, &genresLock, &tagsJSON, &tagsLock,
		&authorsJSON, &authorsLock, &altTitlesJSON, &altTitlesLock,
		&linksJSON, &linksLock, &sharingJSON, &sharingLock, &isNSFW, &nsfwLock, &updatedAt,
	)
	if err != nil {
		return SeriesMetadata{}, err
	}
	if bgmID.Valid && bgmID.Int64 > 0 {
		val := bgmID.Int64
		m.BgmSubjectID = &val
	}
	if syncedAt.Valid && syncedAt.String != "" {
		val := syncedAt.String
		m.ScrapeSyncedAt = &val
	}
	if bgmRank.Valid && bgmRank.Int64 > 0 {
		rank := int(bgmRank.Int64)
		m.BgmRank = &rank
	}
	if bgmScore.Valid {
		val := bgmScore.Float64
		m.BgmScore = &val
	}
	if ageRating.Valid {
		val := int(ageRating.Int64)
		m.AgeRating = &val
	}
	if totalBooks.Valid {
		val := int(totalBooks.Int64)
		m.TotalBookCount = &val
	}
	m.StatusLock = statusLock != 0
	m.TitleLock = titleLock != 0
	m.TitleSortLock = titleSortLock != 0
	m.SummaryLock = summaryLock != 0
	m.ReadingDirLock = readingDirLock != 0
	m.PublisherLock = publisherLock != 0
	m.AgeRatingLock = ageRatingLock != 0
	m.LanguageLock = languageLock != 0
	m.TotalBookCountLock = totalBookCountLock != 0
	m.ReleaseDateLock = releaseDateLock != 0
	m.GenresLock = genresLock != 0
	m.TagsLock = tagsLock != 0
	m.AuthorsLock = authorsLock != 0
	m.AlternateTitlesLock = altTitlesLock != 0
	m.LinksLock = linksLock != 0
	m.SharingLabelsLock = sharingLock != 0
	m.IsNSFW = isNSFW != 0
	m.NSFWLock = nsfwLock != 0

	_ = json.Unmarshal([]byte(genresJSON), &m.Genres)
	if m.Genres == nil {
		m.Genres = []string{}
	}
	_ = json.Unmarshal([]byte(tagsJSON), &m.Tags)
	if m.Tags == nil {
		m.Tags = []string{}
	}
	_ = json.Unmarshal([]byte(authorsJSON), &m.Authors)
	if m.Authors == nil {
		m.Authors = []Author{}
	}
	_ = json.Unmarshal([]byte(altTitlesJSON), &m.AlternateTitles)
	if m.AlternateTitles == nil {
		m.AlternateTitles = []AlternateTitle{}
	}
	_ = json.Unmarshal([]byte(linksJSON), &m.Links)
	if m.Links == nil {
		m.Links = []WebLink{}
	}
	_ = json.Unmarshal([]byte(sharingJSON), &m.SharingLabels)
	if m.SharingLabels == nil {
		m.SharingLabels = []string{}
	}
	if m.CoverSource == "" {
		m.CoverSource = CoverSourceAuto
	}
	if m.Status == "" {
		m.Status = "ONGOING"
	}
	if m.ReadingDirection == "" {
		m.ReadingDirection = "LEFT_TO_RIGHT"
	}
	m.UpdatedAt = parseTime(updatedAt)
	return m, nil
}

func (s *Store) SeriesMetadataByID(ctx context.Context, series Series) (SeriesMetadata, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+seriesMetadataColumns+` FROM series_metadata WHERE series_id=?`, series.ID)
	item, err := scanSeriesMetadataRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultSeriesMetadata(series), nil
	}
	if err != nil {
		return SeriesMetadata{}, err
	}
	if strings.TrimSpace(item.Title) == "" {
		item.Title = series.Name
	}
	if strings.TrimSpace(item.TitleSort) == "" {
		item.TitleSort = item.Title
	}
	return item, nil
}

func (s *Store) SeriesMetadatas(
	ctx context.Context,
	seriesList []Series,
) (map[string]SeriesMetadata, error) {
	out := make(map[string]SeriesMetadata, len(seriesList))
	if len(seriesList) == 0 {
		return out, nil
	}
	byID := make(map[string]Series, len(seriesList))
	ids := make([]string, 0, len(seriesList))
	for _, item := range seriesList {
		if _, exists := byID[item.ID]; exists {
			continue
		}
		byID[item.ID] = item
		out[item.ID] = defaultSeriesMetadata(item)
		ids = append(ids, item.ID)
	}
	for _, chunk := range chunkStrings(ids, sqlInChunkSize) {
		args := make([]any, 0, len(chunk))
		for _, id := range chunk {
			args = append(args, id)
		}
		query := `SELECT ` + seriesMetadataColumns +
			` FROM series_metadata WHERE series_id IN ` + Placeholders(len(args))
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			meta, err := scanSeriesMetadataRow(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			series := byID[meta.SeriesID]
			if strings.TrimSpace(meta.Title) == "" {
				meta.Title = series.Name
			}
			if strings.TrimSpace(meta.TitleSort) == "" {
				meta.TitleSort = meta.Title
			}
			out[meta.SeriesID] = meta
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

func (s *Store) ApplyScrapedMetadata(
	ctx context.Context,
	series Series,
	incoming ScrapedMetadata,
) (SeriesMetadata, error) {
	current, err := s.SeriesMetadataByID(ctx, series)
	if err != nil {
		return SeriesMetadata{}, err
	}
	prevSubjectID := int64(0)
	if current.BgmSubjectID != nil {
		prevSubjectID = *current.BgmSubjectID
	}
	subj := incoming.SubjectID
	current.BgmSubjectID = &subj
	current.ScrapeStatus = ScrapeStatusMatched
	current.ScrapeConfidence = incoming.Confidence
	now := nowText()
	current.ScrapeSyncedAt = &now
	if incoming.CoverURL != "" {
		if prevSubjectID != incoming.SubjectID {
			current.BgmCoverPath = ""
		}
		current.BgmCoverURL = incoming.CoverURL
	}
	current.BgmScore = incoming.Score
	current.BgmRank = nil
	if incoming.Rank > 0 {
		rank := incoming.Rank
		current.BgmRank = &rank
	}

	if !current.StatusLock && incoming.Status != "" {
		current.Status = normalizeSeriesStatus(incoming.Status)
	}
	if !current.TitleLock && strings.TrimSpace(incoming.Title) != "" {
		current.Title = strings.TrimSpace(incoming.Title)
	}
	if !current.TitleSortLock {
		if strings.TrimSpace(incoming.TitleSort) != "" {
			current.TitleSort = strings.TrimSpace(incoming.TitleSort)
		} else if strings.TrimSpace(current.Title) != "" {
			current.TitleSort = current.Title
		}
	}
	if !current.SummaryLock {
		current.Summary = strings.TrimSpace(incoming.Summary)
	}
	if !current.PublisherLock && strings.TrimSpace(incoming.Publisher) != "" {
		current.Publisher = strings.TrimSpace(incoming.Publisher)
	}
	if !current.LanguageLock && strings.TrimSpace(incoming.Language) != "" {
		current.Language = strings.TrimSpace(incoming.Language)
	}
	if !current.TotalBookCountLock && incoming.TotalBookCount != nil && *incoming.TotalBookCount > 0 {
		current.TotalBookCount = incoming.TotalBookCount
	}
	if !current.ReleaseDateLock && strings.TrimSpace(incoming.ReleaseDate) != "" {
		current.ReleaseDate = strings.TrimSpace(incoming.ReleaseDate)
	}
	if !current.GenresLock && incoming.Genres != nil {
		current.Genres = cleanStringSlice(incoming.Genres)
	}
	if !current.TagsLock && incoming.Tags != nil {
		current.Tags = cleanStringSlice(incoming.Tags)
	}
	if !current.AuthorsLock && incoming.Authors != nil {
		current.Authors = cleanAuthors(incoming.Authors)
	}
	if !current.AlternateTitlesLock && incoming.AlternateTitles != nil {
		current.AlternateTitles = cleanAlternateTitles(incoming.AlternateTitles)
	}
	if !current.LinksLock && incoming.Links != nil {
		current.Links = cleanWebLinks(incoming.Links)
	}
	if !current.NSFWLock {
		current.IsNSFW = incoming.IsNSFW
	}
	if err := s.saveSeriesMetadata(ctx, current, now); err != nil {
		return SeriesMetadata{}, err
	}
	return s.SeriesMetadataByID(ctx, series)
}

func (s *Store) MarkSeriesScrapeStatus(
	ctx context.Context,
	series Series,
	status string,
	confidence float64,
) error {
	return s.MarkSeriesScrapeStatusWithNSFW(ctx, series, status, confidence, false)
}

func (s *Store) MarkSeriesScrapeStatusWithNSFW(
	ctx context.Context,
	series Series,
	status string,
	confidence float64,
	inferredNSFW bool,
) error {
	current, err := s.SeriesMetadataByID(ctx, series)
	if err != nil {
		return err
	}
	now := nowText()
	current.ScrapeStatus = status
	current.ScrapeConfidence = confidence
	current.ScrapeSyncedAt = &now
	if !current.NSFWLock && inferredNSFW {
		current.IsNSFW = true
	}
	return s.saveSeriesMetadata(ctx, current, now)
}

func (s *Store) ClearSeriesBangumiBinding(ctx context.Context, series Series) (SeriesMetadata, error) {
	current, err := s.SeriesMetadataByID(ctx, series)
	if err != nil {
		return SeriesMetadata{}, err
	}
	current.BgmSubjectID = nil
	current.ScrapeStatus = ScrapeStatusNone
	current.ScrapeConfidence = 0
	current.BgmCoverURL = ""
	current.BgmCoverPath = ""
	current.BgmScore = nil
	current.BgmRank = nil
	if !current.NSFWLock {
		current.IsNSFW = false
	}
	if !current.TitleLock {
		current.Title = series.Name
	}
	if !current.TitleSortLock {
		current.TitleSort = series.Name
	}
	if err := s.saveSeriesMetadata(ctx, current, nowText()); err != nil {
		return SeriesMetadata{}, err
	}
	return s.SeriesMetadataByID(ctx, series)
}

func (s *Store) PatchSeriesMetadata(
	ctx context.Context,
	series Series,
	patch SeriesMetadataPatch,
) (SeriesMetadata, error) {
	current, err := s.SeriesMetadataByID(ctx, series)
	if err != nil {
		return SeriesMetadata{}, err
	}
	if patch.Status != nil {
		current.Status = normalizeSeriesStatus(*patch.Status)
	}
	if patch.StatusLock != nil {
		current.StatusLock = *patch.StatusLock
	}
	if patch.Title != nil {
		title := strings.TrimSpace(*patch.Title)
		if title == "" {
			title = series.Name
		}
		current.Title = title
	}
	if patch.TitleLock != nil {
		current.TitleLock = *patch.TitleLock
	}
	if patch.TitleSort != nil {
		sortTitle := strings.TrimSpace(*patch.TitleSort)
		if sortTitle == "" {
			sortTitle = current.Title
		}
		current.TitleSort = sortTitle
	}
	if patch.TitleSortLock != nil {
		current.TitleSortLock = *patch.TitleSortLock
	}
	if patch.Summary != nil {
		current.Summary = strings.TrimSpace(*patch.Summary)
	}
	if patch.SummaryLock != nil {
		current.SummaryLock = *patch.SummaryLock
	}
	if patch.ReadingDirection != nil {
		current.ReadingDirection = normalizeReadingDirection(*patch.ReadingDirection)
	}
	if patch.ReadingDirLock != nil {
		current.ReadingDirLock = *patch.ReadingDirLock
	}
	if patch.Publisher != nil {
		current.Publisher = strings.TrimSpace(*patch.Publisher)
	}
	if patch.PublisherLock != nil {
		current.PublisherLock = *patch.PublisherLock
	}
	if patch.ClearAgeRating {
		current.AgeRating = nil
	} else if patch.AgeRating != nil {
		if *patch.AgeRating < 0 {
			current.AgeRating = nil
		} else {
			v := *patch.AgeRating
			current.AgeRating = &v
		}
	}
	if patch.AgeRatingLock != nil {
		current.AgeRatingLock = *patch.AgeRatingLock
	}
	if patch.Language != nil {
		current.Language = strings.TrimSpace(*patch.Language)
	}
	if patch.LanguageLock != nil {
		current.LanguageLock = *patch.LanguageLock
	}
	if patch.ClearTotalBookCount {
		current.TotalBookCount = nil
	} else if patch.TotalBookCount != nil {
		if *patch.TotalBookCount <= 0 {
			current.TotalBookCount = nil
		} else {
			v := *patch.TotalBookCount
			current.TotalBookCount = &v
		}
	}
	if patch.TotalBookCountLock != nil {
		current.TotalBookCountLock = *patch.TotalBookCountLock
	}
	if patch.ReleaseDate != nil {
		current.ReleaseDate = strings.TrimSpace(*patch.ReleaseDate)
	}
	if patch.ReleaseDateLock != nil {
		current.ReleaseDateLock = *patch.ReleaseDateLock
	}
	if patch.Genres != nil {
		current.Genres = cleanStringSlice(*patch.Genres)
	}
	if patch.GenresLock != nil {
		current.GenresLock = *patch.GenresLock
	}
	if patch.Tags != nil {
		current.Tags = cleanStringSlice(*patch.Tags)
	}
	if patch.TagsLock != nil {
		current.TagsLock = *patch.TagsLock
	}
	if patch.Authors != nil {
		current.Authors = cleanAuthors(*patch.Authors)
	}
	if patch.AuthorsLock != nil {
		current.AuthorsLock = *patch.AuthorsLock
	}
	if patch.AlternateTitles != nil {
		current.AlternateTitles = cleanAlternateTitles(*patch.AlternateTitles)
	}
	if patch.AlternateTitlesLock != nil {
		current.AlternateTitlesLock = *patch.AlternateTitlesLock
	}
	if patch.Links != nil {
		current.Links = cleanWebLinks(*patch.Links)
	}
	if patch.LinksLock != nil {
		current.LinksLock = *patch.LinksLock
	}
	if patch.SharingLabels != nil {
		current.SharingLabels = cleanStringSlice(*patch.SharingLabels)
	}
	if patch.SharingLabelsLock != nil {
		current.SharingLabelsLock = *patch.SharingLabelsLock
	}
	if patch.IsNSFW != nil {
		current.IsNSFW = *patch.IsNSFW
	}
	if patch.NSFWLock != nil {
		current.NSFWLock = *patch.NSFWLock
	}
	if patch.CoverSource != nil {
		switch strings.ToLower(strings.TrimSpace(*patch.CoverSource)) {
		case CoverSourceBangumi:
			current.CoverSource = CoverSourceBangumi
		case CoverSourceBook:
			current.CoverSource = CoverSourceBook
		default:
			current.CoverSource = CoverSourceAuto
		}
	}
	if err := s.saveSeriesMetadata(ctx, current, nowText()); err != nil {
		return SeriesMetadata{}, err
	}
	return s.SeriesMetadataByID(ctx, series)
}

func (s *Store) SetSeriesBgmCoverPath(ctx context.Context, seriesID, fileName string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE series_metadata SET bgm_cover_path=?,updated_at=? WHERE series_id=?`,
		strings.TrimSpace(fileName), nowText(), seriesID)
	return err
}

func (s *Store) saveSeriesMetadata(ctx context.Context, m SeriesMetadata, now string) error {
	genresJSON, _ := json.Marshal(cleanStringSlice(m.Genres))
	tagsJSON, _ := json.Marshal(cleanStringSlice(m.Tags))
	authorsJSON, _ := json.Marshal(cleanAuthors(m.Authors))
	altTitlesJSON, _ := json.Marshal(cleanAlternateTitles(m.AlternateTitles))
	linksJSON, _ := json.Marshal(cleanWebLinks(m.Links))
	sharingJSON, _ := json.Marshal(cleanStringSlice(m.SharingLabels))

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
INSERT INTO series_metadata(
 `+seriesMetadataColumns+`
) VALUES(
 ?,?,?,?,?,
 ?,?,?,?,?,
 ?,?,?,?,?,?,
 ?,?,?,?,
 ?,?,?,?,
 ?,?,?,?,
 ?,?,
 ?,?,?,?,
 ?,?,?,?,
 ?,?,?,?,?,?,?
)
ON CONFLICT(series_id) DO UPDATE SET
 bgm_subject_id=excluded.bgm_subject_id,
 scrape_status=excluded.scrape_status,
 scrape_confidence=excluded.scrape_confidence,
 scrape_synced_at=excluded.scrape_synced_at,
 cover_source=excluded.cover_source,
 bgm_cover_url=excluded.bgm_cover_url,
 bgm_cover_path=excluded.bgm_cover_path,
 bgm_score=excluded.bgm_score,
 bgm_rank=excluded.bgm_rank,
 status=excluded.status,status_lock=excluded.status_lock,
 title=excluded.title,title_lock=excluded.title_lock,
 title_sort=excluded.title_sort,title_sort_lock=excluded.title_sort_lock,
 summary=excluded.summary,summary_lock=excluded.summary_lock,
 reading_direction=excluded.reading_direction,reading_direction_lock=excluded.reading_direction_lock,
 publisher=excluded.publisher,publisher_lock=excluded.publisher_lock,
 age_rating=excluded.age_rating,age_rating_lock=excluded.age_rating_lock,
 language=excluded.language,language_lock=excluded.language_lock,
 total_book_count=excluded.total_book_count,total_book_count_lock=excluded.total_book_count_lock,
 release_date=excluded.release_date,release_date_lock=excluded.release_date_lock,
 genres_json=excluded.genres_json,genres_lock=excluded.genres_lock,
 tags_json=excluded.tags_json,tags_lock=excluded.tags_lock,
 authors_json=excluded.authors_json,authors_lock=excluded.authors_lock,
 alternate_titles_json=excluded.alternate_titles_json,alternate_titles_lock=excluded.alternate_titles_lock,
 links_json=excluded.links_json,links_lock=excluded.links_lock,
 sharing_labels_json=excluded.sharing_labels_json,sharing_labels_lock=excluded.sharing_labels_lock,
 is_nsfw=excluded.is_nsfw,nsfw_lock=excluded.nsfw_lock,
 updated_at=excluded.updated_at`,
		m.SeriesID, nullableInt64Ptr(m.BgmSubjectID), m.ScrapeStatus, m.ScrapeConfidence, nullableStringPtr(m.ScrapeSyncedAt),
		m.CoverSource, m.BgmCoverURL, m.BgmCoverPath, nullableFloat64Ptr(m.BgmScore), nullableIntPtr(m.BgmRank),
		m.Status, boolInt(m.StatusLock), m.Title, boolInt(m.TitleLock), m.TitleSort, boolInt(m.TitleSortLock),
		m.Summary, boolInt(m.SummaryLock), m.ReadingDirection, boolInt(m.ReadingDirLock),
		m.Publisher, boolInt(m.PublisherLock), nullableIntPtr(m.AgeRating), boolInt(m.AgeRatingLock),
		m.Language, boolInt(m.LanguageLock), nullableIntPtr(m.TotalBookCount), boolInt(m.TotalBookCountLock),
		m.ReleaseDate, boolInt(m.ReleaseDateLock),
		string(genresJSON), boolInt(m.GenresLock), string(tagsJSON), boolInt(m.TagsLock),
		string(authorsJSON), boolInt(m.AuthorsLock), string(altTitlesJSON), boolInt(m.AlternateTitlesLock),
		string(linksJSON), boolInt(m.LinksLock), string(sharingJSON), boolInt(m.SharingLabelsLock),
		boolInt(m.IsNSFW), boolInt(m.NSFWLock), now,
	)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM series_genres WHERE series_id=?`, m.SeriesID); err != nil {
		return err
	}
	for _, genre := range cleanStringSlice(m.Genres) {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO series_genres(series_id,genre) VALUES(?,?)`, m.SeriesID, genre); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM series_tags WHERE series_id=?`, m.SeriesID); err != nil {
		return err
	}
	for _, tag := range cleanStringSlice(m.Tags) {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO series_tags(series_id,tag) VALUES(?,?)`, m.SeriesID, tag); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM series_authors WHERE series_id=?`, m.SeriesID); err != nil {
		return err
	}
	for _, author := range cleanAuthors(m.Authors) {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO series_authors(series_id,name,role) VALUES(?,?,?)`,
			m.SeriesID, author.Name, author.Role); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) Genres(ctx context.Context, libraryIDs []string) ([]string, error) {
	if len(libraryIDs) == 0 {
		return shelfCached(ctx, s, "taxonomy:genre", false, func(ctx context.Context) ([]string, error) {
			return s.distinctSeriesValues(ctx, "series_genres", "genre", nil)
		})
	}
	return s.distinctSeriesValues(ctx, "series_genres", "genre", libraryIDs)
}

func (s *Store) Tags(ctx context.Context, libraryIDs []string) ([]string, error) {
	if len(libraryIDs) == 0 {
		return shelfCached(ctx, s, "taxonomy:tag", false, func(ctx context.Context) ([]string, error) {
			return s.distinctSeriesValues(ctx, "series_tags", "tag", nil)
		})
	}
	return s.distinctSeriesValues(ctx, "series_tags", "tag", libraryIDs)
}

func (s *Store) Publishers(ctx context.Context, libraryIDs []string) ([]string, error) {
	if len(libraryIDs) == 0 {
		return shelfCached(ctx, s, "taxonomy:publisher", false, func(ctx context.Context) ([]string, error) {
			return s.queryPublishers(ctx, nil)
		})
	}
	return s.queryPublishers(ctx, libraryIDs)
}

type AuthorQuery struct {
	LibraryIDs    []string
	CollectionIDs []string
	ReadListIDs   []string
	Search        string
	Role          string
	Page          int
	Size          int
	Unpaged       bool
}

type scrapeSummaryResult struct {
	matched, unmatched, total int64
}

func (s *Store) queryPublishers(ctx context.Context, libraryIDs []string) ([]string, error) {
	enabledLibs, err := s.enabledLibraryIDs(ctx)
	if err != nil {
		return nil, err
	}
	libs, empty := intersectLibraryIDs(libraryIDs, enabledLibs)
	if empty {
		return []string{}, nil
	}
	query := `
SELECT DISTINCT sm.publisher
FROM series_metadata sm
JOIN series s ON s.id=sm.series_id
WHERE sm.publisher<>'' AND s.library_id IN ` + Placeholders(len(libs))
	if !s.ShowNSFW(ctx) {
		query += ` AND sm.is_nsfw=0`
	}
	args := make([]any, 0, len(libs))
	for _, id := range libs {
		args = append(args, id)
	}
	query += ` ORDER BY sm.publisher COLLATE NOCASE ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var val string
		if err := rows.Scan(&val); err != nil {
			return nil, err
		}
		out = append(out, val)
	}
	return out, rows.Err()
}

func (s *Store) Authors(
	ctx context.Context,
	libraryIDs []string,
	search, role string,
) ([]Author, error) {
	search = strings.TrimSpace(search)
	role = strings.TrimSpace(role)
	if len(libraryIDs) == 0 && search == "" && role == "" {
		return shelfCached(ctx, s, "taxonomy:authors", false, func(ctx context.Context) ([]Author, error) {
			return s.queryAuthorsTop(ctx, nil, "", "", 500)
		})
	}
	return s.queryAuthorsTop(ctx, libraryIDs, search, role, 500)
}

func (s *Store) queryAuthorsTop(
	ctx context.Context,
	libraryIDs []string,
	search, role string,
	limit int,
) ([]Author, error) {
	enabledLibs, err := s.enabledLibraryIDs(ctx)
	if err != nil {
		return nil, err
	}
	libs, empty := intersectLibraryIDs(libraryIDs, enabledLibs)
	if empty {
		return []Author{}, nil
	}
	clauses := []string{
		`sa.name<>''`,
		`s.library_id IN ` + Placeholders(len(libs)),
	}
	args := make([]any, 0, len(libs)+3)
	for _, id := range libs {
		args = append(args, id)
	}
	if !s.ShowNSFW(ctx) {
		clauses = append(clauses, `s.id NOT IN (SELECT series_id FROM series_metadata WHERE is_nsfw=1)`)
	}
	if search != "" {
		clauses = append(clauses, `sa.name LIKE ? ESCAPE '\' COLLATE NOCASE`)
		args = append(args, "%"+escapeLike(search)+"%")
	}
	if role != "" {
		clauses = append(clauses, `sa.role=? COLLATE NOCASE`)
		args = append(args, strings.ToLower(role))
	}
	// Drive from series_authors in idx_series_authors_name order so LIMIT 500 stops early!
	query := `
SELECT DISTINCT sa.name, sa.role
FROM series_authors sa
CROSS JOIN series s ON s.id=sa.series_id
WHERE ` + strings.Join(clauses, " AND ") + `
ORDER BY sa.name COLLATE NOCASE ASC, sa.role ASC
LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Author, 0)
	for rows.Next() {
		var item Author
		if err := rows.Scan(&item.Name, &item.Role); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) AuthorsPage(ctx context.Context, q AuthorQuery) ([]Author, int64, error) {
	if q.Page < 0 {
		q.Page = 0
	}
	if !q.Unpaged && (q.Size <= 0 || q.Size > 500) {
		q.Size = 20
	}
	enabledLibs, err := s.enabledLibraryIDs(ctx)
	if err != nil {
		return nil, 0, err
	}
	libs, empty := intersectLibraryIDs(q.LibraryIDs, enabledLibs)
	if empty {
		return []Author{}, 0, nil
	}

	clauses := []string{
		`sa.name<>''`,
		`s.library_id IN ` + Placeholders(len(libs)),
	}
	args := make([]any, 0, len(libs)+4)
	for _, id := range libs {
		args = append(args, id)
	}
	if !s.ShowNSFW(ctx) {
		clauses = append(clauses, `s.id NOT IN (SELECT series_id FROM series_metadata WHERE is_nsfw=1)`)
	}
	if len(q.CollectionIDs) > 0 {
		cids := cleanStringSliceCaseSensitive(q.CollectionIDs)
		clauses = append(clauses, `s.id IN (SELECT cs.series_id FROM collection_series cs WHERE cs.collection_id IN `+Placeholders(len(cids))+`)`)
		for _, id := range cids {
			args = append(args, id)
		}
	}
	if len(q.ReadListIDs) > 0 {
		rids := cleanStringSliceCaseSensitive(q.ReadListIDs)
		clauses = append(clauses, `s.id IN (SELECT b.series_id FROM readlist_books rb JOIN books b ON b.id=rb.book_id WHERE rb.readlist_id IN `+Placeholders(len(rids))+`)`)
		for _, id := range rids {
			args = append(args, id)
		}
	}
	if search := strings.TrimSpace(q.Search); search != "" {
		clauses = append(clauses, `sa.name LIKE ? ESCAPE '\' COLLATE NOCASE`)
		args = append(args, "%"+escapeLike(search)+"%")
	}
	if role := strings.TrimSpace(q.Role); role != "" {
		clauses = append(clauses, `sa.role=? COLLATE NOCASE`)
		args = append(args, strings.ToLower(role))
	}

	base := ` FROM series s JOIN series_authors sa ON sa.series_id=s.id WHERE ` + strings.Join(clauses, " AND ")
	var total int64
	if !q.Unpaged {
		if err := s.db.QueryRowContext(ctx,
			`SELECT count(*) FROM (SELECT DISTINCT sa.name, sa.role`+base+`)`, args...).Scan(&total); err != nil {
			return nil, 0, err
		}
		if total == 0 {
			return []Author{}, 0, nil
		}
	}

	query := `SELECT DISTINCT sa.name, sa.role` + base + ` ORDER BY sa.name COLLATE NOCASE ASC, sa.role ASC`
	queryArgs := append([]any(nil), args...)
	if !q.Unpaged {
		query += ` LIMIT ? OFFSET ?`
		queryArgs = append(queryArgs, q.Size, q.Page*q.Size)
	}
	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]Author, 0)
	for rows.Next() {
		var item Author
		if err := rows.Scan(&item.Name, &item.Role); err != nil {
			return nil, 0, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if q.Unpaged {
		total = int64(len(out))
	}
	return out, total, nil
}

func (s *Store) ScrapeSummary(ctx context.Context) (matched, unmatched, total int64, err error) {
	r, err := shelfCached(ctx, s, "scrape-summary", false, s.queryScrapeSummary)
	return r.matched, r.unmatched, r.total, err
}

func (s *Store) queryScrapeSummary(ctx context.Context) (scrapeSummaryResult, error) {
	enabledLibs, err := s.enabledLibraryIDs(ctx)
	if err != nil {
		return scrapeSummaryResult{}, err
	}
	if len(enabledLibs) == 0 {
		return scrapeSummaryResult{}, nil
	}
	libPH := Placeholders(len(enabledLibs))
	libArgs := make([]any, 0, len(enabledLibs))
	for _, id := range enabledLibs {
		libArgs = append(libArgs, id)
	}

	var out scrapeSummaryResult
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM series WHERE library_id IN `+libPH, libArgs...).Scan(&out.total); err != nil {
		return scrapeSummaryResult{}, err
	}
	showNSFW := s.ShowNSFW(ctx)
	if !showNSFW {
		var nsfwSeries int64
		if err := s.db.QueryRowContext(ctx, `
SELECT count(*)
FROM series_metadata sm
JOIN series s ON s.id=sm.series_id
WHERE sm.is_nsfw=1 AND s.library_id IN `+libPH, libArgs...).Scan(&nsfwSeries); err != nil {
			return scrapeSummaryResult{}, err
		}
		out.total = max(int64(0), out.total-nsfwSeries)
	}

	statusQuery := `
SELECT
 coalesce(sum(CASE WHEN sm.scrape_status='matched' THEN 1 ELSE 0 END), 0),
 coalesce(sum(CASE WHEN sm.scrape_status='unmatched' THEN 1 ELSE 0 END), 0)
FROM series s
CROSS JOIN series_metadata sm ON sm.series_id=s.id
WHERE s.library_id IN ` + libPH + ` AND sm.scrape_status IN ('matched','unmatched')`
	if !showNSFW {
		statusQuery += ` AND sm.is_nsfw=0`
	}
	if err := s.db.QueryRowContext(ctx, statusQuery, libArgs...).Scan(&out.matched, &out.unmatched); err != nil {
		return scrapeSummaryResult{}, err
	}
	return out, nil
}

func (s *Store) NSFWSeriesCount(ctx context.Context) (int64, error) {
	enabledLibs, err := s.enabledLibraryIDs(ctx)
	if err != nil {
		return 0, err
	}
	if len(enabledLibs) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(enabledLibs))
	for _, id := range enabledLibs {
		args = append(args, id)
	}
	var count int64
	err = s.db.QueryRowContext(ctx, `
SELECT count(*) FROM series_metadata sm
JOIN series s ON s.id=sm.series_id
WHERE sm.is_nsfw=1 AND s.library_id IN `+Placeholders(len(args)), args...).Scan(&count)
	return count, err
}

func (s *Store) distinctSeriesValues(
	ctx context.Context,
	table, column string,
	libraryIDs []string,
) ([]string, error) {
	enabledLibs, err := s.enabledLibraryIDs(ctx)
	if err != nil {
		return nil, err
	}
	libs, empty := intersectLibraryIDs(libraryIDs, enabledLibs)
	if empty {
		return []string{}, nil
	}
	query := `
SELECT DISTINCT t.` + column + `
FROM series s
JOIN ` + table + ` t ON t.series_id=s.id
WHERE s.library_id IN ` + Placeholders(len(libs)) + ` AND t.` + column + `<>''`
	if !s.ShowNSFW(ctx) {
		query += ` AND s.id NOT IN (SELECT series_id FROM series_metadata WHERE is_nsfw=1)`
	}
	args := make([]any, 0, len(libs))
	for _, id := range libs {
		args = append(args, id)
	}
	query += ` ORDER BY t.` + column + ` COLLATE NOCASE ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var val string
		if err := rows.Scan(&val); err != nil {
			return nil, err
		}
		out = append(out, val)
	}
	return out, rows.Err()
}

func normalizeSeriesStatus(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "ENDED":
		return "ENDED"
	case "ABANDONED":
		return "ABANDONED"
	case "HIATUS":
		return "HIATUS"
	default:
		return "ONGOING"
	}
}

func normalizeReadingDirection(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "RIGHT_TO_LEFT":
		return "RIGHT_TO_LEFT"
	case "VERTICAL":
		return "VERTICAL"
	case "WEBTOON":
		return "WEBTOON"
	default:
		return "LEFT_TO_RIGHT"
	}
}

func cleanStringSlice(items []string) []string {
	out := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, raw := range items {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		key := strings.ToLower(v)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
	}
	return out
}

func cleanAuthors(items []Author) []Author {
	out := make([]Author, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		role := strings.ToLower(strings.TrimSpace(item.Role))
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
		out = append(out, Author{Name: name, Role: role})
	}
	return out
}

func cleanAlternateTitles(items []AlternateTitle) []AlternateTitle {
	out := make([]AlternateTitle, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		label := strings.TrimSpace(item.Label)
		if title == "" {
			continue
		}
		if label == "" {
			label = "别名"
		}
		key := strings.ToLower(label) + "\x00" + strings.ToLower(title)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, AlternateTitle{Label: label, Title: title})
	}
	return out
}

func cleanWebLinks(items []WebLink) []WebLink {
	out := make([]WebLink, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		label := strings.TrimSpace(item.Label)
		url := strings.TrimSpace(item.URL)
		if url == "" {
			continue
		}
		if label == "" {
			label = "Link"
		}
		if seen[url] {
			continue
		}
		seen[url] = true
		out = append(out, WebLink{Label: label, URL: url})
	}
	return out
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullableInt64Ptr(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableIntPtr(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableFloat64Ptr(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableStringPtr(v *string) any {
	if v == nil || *v == "" {
		return nil
	}
	return *v
}
