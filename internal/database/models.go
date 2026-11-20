package database

import "time"

const (
	Provider115    = "115"
	ProviderLocal  = "local"
	ProviderWebDAV = "webdav"
	ProviderUnion  = "union"

	CoverSourceAuto    = "auto"
	CoverSourceBangumi = "bangumi"
	CoverSourceBook    = "book"

	ScrapeStatusNone      = "none"
	ScrapeStatusMatched   = "matched"
	ScrapeStatusUnmatched = "unmatched"
	ScrapeStatusSkipped   = "skipped"
)

type Account struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	UpdatedAt    string `json:"updatedAt"`
}

type AdminUser struct {
	Username  string `json:"username"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type AuthSession struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	Remember   bool   `json:"remember"`
	UserAgent  string `json:"userAgent"`
	CreatedAt  string `json:"createdAt"`
	LastSeenAt string `json:"lastSeenAt"`
	ExpiresAt  string `json:"expiresAt"`
	Current    bool   `json:"current,omitempty"`
	Refreshed  bool   `json:"-"`
}

type APIKey struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	KeyPrefix  string  `json:"keyPrefix"`
	CreatedAt  string  `json:"createdAt"`
	LastUsedAt *string `json:"lastUsedAt"`
}

type WebDAVAccount struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	BaseURL       string  `json:"baseUrl"`
	Username      string  `json:"username"`
	HasPassword   bool    `json:"hasPassword"`
	InsecureTLS   bool    `json:"insecureTls"`
	RatePerSecond float64 `json:"ratePerSecond"`
	CreatedAt     string  `json:"createdAt"`
	UpdatedAt     string  `json:"updatedAt"`
}

type WebDAVAccountSecret struct {
	WebDAVAccount
	Password string
}

type UnionLayer struct {
	UnionLibraryID    string `json:"unionLibraryId"`
	MemberLibraryID   string `json:"memberLibraryId"`
	MemberLibraryName string `json:"memberLibraryName"`
	ProviderType      string `json:"providerType"`
	WebDAVAccountName string `json:"webdavAccountName,omitempty"`
	Priority          int    `json:"priority"`
}

type UnionSeriesSource struct {
	UnionSeriesID     string `json:"unionSeriesId"`
	Priority          int    `json:"priority"`
	SourceSeriesID    string `json:"sourceSeriesId"`
	SourceLibraryID   string `json:"sourceLibraryId"`
	SourceLibraryName string `json:"sourceLibraryName"`
	ProviderType      string `json:"providerType"`
	WebDAVAccountName string `json:"webdavAccountName,omitempty"`
}

type UnionBookSource struct {
	UnionBookID       string `json:"unionBookId"`
	Priority          int    `json:"priority"`
	SourceBookID      string `json:"sourceBookId"`
	SourceLibraryID   string `json:"sourceLibraryId"`
	SourceLibraryName string `json:"sourceLibraryName"`
	ProviderType      string `json:"providerType"`
	WebDAVAccountName string `json:"webdavAccountName,omitempty"`
	SourceSize        int64  `json:"sourceSize"`
	Active            bool   `json:"active"`
}

type UnionRefreshStats struct {
	UnionLibraryID      string `json:"unionLibraryId"`
	MemberCount         int    `json:"memberCount"`
	SeriesCount         int    `json:"seriesCount"`
	BooksCount          int    `json:"booksCount"`
	InheritedMetadata   int    `json:"inheritedMetadata"`
	InheritedProgress   int    `json:"inheritedProgress"`
	MigratedCollections int    `json:"migratedCollections"`
	MigratedReadLists   int    `json:"migratedReadLists"`
	RemovedStaleSeries  int    `json:"removedStaleSeries"`
	RemovedStaleBooks   int    `json:"removedStaleBooks"`
}

type Library struct {
	ID                  string       `json:"id"`
	Name                string       `json:"name"`
	ProviderType        string       `json:"providerType"`
	RootCID             string       `json:"rootCid"`
	WebDAVAccountID     string       `json:"webdavAccountId,omitempty"`
	WebDAVAccountName   string       `json:"webdavAccountName,omitempty"`
	RootPath            string       `json:"rootPath,omitempty"`
	Enabled             bool         `json:"enabled"`
	OneShot             bool         `json:"oneShot"`
	UnionParentID       string       `json:"unionParentId,omitempty"`
	UnionParentName     string       `json:"unionParentName,omitempty"`
	UnionPriority       int          `json:"unionPriority,omitempty"`
	UnionLayers         []UnionLayer `json:"unionLayers,omitempty"`
	CreatedAt           string       `json:"createdAt"`
	UpdatedAt           string       `json:"updatedAt"`
	LastScanStartedAt   *string      `json:"lastScanStartedAt"`
	LastScanCompletedAt *string      `json:"lastScanCompletedAt"`
	LastScanStatus      string       `json:"lastScanStatus"`
	LastScanError       string       `json:"lastScanError"`
	SeriesCount         int64        `json:"seriesCount"`
	BookCount           int64        `json:"bookCount"`
	ComicBytes          int64        `json:"comicBytes"`
}

func (l Library) EffectiveProvider() string {
	switch l.ProviderType {
	case ProviderLocal, ProviderWebDAV, ProviderUnion:
		return l.ProviderType
	default:
		return Provider115
	}
}

type Series struct {
	ID             string
	LibraryID      string
	CID            string
	Name           string
	RelativePath   string
	OneShot        bool
	FileModifiedAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	BooksCount     int
}

type Author struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

type WebLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type AlternateTitle struct {
	Label string `json:"label"`
	Title string `json:"title"`
}

type SeriesMetadata struct {
	SeriesID            string           `json:"seriesId"`
	BgmSubjectID        *int64           `json:"bgmSubjectId,omitempty"`
	ScrapeStatus        string           `json:"scrapeStatus"`
	ScrapeConfidence    float64          `json:"scrapeConfidence"`
	ScrapeSyncedAt      *string          `json:"scrapeSyncedAt,omitempty"`
	CoverSource         string           `json:"coverSource"`
	BgmCoverURL         string           `json:"bgmCoverUrl,omitempty"`
	BgmCoverPath        string           `json:"-"`
	BgmRank             *int             `json:"bgmRank,omitempty"`
	BgmScore            *float64         `json:"bgmScore,omitempty"`
	Status              string           `json:"status"`
	StatusLock          bool             `json:"statusLock"`
	Title               string           `json:"title"`
	TitleLock           bool             `json:"titleLock"`
	TitleSort           string           `json:"titleSort"`
	TitleSortLock       bool             `json:"titleSortLock"`
	Summary             string           `json:"summary"`
	SummaryLock         bool             `json:"summaryLock"`
	ReadingDirection    string           `json:"readingDirection"`
	ReadingDirLock      bool             `json:"readingDirectionLock"`
	Publisher           string           `json:"publisher"`
	PublisherLock       bool             `json:"publisherLock"`
	AgeRating           *int             `json:"ageRating"`
	AgeRatingLock       bool             `json:"ageRatingLock"`
	Language            string           `json:"language"`
	LanguageLock        bool             `json:"languageLock"`
	TotalBookCount      *int             `json:"totalBookCount"`
	TotalBookCountLock  bool             `json:"totalBookCountLock"`
	ReleaseDate         string           `json:"releaseDate"`
	ReleaseDateLock     bool             `json:"releaseDateLock"`
	Genres              []string         `json:"genres"`
	GenresLock          bool             `json:"genresLock"`
	Tags                []string         `json:"tags"`
	TagsLock            bool             `json:"tagsLock"`
	Authors             []Author         `json:"authors"`
	AuthorsLock         bool             `json:"authorsLock"`
	AlternateTitles     []AlternateTitle `json:"alternateTitles"`
	AlternateTitlesLock bool             `json:"alternateTitlesLock"`
	Links               []WebLink        `json:"links"`
	LinksLock           bool             `json:"linksLock"`
	SharingLabels       []string         `json:"sharingLabels"`
	SharingLabelsLock   bool             `json:"sharingLabelsLock"`
	IsNSFW              bool             `json:"isNsfw"`
	NSFWLock            bool             `json:"nsfwLock"`
	UpdatedAt           time.Time        `json:"updatedAt"`
}

type BangumiScrapeRun struct {
	ID              string   `json:"id"`
	LibraryID       *string  `json:"libraryId"`
	LibraryName     string   `json:"libraryName"`
	Mode            string   `json:"mode"`
	Force           bool     `json:"force"`
	Status          string   `json:"status"`
	TotalSeries     int      `json:"totalSeries"`
	ProcessedSeries int      `json:"processedSeries"`
	MatchedCount    int      `json:"matchedCount"`
	UnmatchedCount  int      `json:"unmatchedCount"`
	SkippedCount    int      `json:"skippedCount"`
	FailedCount     int      `json:"failedCount"`
	CurrentSeries   string   `json:"currentSeries"`
	Errors          []string `json:"errors"`
	CancelRequested bool     `json:"cancelRequested"`
	StartedAt       *string  `json:"startedAt"`
	CompletedAt     *string  `json:"completedAt"`
	CreatedAt       string   `json:"createdAt"`
}

type Book struct {
	ID             string
	SeriesID       string
	LibraryID      string
	FileID         string
	ParentCID      string
	Name           string
	Size           int64
	PickCode       string
	SHA1           string
	FileCreatedAt  *time.Time
	FileModifiedAt *time.Time
	NumberSort     float64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	PageCount      int
}

type SeriesReadProgress struct {
	BooksCount                   int     `json:"booksCount"`
	BooksReadCount               int     `json:"booksReadCount"`
	BooksUnreadCount             int     `json:"booksUnreadCount"`
	BooksInProgressCount         int     `json:"booksInProgressCount"`
	LastReadContinuousNumberSort float64 `json:"lastReadContinuousNumberSort"`
	MaxNumberSort                float64 `json:"maxNumberSort"`
}

type BookReadProgress struct {
	BookID      string
	SeriesID    string
	Completed   bool
	Page        *int
	ReadDate    *time.Time
	CompletedAt *time.Time
	UpdatedAt   time.Time
}

type Collection struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Ordered   bool      `json:"ordered"`
	SeriesIDs []string  `json:"seriesIds"`
	Filtered  bool      `json:"filtered"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type ReadList struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Summary   string    `json:"summary"`
	Ordered   bool      `json:"ordered"`
	BookIDs   []string  `json:"bookIds"`
	Filtered  bool      `json:"filtered"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type BookPageProgress struct {
	BookID         string
	SeriesID       string
	LastLoadedPage int
	MaxLoadedPage  int
	PageCount      int
	UpdatedAt      time.Time
}

type BookPageProgressView struct {
	BookPageProgress
	BookName   string
	NumberSort float64
}

type ArchiveIndexStats struct {
	BookID        string
	Version       string
	PageCount     int
	Duration      time.Duration
	CompletedAt   time.Time
	HasDuration   bool
	Current       bool
	BookName      string
	BookNumber    float64
	BookSeriesID  string
	BookLibraryID string
}

type DownloadStats struct {
	BookID      string
	SeriesID    string
	Bytes       int64
	Duration    time.Duration
	Samples     int64
	UpdatedAt   time.Time
	HasDownload bool
}

type SeriesPerformanceStats struct {
	BooksCount         int
	IndexedBooksCount  int
	IndexAverage       time.Duration
	IndexLatestAt      time.Time
	IndexDurationCount int
	CoverDuration      time.Duration
	CoverCompletedAt   time.Time
	HasCoverDuration   bool
	DownloadBytes      int64
	DownloadDuration   time.Duration
	DownloadSamples    int64
	DownloadLatestAt   time.Time
	HasDownload        bool
}

type GlobalPerformanceStats struct {
	IndexCount             int64 `json:"indexCount"`
	IndexAverageDurationNs int64 `json:"indexAverageDurationNs"`
	CoverCount             int64 `json:"coverCount"`
	CoverAverageDurationNs int64 `json:"coverAverageDurationNs"`
	DownloadBytes          int64 `json:"downloadBytes"`
	DownloadDurationNs     int64 `json:"downloadDurationNs"`
	DownloadSamples        int64 `json:"downloadSamples"`
}

type ScanRun struct {
	ID              string  `json:"id"`
	LibraryID       *string `json:"libraryId"`
	LibraryName     string  `json:"libraryName"`
	Status          string  `json:"status"`
	TriggerType     string  `json:"triggerType"`
	StartedAt       *string `json:"startedAt"`
	CompletedAt     *string `json:"completedAt"`
	DirectoriesSeen int64   `json:"directoriesSeen"`
	FilesSeen       int64   `json:"filesSeen"`
	SeriesSeen      int64   `json:"seriesSeen"`
	BooksSeen       int64   `json:"booksSeen"`
	CurrentPath     string  `json:"currentPath"`
	Error           string  `json:"error"`
	CancelRequested bool    `json:"cancelRequested"`
	CreatedAt       string  `json:"createdAt"`
}

type CacheStats struct {
	Type  string `json:"type"`
	Files int64  `json:"files"`
	Bytes int64  `json:"bytes"`
}

type LibrarySchedule struct {
	LibraryID            string   `json:"libraryId"`
	Enabled              bool     `json:"enabled"`
	Frequency            string   `json:"frequency"` // "daily" | "weekly"
	Weekday              int      `json:"weekday"`   // 0=Sunday .. 6=Saturday
	TimeOfDay            string   `json:"timeOfDay"` // "HH:MM" in server local time
	ScanMemberLibraryIDs []string `json:"scanMemberLibraryIds"`
	AutoBangumi          bool     `json:"autoBangumi"`
	AutoCoverLimit       int      `json:"autoCoverLimit"` // 0 = disabled (default), -1 = unlimited (local/SMB), >0 = cap
	LastRunAt            *string  `json:"lastRunAt"`
	LastSlotKey          string   `json:"lastSlotKey,omitempty"`
	UpdatedAt            string   `json:"updatedAt"`
}

type PipelineRun struct {
	ID              string   `json:"id"`
	LibraryID       string   `json:"libraryId"`
	LibraryName     string   `json:"libraryName"`
	TriggerType     string   `json:"triggerType"`
	Status          string   `json:"status"` // queued | running | success | partial | failed | canceled
	Stage           string   `json:"stage"`  // queued | scan | union_refresh | bangumi | cover | done
	ScanRunIDs      []string `json:"scanRunIds"`
	BangumiRunID    string   `json:"bangumiRunId,omitempty"`
	CoverRunID      string   `json:"coverRunId,omitempty"`
	Summary         string   `json:"summary"`
	Error           string   `json:"error"`
	CancelRequested bool     `json:"cancelRequested"`
	StartedAt       *string  `json:"startedAt"`
	CompletedAt     *string  `json:"completedAt"`
	CreatedAt       string   `json:"createdAt"`
}

type BangumiMatch struct {
	SeriesID          string `json:"seriesId"`
	Title             string `json:"title"`
	OriginalName      string `json:"originalName"`
	LibraryID         string `json:"libraryId"`
	LibraryName       string `json:"libraryName"`
	ProviderType      string `json:"providerType"`
	WebDAVAccountName string `json:"webdavAccountName,omitempty"`
	BooksCount        int    `json:"booksCount"`
	BooksReadCount    int    `json:"booksReadCount"`
	OneShot           bool   `json:"oneshot"`
	URL               string `json:"url"`
}
