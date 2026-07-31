package database

import "time"

type Account struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	UpdatedAt    string `json:"updatedAt"`
}

type Library struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	RootCID             string  `json:"rootCid"`
	Enabled             bool    `json:"enabled"`
	OneShot             bool    `json:"oneShot"`
	CreatedAt           string  `json:"createdAt"`
	UpdatedAt           string  `json:"updatedAt"`
	LastScanStartedAt   *string `json:"lastScanStartedAt"`
	LastScanCompletedAt *string `json:"lastScanCompletedAt"`
	LastScanStatus      string  `json:"lastScanStatus"`
	LastScanError       string  `json:"lastScanError"`
	SeriesCount         int64   `json:"seriesCount"`
	BookCount           int64   `json:"bookCount"`
	ComicBytes          int64   `json:"comicBytes"`
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
