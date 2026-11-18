package bangumi

import (
	"regexp"
	"strings"
)

type ParsedQuery struct {
	Raw          string   `json:"raw"`
	Candidates   []string `json:"candidates"`
	Authors      []string `json:"authors"`
	Publisher    string   `json:"publisher"`
	EditionHints []string `json:"editionHints"`
}

var (
	squareBracketRe = regexp.MustCompile(`\[([^\[\]]+)\]|【([^【】]+)】`)
	parenGroupRe    = regexp.MustCompile(`\(([^()]+)\)|（([^（）]+)）`)
	leadingNumRe    = regexp.MustCompile(`^\s*\d{1,4}\s*[._\-、]\s*`)
	trailingVolRe   = regexp.MustCompile(`(?i)[\s_\-]*(?:第?\s*\d+(?:\s*[-~～]\s*\d+)?\s*[卷册冊话話集部篇期巻]|vol\.?\s*\d+(?:\s*[-~～]\s*\d+)?|v\d+(?:\s*[-~～]\s*v?\d+)?|全\s*\d+\s*[卷册冊话話集巻]?|\d+\s*[-~～]\s*\d+\s*(?:完|未完)?|\d+\s*完)\s*$`)
	editionRe       = regexp.MustCompile(`(?i)(完全版|爱藏版|新装版|新装再编版|文库版|豪华版|珍藏版|典藏版|彩版|全彩版|彩色版|单行本|合集|短篇集|外传)`)
	noiseWordRe     = regexp.MustCompile(`(?i)^(?:一般コミック|少年コミック|青年コミック|少女コミック|漫画|コミック|comic|manga|汉化|中文|繁体|简体|港版|台版|日版|境外版|电子版|高清|扫图|digital|dl版|连载中?|未完|完结?|已完结|全\d+[卷册话巻]?|\d+[-~～]\d+.*|\d+卷?完?|vol\.?\s*\d+.*|v\d+.*|\d{4}年?)$`)

	knownPublishers = []string{
		"东立", "尖端", "青文", "玉皇朝", "天下", "文化传信", "长鸿", "台湾角川", "角川",
		"集英社", "讲谈社", "小学馆", "白泉社", "芳文社", "新潮社", "秋田书店",
		"少年画报社", "一迅社", "双叶社", "竹书房", "德间书店", "祥传社", "史克威尔艾尼克斯",
		"square enix", "kadokawa", "shueisha", "kodansha", "shogakukan", "hakusensha",
		"脸谱", "时报", "东贩", "台湾东贩", "威向", "四季", "青空文化", "中信", "人民文学",
		"新星", "磨铁", "天闻角川", "次元书馆", "后浪", "漫编室", "bilibili", "哔哩哔哩",
	}
)

// ParseSeriesName extracts ranked title search candidates, author hints, and publisher
// from messy real-world manga folder/archive names.
func ParseSeriesName(rawName string) ParsedQuery {
	rawName = strings.TrimSpace(rawName)
	if rawName == "" {
		return ParsedQuery{}
	}
	simp := ToSimplified(rawName)

	var (
		brackets     []string
		authors      []string
		publisher    string
		editionHints []string
	)

	for _, m := range squareBracketRe.FindAllStringSubmatch(simp, -1) {
		token := strings.TrimSpace(m[1])
		if token == "" {
			token = strings.TrimSpace(m[2])
		}
		if token != "" {
			brackets = append(brackets, token)
		}
	}
	for _, m := range parenGroupRe.FindAllStringSubmatch(simp, -1) {
		token := strings.TrimSpace(m[1])
		if token == "" {
			token = strings.TrimSpace(m[2])
		}
		if token == "" {
			continue
		}
		if pub := matchPublisher(token); pub != "" && publisher == "" {
			publisher = pub
		}
		if ed := editionRe.FindString(token); ed != "" {
			editionHints = appendUniqueText(editionHints, ed)
		}
	}

	outside := squareBracketRe.ReplaceAllString(simp, " ")
	outside = parenGroupRe.ReplaceAllString(outside, " ")
	outside = cleanTitleSurface(outside)

	var titleTokens []string
	for idx, token := range brackets {
		if pub := matchPublisher(token); pub != "" {
			if publisher == "" {
				publisher = pub
			}
			continue
		}
		if ed := editionRe.FindString(token); ed != "" {
			editionHints = appendUniqueText(editionHints, ed)
		}
		if isNoiseBracket(token) {
			continue
		}
		// Co-author syntax like [山田钟人×阿部司] or [原作:A 作画:B]
		if strings.ContainsAny(token, "×✕") || strings.Contains(token, "原作") || strings.Contains(token, "作画") {
			for _, a := range splitAuthorsToken(token) {
				authors = appendUniqueText(authors, a)
			}
			continue
		}
		if outside == "" {
			// Vol.moe / Kox.moe pattern: [Title][Author][Publisher][Vol]
			if len(titleTokens) == 0 && idx <= 1 {
				titleTokens = append(titleTokens, token)
			} else {
				for _, a := range splitAuthorsToken(token) {
					authors = appendUniqueText(authors, a)
				}
			}
		} else {
			// Scanlation / raw pattern: [Author] Title
			for _, a := range splitAuthorsToken(token) {
				authors = appendUniqueText(authors, a)
			}
		}
	}

	var candidates []string
	addCandidate := func(val string) {
		val = cleanTitleSurface(val)
		if len([]rune(val)) == 0 {
			return
		}
		// Avoid adding a pure author name or pure number if we already have stronger candidates
		candidates = appendUniqueText(candidates, val)
		// If candidate contains edition suffix like "灌篮高手 完全版", also add base title "灌篮高手"
		strippedEd := cleanTitleSurface(editionRe.ReplaceAllString(val, " "))
		if strippedEd != "" && strippedEd != val {
			candidates = appendUniqueText(candidates, strippedEd)
		}
		// If candidate has subtitle separators ("_" / "/" / "-" / "：" / ":"), add individual parts
		for _, sep := range []string{"_", "／", "/", " - ", "：", ":", "～", "~"} {
			if strings.Contains(val, sep) {
				for _, sub := range strings.Split(val, sep) {
					sub = cleanTitleSurface(sub)
					if len([]rune(sub)) >= 2 {
						candidates = appendUniqueText(candidates, sub)
					}
				}
			}
		}
	}

	if outside != "" {
		// Check "Author - Title" or "Title - Author" when no author was extracted from brackets
		if len(authors) == 0 && strings.Contains(outside, " - ") {
			parts := strings.SplitN(outside, " - ", 2)
			left := cleanTitleSurface(parts[0])
			right := cleanTitleSurface(parts[1])
			if left != "" && right != "" {
				addCandidate(outside)
				addCandidate(right)
				addCandidate(left)
			} else {
				addCandidate(outside)
			}
		} else {
			addCandidate(outside)
		}
	}
	for _, tok := range titleTokens {
		addCandidate(tok)
	}
	if len(candidates) == 0 {
		// Last-resort fallback on stripped raw string
		fallback := cleanTitleSurface(simp)
		if fallback != "" {
			candidates = appendUniqueText(candidates, fallback)
		} else {
			candidates = appendUniqueText(candidates, simp)
		}
	}

	return ParsedQuery{
		Raw:          rawName,
		Candidates:   candidates,
		Authors:      authors,
		Publisher:    publisher,
		EditionHints: editionHints,
	}
}

func cleanTitleSurface(s string) string {
	s = strings.TrimSpace(s)
	s = leadingNumRe.ReplaceAllString(s, "")
	for {
		next := strings.TrimSpace(trailingVolRe.ReplaceAllString(s, ""))
		if next == s {
			break
		}
		s = next
	}
	s = strings.Trim(s, " \t\r\n-–—_·•:：/\\|")
	s = strings.Join(strings.Fields(s), " ")
	return s
}

func matchPublisher(token string) string {
	lower := strings.ToLower(strings.TrimSpace(token))
	lower = strings.TrimSuffix(lower, "出版社")
	lower = strings.TrimSuffix(lower, "出版")
	for _, pub := range knownPublishers {
		if lower == pub || strings.Contains(lower, pub) {
			return pub
		}
	}
	return ""
}

func isNoiseBracket(token string) bool {
	t := strings.TrimSpace(token)
	if t == "" {
		return true
	}
	if noiseWordRe.MatchString(t) {
		return true
	}
	if trailingVolRe.MatchString(t) && cleanTitleSurface(t) == "" {
		return true
	}
	return false
}

func splitAuthorsToken(token string) []string {
	cleaned := parenGroupRe.ReplaceAllString(token, "")
	for _, prefix := range []string{"原作:", "原作：", "作画:", "作画：", "作者:", "作者：", "漫画:", "漫画：", "著:", "著："} {
		cleaned = strings.ReplaceAll(cleaned, prefix, " ")
	}
	fields := strings.FieldsFunc(cleaned, func(r rune) bool {
		switch r {
		case '×', '✕', '、', ',', '，', '/', '／', '&', ';', '；':
			return true
		default:
			return false
		}
	})
	var out []string
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if len([]rune(f)) >= 2 && !isNoiseBracket(f) {
			out = appendUniqueText(out, f)
		}
	}
	return out
}

func appendUniqueText(list []string, item string) []string {
	item = strings.TrimSpace(item)
	if item == "" {
		return list
	}
	norm := strings.ToLower(item)
	for _, existing := range list {
		if strings.ToLower(existing) == norm {
			return list
		}
	}
	return append(list, item)
}
