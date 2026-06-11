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
