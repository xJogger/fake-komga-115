package bangumi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/xJogger/fake-komga-115/internal/database"
)

var (
	ErrNSFWBlocked     = errors.New("NSFW Bangumi subjects are disabled")
	ErrSubjectNotFound = errors.New("Bangumi subject not found")
)

type LiveClient struct {
	baseURL string
	http    *http.Client
	limiter *rate.Limiter
}

func NewLiveClient() *LiveClient {
	return &LiveClient{
		baseURL: "https://api.bgm.tv",
		http:    &http.Client{Timeout: 25 * time.Second},
		limiter: rate.NewLimiter(rate.Limit(2.0), 2),
	}
}

type apiSubjectResponse struct {
	ID       int64      `json:"id"`
	Type     int        `json:"type"`
	Platform string     `json:"platform"`
	Name     string     `json:"name"`
	NameCN   string     `json:"name_cn"`
	Summary  string     `json:"summary"`
	NSFW     bool       `json:"nsfw"`
	Series   bool       `json:"series"`
	Date     string     `json:"date"`
	Volumes  int        `json:"volumes"`
	MetaTags []string   `json:"meta_tags"`
	Tags     []TagCount `json:"tags"`
	Images   struct {
		Common string `json:"common"`
		Medium string `json:"medium"`
		Large  string `json:"large"`
	} `json:"images"`
	Rating struct {
		Score float64 `json:"score"`
		Rank  int     `json:"rank"`
	} `json:"rating"`
	Score   float64          `json:"score"`
	Rank    int              `json:"rank"`
	Infobox []apiInfoboxItem `json:"infobox"`
}

type apiInfoboxItem struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

type apiPersonRelation struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Relation string `json:"relation"`
}

func (c *LiveClient) Search(
	ctx context.Context,
	keyword string,
	parsed ParsedQuery,
	targetIsOneShot bool,
	limit int,
) ([]SubjectCandidate, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 25 {
		limit = 15
	}
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	nsfwFalse := false
	reqBody, _ := json.Marshal(map[string]any{
		"keyword": keyword,
		"filter": map[string]any{
			"type": []int{1},
			"nsfw": nsfwFalse,
		},
	})
	url := fmt.Sprintf("%s/v0/search/subjects?limit=%d", strings.TrimRight(c.baseURL, "/"), limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", archiveUserAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Bangumi search API returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Data []apiSubjectResponse `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	if len(parsed.Candidates) == 0 {
		parsed = ParseSeriesName(keyword)
	}
	out := make([]SubjectCandidate, 0, len(payload.Data))
	for _, item := range payload.Data {
		if item.NSFW || item.Type != 1 {
			continue
		}
		cand := mapAPISubject(item, nil)
		cand.Confidence = ScoreCandidate(parsed, cand, targetIsOneShot)
		out = append(out, cand)
	}
	slices.SortFunc(out, func(a, b SubjectCandidate) int {
		if a.Confidence > b.Confidence {
			return -1
		}
		if a.Confidence < b.Confidence {
			return 1
		}
		return 0
	})
	return out, nil
}

func (c *LiveClient) SubjectByID(ctx context.Context, subjectID int64) (SubjectCandidate, error) {
	if subjectID <= 0 {
		return SubjectCandidate{}, ErrSubjectNotFound
	}
	if err := c.limiter.Wait(ctx); err != nil {
		return SubjectCandidate{}, err
	}
	url := fmt.Sprintf("%s/v0/subjects/%d", strings.TrimRight(c.baseURL, "/"), subjectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return SubjectCandidate{}, err
	}
	req.Header.Set("User-Agent", archiveUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return SubjectCandidate{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return SubjectCandidate{}, ErrSubjectNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return SubjectCandidate{}, fmt.Errorf("Bangumi subject API returned HTTP %d", resp.StatusCode)
	}
	var item apiSubjectResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&item); err != nil {
		return SubjectCandidate{}, err
	}
	if item.NSFW {
		return SubjectCandidate{}, ErrNSFWBlocked
	}
	persons, _ := c.fetchSubjectPersons(ctx, subjectID)
	cand := mapAPISubject(item, persons)
	cand.Confidence = 1.0
	return cand, nil
}

func (c *LiveClient) fetchSubjectPersons(ctx context.Context, subjectID int64) ([]apiPersonRelation, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/v0/subjects/%d/persons", strings.TrimRight(c.baseURL, "/"), subjectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", archiveUserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	var list []apiPersonRelation
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&list); err != nil {
		return nil, nil
	}
	return list, nil
}

func (c *LiveClient) FetchCoverBytes(ctx context.Context, coverURL string, subjectID int64) ([]byte, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	target := strings.TrimSpace(coverURL)
	if target == "" && subjectID > 0 {
		target = CoverEndpointURL(subjectID)
	}
	if target == "" {
		return nil, errors.New("empty Bangumi cover URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", archiveUserAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Bangumi cover download returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("Bangumi cover download returned empty body")
	}
	return data, nil
}

func mapAPISubject(item apiSubjectResponse, persons []apiPersonRelation) SubjectCandidate {
	var info WikiInfo
	info.Status = "ONGOING"
	for _, entry := range item.Infobox {
		var strVal string
		if err := json.Unmarshal(entry.Value, &strVal); err == nil {
			applyInfoboxSingle(&info, entry.Key, strVal)
			continue
		}
		var arrVal []struct {
			K string `json:"k"`
			V string `json:"v"`
		}
		if err := json.Unmarshal(entry.Value, &arrVal); err == nil {
			var block []string
			for _, pair := range arrVal {
				if pair.K != "" {
					block = append(block, pair.K+"|"+pair.V)
				} else if pair.V != "" {
					block = append(block, pair.V)
				}
			}
			applyInfoboxBlock(&info, entry.Key, block)
		}
	}
	for _, p := range persons {
		rel := strings.TrimSpace(p.Relation)
		switch {
		case rel == "作者":
			info.Authors = append(info.Authors, database.Author{Name: p.Name, Role: "writer"})
			info.Authors = append(info.Authors, database.Author{Name: p.Name, Role: "penciller"})
		case strings.Contains(rel, "原作") || strings.Contains(rel, "脚本"):
			info.Authors = append(info.Authors, database.Author{Name: p.Name, Role: "writer"})
		case strings.Contains(rel, "作画") || strings.Contains(rel, "插图") || strings.Contains(rel, "人物原案"):
			info.Authors = append(info.Authors, database.Author{Name: p.Name, Role: "penciller"})
		}
	}
	info.Authors = dedupeAuthors(info.Authors)

	nameCN := strings.TrimSpace(item.NameCN)
	if nameCN == "" {
		nameCN = info.TitleCN
	}
	platformCode := PlatformCodeFromString(item.Platform)
	if platformCode == 0 && item.Type == 1 {
		platformCode = PlatformComic
	}
	genres, tags := ClassifyTagsAndGenres(item.MetaTags, item.Tags)
	scoreVal := item.Rating.Score
	if scoreVal <= 0 {
		scoreVal = item.Score
	}
	var scorePtr *float64
	if scoreVal > 0 {
		v := scoreVal
		scorePtr = &v
	}
	rank := item.Rating.Rank
	if rank <= 0 {
		rank = item.Rank
	}
	totalVols := info.TotalVolumes
	if totalVols == nil && item.Volumes > 0 {
		v := item.Volumes
		totalVols = &v
	}
	relDate := strings.TrimSpace(item.Date)
	if relDate == "" {
		relDate = info.ReleaseDate
	} else {
		relDate = normalizeDateString(relDate)
	}
	coverURL := item.Images.Common
	if coverURL == "" {
		coverURL = item.Images.Medium
	}
	if coverURL == "" && item.ID > 0 {
		coverURL = CoverEndpointURL(item.ID)
	}
	return SubjectCandidate{
		ID:              item.ID,
		Platform:        platformCode,
		PlatformLabel:   PlatformName(platformCode),
		IsSeries:        item.Series,
		Name:            strings.TrimSpace(item.Name),
		NameCN:          nameCN,
		Summary:         strings.TrimSpace(item.Summary),
		Publisher:       info.Publisher,
		Status:          info.Status,
		ReleaseDate:     relDate,
		TotalVolumes:    totalVols,
		Score:           scorePtr,
		Rank:            rank,
		CoverURL:        coverURL,
		Genres:          genres,
		Tags:            tags,
		Authors:         info.Authors,
		AlternateTitles: info.Aliases,
	}
}

func ParseSubjectIDInput(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if idx := strings.Index(raw, "/subject/"); idx >= 0 {
		rest := raw[idx+len("/subject/"):]
		if cut := strings.IndexAny(rest, "/?#"); cut >= 0 {
			rest = rest[:cut]
		}
		raw = rest
	}
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
