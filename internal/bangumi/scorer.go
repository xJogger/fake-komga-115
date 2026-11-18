package bangumi

import (
	"fmt"
	"math"
	"strings"

	"github.com/xJogger/fake-komga-115/internal/database"
)

const (
	PlatformComic        = 1001
	PlatformNovel        = 1002
	PlatformIllustration = 1003

	AutoMatchMinConfidence = 0.72
)

type SubjectCandidate struct {
	ID              int64                     `json:"id"`
	Platform        int                       `json:"platform"`
	PlatformLabel   string                    `json:"platformLabel"`
	IsSeries        bool                      `json:"isSeries"`
	Name            string                    `json:"name"`
	NameCN          string                    `json:"nameCn"`
	Summary         string                    `json:"summary"`
	Publisher       string                    `json:"publisher"`
	Status          string                    `json:"status"`
	ReleaseDate     string                    `json:"releaseDate"`
	TotalVolumes    *int                      `json:"totalVolumes,omitempty"`
	Score           *float64                  `json:"score,omitempty"`
	Rank            int                       `json:"rank,omitempty"`
	CoverURL        string                    `json:"coverUrl"`
	Genres          []string                  `json:"genres"`
	Tags            []string                  `json:"tags"`
	Authors         []database.Author         `json:"authors"`
	AlternateTitles []database.AlternateTitle `json:"alternateTitles"`
	Confidence      float64                   `json:"confidence"`
}

func CoverEndpointURL(subjectID int64) string {
	return fmt.Sprintf("https://api.bgm.tv/v0/subjects/%d/image?type=common", subjectID)
}

func SubjectWebURL(subjectID int64) string {
	return fmt.Sprintf("https://bgm.tv/subject/%d", subjectID)
}

func PlatformName(code int) string {
	switch code {
	case PlatformComic:
		return "漫画"
	case PlatformNovel:
		return "小说"
	case PlatformIllustration:
		return "画集"
	case 1004:
		return "绘本"
	case 1005:
		return "写真"
	case 1006:
		return "公式书"
	default:
		return "书籍"
	}
}

func PlatformCodeFromString(label string) int {
	s := ToSimplified(strings.TrimSpace(label))
	switch {
	case strings.Contains(s, "漫画"):
		return PlatformComic
	case strings.Contains(s, "小说"):
		return PlatformNovel
	case strings.Contains(s, "画集"):
		return PlatformIllustration
	case strings.Contains(s, "绘本"):
		return 1004
	case strings.Contains(s, "写真"):
		return 1005
	case strings.Contains(s, "公式"):
		return 1006
	default:
		return 0
	}
}

// ScoreCandidate evaluates how well a Bangumi SubjectCandidate matches a ParsedQuery.
// Returns a confidence score in [0, 1].
func ScoreCandidate(query ParsedQuery, cand SubjectCandidate, targetIsOneShot bool) float64 {
	var candKeys []string
	primaryKeys := map[string]bool{}
	addKey := func(raw string, primary bool) {
		k := NormalizeMatchKey(raw)
		if k == "" {
			return
		}
		candKeys = appendUniqueText(candKeys, k)
		if primary {
			primaryKeys[k] = true
		}
		// Also strip edition suffix for base-title comparison
		stripped := NormalizeMatchKey(editionRe.ReplaceAllString(ToSimplified(raw), " "))
		if stripped != "" && stripped != k {
			candKeys = appendUniqueText(candKeys, stripped)
		}
	}
	addKey(cand.NameCN, true)
	addKey(cand.Name, true)
	for _, alt := range cand.AlternateTitles {
		addKey(alt.Title, false)
	}
	if len(candKeys) == 0 {
		return 0
	}

	bestTitleScore := 0.0
	for idx, qRaw := range query.Candidates {
		qk := NormalizeMatchKey(qRaw)
		if qk == "" {
			continue
		}
		rankPenalty := math.Max(0, float64(idx)*0.03)
		for _, ck := range candKeys {
			s := compareNormalizedKeys(qk, ck, primaryKeys[ck])
			if s-rankPenalty > bestTitleScore {
				bestTitleScore = s - rankPenalty
			}
		}
	}
	if bestTitleScore < 0.35 {
		return 0
	}

	score := bestTitleScore

	// Platform weighting: strongly prefer Manga over Light Novel / Artbook
	switch cand.Platform {
	case PlatformComic:
		score += 0.08
	case 0:
		score += 0.02
	case PlatformNovel:
		score -= 0.22
	case PlatformIllustration, 1005, 1006:
		score -= 0.18
	}

	// Series vs One-Shot weighting
	if !targetIsOneShot {
		if cand.IsSeries {
			score += 0.06
		}
	}

	// Edition hint alignment (e.g. "完全版" in folder name matches "完全版" in Bangumi title)
	if len(query.EditionHints) > 0 {
		combinedTitle := ToSimplified(cand.NameCN + " " + cand.Name)
		matchedEd := false
		for _, ed := range query.EditionHints {
			if strings.Contains(combinedTitle, ed) {
				matchedEd = true
				break
			}
		}
		if matchedEd {
			score += 0.04
		}
	} else {
		// When folder has NO edition hint, slightly prefer main entry over "完全版 / 原画集 / 公式书 / 外传"
		combinedTitle := ToSimplified(cand.NameCN + " " + cand.Name)
		for _, suffix := range []string{"完全版", "爱藏版", "新装版", "文库版", "画集", "公式", "外传", "选集", "公式ガイド"} {
			if strings.Contains(combinedTitle, suffix) {
				score -= 0.05
				break
			}
		}
	}

	// Author cross-check bonus / penalty
	if len(query.Authors) > 0 && len(cand.Authors) > 0 {
		authorMatched := false
		for _, qa := range query.Authors {
			qak := NormalizeMatchKey(qa)
			if len([]rune(qak)) < 2 {
				continue
			}
			for _, ca := range cand.Authors {
				cak := NormalizeMatchKey(ca.Name)
				if cak != "" && (cak == qak || strings.Contains(cak, qak) || strings.Contains(qak, cak)) {
					authorMatched = true
					break
				}
			}
			if authorMatched {
				break
			}
		}
		if authorMatched {
			score += 0.14
		} else if bestTitleScore < 0.88 {
			score -= 0.08
		}
	}

	// Short single-character or 2-ASCII-letter titles require stricter exactness
	if len(query.Candidates) > 0 && len([]rune(NormalizeMatchKey(query.Candidates[0]))) <= 1 {
		if bestTitleScore < 0.90 {
			score -= 0.25
		}
	}

	// Subtle popularity tie-breaker (0 to +0.02)
	if cand.Rank > 0 && cand.Rank < 10000 {
		score += 0.02 * (1.0 - float64(cand.Rank)/10000.0)
	} else if cand.Score != nil && *cand.Score > 0 {
		score += 0.005
	}

	if score > 0.99 {
		score = 0.99
	}
	if score < 0 {
		score = 0
	}
	return math.Round(score*1000) / 1000
}

func compareNormalizedKeys(qk, ck string, isPrimary bool) float64 {
	if qk == ck {
		if isPrimary {
			return 0.88
		}
		return 0.83
	}
	qRunes := []rune(qk)
	cRunes := []rune(ck)
	minLen := min(len(qRunes), len(cRunes))
	maxLen := max(len(qRunes), len(cRunes))
	if minLen < 2 || maxLen == 0 {
		return 0
	}
	ratio := float64(minLen) / float64(maxLen)
	if strings.HasPrefix(ck, qk) || strings.HasPrefix(qk, ck) {
		if ratio >= 0.65 {
			return 0.68 + 0.14*ratio
		}
		if minLen >= 4 && ratio >= 0.45 {
			return 0.56 + 0.12*ratio
		}
	}
	if minLen >= 4 && (strings.Contains(ck, qk) || strings.Contains(qk, ck)) && ratio >= 0.55 {
		return 0.54 + 0.12*ratio
	}
	return 0
}

func ToScrapedMetadata(seriesName string, cand SubjectCandidate) database.ScrapedMetadata {
	displayTitle := strings.TrimSpace(cand.NameCN)
	if displayTitle == "" {
		displayTitle = strings.TrimSpace(cand.Name)
	}
	if displayTitle == "" {
		displayTitle = seriesName
	}
	var altTitles []database.AlternateTitle
	if orig := strings.TrimSpace(cand.Name); orig != "" && orig != displayTitle {
		altTitles = append(altTitles, database.AlternateTitle{Label: "原名", Title: orig})
	}
	if seriesName != "" && seriesName != displayTitle && seriesName != cand.Name {
		altTitles = append(altTitles, database.AlternateTitle{Label: "目录名", Title: seriesName})
	}
	for _, alt := range cand.AlternateTitles {
		if alt.Title != "" && alt.Title != displayTitle && alt.Title != cand.Name {
			altTitles = append(altTitles, alt)
		}
	}
	coverURL := cand.CoverURL
	if coverURL == "" && cand.ID > 0 {
		coverURL = CoverEndpointURL(cand.ID)
	}
	status := cand.Status
	if status == "" {
		status = "ONGOING"
	}
	return database.ScrapedMetadata{
		SubjectID:       cand.ID,
		Confidence:      cand.Confidence,
		CoverURL:        coverURL,
		Score:           cand.Score,
		Status:          status,
		Title:           displayTitle,
		TitleSort:       displayTitle,
		Summary:         strings.TrimSpace(cand.Summary),
		Publisher:       strings.TrimSpace(cand.Publisher),
		TotalBookCount:  cand.TotalVolumes,
		ReleaseDate:     cand.ReleaseDate,
		Genres:          cand.Genres,
		Tags:            cand.Tags,
		Authors:         cand.Authors,
		AlternateTitles: altTitles,
		Links: []database.WebLink{
			{Label: "Bangumi", URL: SubjectWebURL(cand.ID)},
		},
	}
}
