package bangumi

import (
	"regexp"
	"strings"
	"unicode"
)

type ParsedQuery struct {
	Raw             string   `json:"raw"`
	Candidates      []string `json:"candidates"`
	Authors         []string `json:"authors"`
	Publisher       string   `json:"publisher"`
	EditionHints    []string `json:"editionHints"`
	SubfolderSuffix string   `json:"subfolderSuffix,omitempty"`
}

var (
	squareBracketRe    = regexp.MustCompile(`\[([^\[\]]+)\]|【([^【】]+)】`)
	parenGroupRe       = regexp.MustCompile(`\(([^()]+)\)|（([^（）]+)）`)
	trailingParenRe    = regexp.MustCompile(`\s*(?:\([^()]*\)|（[^（）]*）)\s*$`)
	leadingDateRe      = regexp.MustCompile(`^\s*\d{4}[.\-/年]\d{1,2}(?:[.\-/月]\d{1,2}日?)?[.\-_\s]*`)
	leadingOKRe        = regexp.MustCompile(`(?i)^\s*ok\s*[-_]+\s*`)
	leadingTagBeforeBr = regexp.MustCompile(`(?i)^\s*(?:[a-z]|\d{1,4})\s*([\[【])`)
	leadingNumRe       = regexp.MustCompile(`^\s*\d{1,4}\s*[._\-、]\s*`)
	trailingVolRe      = regexp.MustCompile(`(?i)[\s_\-]*(?:第?\s*\d+(?:\s*[-~～]\s*\d+)?\s*[卷册冊话話集部篇期巻](?:全|完|未完)?|(?:vol|chap|ch|episode|ep)\.?\s*\d+(?:\s*[-~～]\s*\d+)?|v\d+(?:\s*[-~～]\s*v?\d+)?|全\s*\d+\s*[卷册冊话話集巻]?|全[一1][卷册冊]|\d+\s*[-~～]\s*\d+\s*(?:完|未|未完|卷全|册全)?|\d+\s*(?:完|未|未完|卷全|册全)|(?:上下[卷册冊]?|上中下)|[\s_\-]+(?:完结|已完结|连载中|未完))\s*$`)
	editionRe          = regexp.MustCompile(`(?i)(数码全彩版?|全彩汉化版|全彩版?|彩色版|彩版|完全版|爱藏版|新装再编版|新装版|文库版|豪华版|珍藏版|典藏版|特装版|限定版|纪念版|总集篇|单行本|合集|短篇集|外传|漫画版|コミカライズ|[@＠]\s*comic)`)
	noiseWordRe        = regexp.MustCompile(`(?i)^(?:一般コミック|成年コミック|少年コミック|青年コミック|少女コミック|女性コミック|ボーイズラブ|漫画|コミック|comic|comics|manga|汉化|中文|繁体|繁中|简体|简中|港版|台版|日版|韩版|美版|英文版|境外版|电子版|数码版|高清|扫图|自扫|自购|无水印|有水印|双页|单页|digital|dl版|epub|mobi|pdf|zip|cbz|cbr|rar|raw|raws|scan|scans|chn|jpn|eng|enu|esp|kor|cht|chs|cs|连载中?|未完|完结?|已完结|全\d+[卷册话巻]?|\d+[-~～]\d+.*|\d+卷?(?:完|未|全)?|(?:vol|chap|ch)\.?\s*\d+.*|v\d+.*|\d{4}(?:[.\-/年]\d{1,2})?年?|\d{3,4}[px]|\d+x\d+)$`)
	scanlatorSuffixRe  = regexp.MustCompile(`(?i)(?:汉化组|汉化社|翻译组|制作组|扫图组|数码组|个人汉化|联合汉化|自扫|自购|手打|出品|压制|收集|整理|补档)$`)
	coAuthorSplitRe    = regexp.MustCompile(`\s*[×✕xX]\s*`)
	numberedSubfolder  = regexp.MustCompile(`(?i)^\s*(?:\d{1,2}\s*[._\- ]+\s*)?(第一部|第二部|第三部|第四部|第\d+部|前篇|后篇|本篇|正篇|外传|番外|番外篇|特别篇|短篇|短篇集|新版|旧版|新系列|r系列|sp|ova|oad|oped|特典|单行本|非单行本|连载|连载汉化|台版|港版|日版|完全版|爱藏版|新装版|文库版|全彩版|彩版|合集|公式书|设定集|画集)\s*$`)
	r18BoundaryRe      = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(r-?18|18\+|18禁|18rin)(?:$|[^a-z0-9])`)

	knownPublishers = []string{
		"东立", "尖端", "青文", "玉皇朝", "天下", "文化传信", "长鸿", "台湾角川", "角川",
		"集英社", "讲谈社", "小学馆", "白泉社", "芳文社", "新潮社", "秋田书店",
		"少年画报社", "一迅社", "双叶社", "竹书房", "德间书店", "祥传社", "史克威尔艾尼克斯",
		"square enix", "kadokawa", "shueisha", "kodansha", "shogakukan", "hakusensha",
		"脸谱", "时报", "东贩", "台湾东贩", "威向", "四季", "青空文化", "中信", "人民文学",
		"新星", "磨铁", "天闻角川", "次元书馆", "后浪", "漫编室", "bilibili", "哔哩哔哩",
		"未来数位",
	}

	unambiguousScanlators = map[string]bool{
		"绛英": true, "瓜瓜": true, "小良": true, "铜板": true, "zero": true,
		"zero有水印": true, "zero无水印": true, "bili": true, "b漫": true,
		"哔哩哔哩漫画": true, "腾讯动漫": true, "动漫之家": true, "dmzj": true,
		"c.c": true, "c.c.": true, "cc": true, "hmm": true, "kox": true,
		"vol": true, "vol.moe": true, "kox.moe": true, "azusa": true, "梓喵": true,
		"漫画补档": true, "补档": true, "堕落的猴子": true, "猴子": true, "团子": true,
		"脸肿": true, "无邪气": true, "红莲": true, "离境": true, "网源": true,
	}

	// Scanlator credits that happen to collide with an actual manga title (e.g. 恶之华).
	// Treated as noise when outside != "" or at bracket index >= 2, and deprioritized when at index 0.
	ambiguousScanlators = map[string]bool{
		"恶之华": true,
	}

	genericSubfolderExact = map[string]string{
		"台版": "台版", "港版": "港版", "日版": "日版", "韩版": "韩版", "美版": "美版", "英文版": "英文版",
		"东立": "东立版", "东立版": "东立版", "尖端": "尖端版", "尖端版": "尖端版",
		"青文": "青文版", "青文版": "青文版", "玉皇朝": "玉皇朝版", "文化传信": "文传版",
		"单行本": "单行本", "非单行本": "非单行本", "连载": "连载", "连载话": "连载", "连载版": "连载版",
		"连载汉化": "连载汉化", "杂志连载": "杂志连载", "电子版": "电子版", "扫图版": "扫图版",
		"全彩": "全彩版", "全彩版": "全彩版", "彩色版": "彩色版", "彩版": "彩版",
		"完全版": "完全版", "爱藏版": "爱藏版", "新装版": "新装版", "文库版": "文库版", "合集": "合集",
		"特别篇": "特别篇", "番外": "番外", "番外篇": "番外篇", "外传": "外传", "特典": "特典",
		"小册子": "特典", "短篇": "短篇", "短篇集": "短篇集", "公式书": "公式书", "设定集": "设定集", "画集": "画集",
		"oped": "OPED", "sp": "SP", "ova": "OVA", "oad": "OAD",
		"epub": "EPUB", "epub提取": "EPUB提取", "mobi": "MOBI", "pdf": "PDF",
		"zip": "ZIP", "cbz": "CBZ", "cbr": "CBR", "rar": "RAR",
		"chn": "中文", "cht": "繁中", "chs": "简中", "cs": "中文",
		"jpn": "日版", "eng": "英文版", "enu": "英文版", "esp": "西语版", "kor": "韩版",
		"上": "上", "下": "下", "前篇": "前篇", "后篇": "后篇",
	}

	genericBucketParents = map[string]bool{
		"漫画": true, "日本漫画": true, "港台漫画": true, "欧美漫画": true, "韩国漫画": true, "国产漫画": true,
		"日漫": true, "港漫": true, "韩漫": true, "国漫": true, "美漫": true,
		"完结": true, "已完结": true, "连载": true, "连载中": true, "未完": true,
		"短篇": true, "合集": true, "补档": true, "漫画补档": true, "new": true, "comics": true, "manga": true,
	}

	adultPathKeywords = []string{
		"未来数位", "无修正", "成人漫画", "成人向", "成年コミック", "成年向", "エロ漫画", "h漫", "禁漫", "本子合集",
	}
)

// ParseSeriesName extracts ranked title search candidates, author hints, and publisher
// from messy real-world manga folder/archive names.
func ParseSeriesName(rawName string) ParsedQuery {
	return ParseSeriesWithPath(rawName, "")
}

// ParseSeriesWithPath parses rawName and falls back to the parent series folder in relativePath
// when rawName is a generic edition/extra subfolder (such as "台版", "特别篇", "单行本", "[CHN]").
func ParseSeriesWithPath(rawName, relativePath string) ParsedQuery {
	rawName = strings.TrimSpace(rawName)
	if rawName == "" {
		return ParsedQuery{}
	}
	if parentName, suffix := detectGenericSubfolder(rawName, relativePath); parentName != "" {
		parentParsed := parseCoreName(parentName)
		if len(parentParsed.Candidates) > 0 {
			parentParsed.Raw = rawName
			parentParsed.SubfolderSuffix = suffix
			return parentParsed
		}
	}
	return parseCoreName(rawName)
}

func parseCoreName(rawName string) ParsedQuery {
	simp := preprocessSurface(ToSimplified(rawName))

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

	outsideWithParens := squareBracketRe.ReplaceAllString(simp, " ")
	outsideStripped := parenGroupRe.ReplaceAllString(outsideWithParens, " ")
	outside := cleanTitleSurface(outsideStripped)

	var (
		cleanBrackets   []string
		ambiguousAtZero string
	)
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
		lowerTok := strings.ToLower(strings.TrimSpace(token))
		if ambiguousScanlators[lowerTok] {
			if outside != "" || idx >= 2 || len(cleanBrackets) >= 2 {
				continue
			}
			if len(cleanBrackets) == 0 {
				ambiguousAtZero = token
				continue
			}
		}
		if isCoAuthorBracket(token) {
			for _, a := range splitAuthorsToken(token) {
				authors = appendUniqueText(authors, a)
			}
			continue
		}
		if outside == "" {
			cleanBrackets = append(cleanBrackets, token)
		} else {
			for _, a := range splitAuthorsToken(token) {
				authors = appendUniqueText(authors, a)
			}
		}
	}

	var titleTokens []string
	if outside == "" {
		// Support both [Title][Author] and [Author][Title] (and up to 3 leading clean brackets).
		for i, tok := range cleanBrackets {
			if i < 3 {
				titleTokens = append(titleTokens, tok)
			}
			for _, a := range splitAuthorsToken(tok) {
				authors = appendUniqueText(authors, a)
			}
		}
		if ambiguousAtZero != "" {
			// Put real title bracket(s) ahead of ambiguous scanlator name, but keep it as fallback
			// so genuine manga "[押见修造][恶之华]" or "[恶之华]" still matches!
			titleTokens = append(titleTokens, ambiguousAtZero)
		}
	}

	var candidates []string
	var addCandidate func(val string, depth int)
	addCandidate = func(val string, depth int) {
		if depth > 2 {
			return
		}
		rawVal := strings.TrimSpace(val)
		if rawVal == "" {
			return
		}

		// Extract outer & inner parenthesized alternate titles before stripping parens
		outerText, innerParts := extractParenParts(rawVal)
		cleaned := cleanTitleSurface(outerText)
		if cleaned == "" {
			cleaned = cleanTitleSurface(rawVal)
		}
		if len([]rune(cleaned)) > 0 && !isNoiseBracket(cleaned) {
			candidates = appendUniqueText(candidates, cleaned)

			strippedEd := cleanTitleSurface(editionRe.ReplaceAllString(cleaned, " "))
			if strippedEd != "" && strippedEd != cleaned && !isNoiseBracket(strippedEd) {
				candidates = appendUniqueText(candidates, strippedEd)
			}

			// Split subtitle / bilingual separators
			for _, sep := range []string{"_", "／", "/", " - ", " — ", " ─ ", "※", "：", ":", "～", "~", "-"} {
				if strings.Contains(cleaned, sep) {
					parts := strings.Split(cleaned, sep)
					for _, sub := range parts {
						sub = cleanTitleSurface(sub)
						if sep == "-" && !isStrongDashPart(sub) {
							continue
						}
						if len([]rune(sub)) >= 2 && !isNoiseBracket(sub) {
							candidates = appendUniqueText(candidates, sub)
						}
					}
				}
			}

			// Split CJK <-> Latin bilingual concatenated titles (e.g. "ARIA 水星领航员", "BASARA婆娑罗", "死神BLEACH")
			for _, part := range splitLatinCJKPair(cleaned) {
				if len([]rune(part)) >= 2 && !isNoiseBracket(part) {
					candidates = appendUniqueText(candidates, part)
				}
			}
		}

		for _, inner := range innerParts {
			innerClean := cleanTitleSurface(inner)
			if len([]rune(innerClean)) >= 2 && matchPublisher(innerClean) == "" && !isNoiseBracket(innerClean) && !editionRe.MatchString(innerClean) {
				addCandidate(innerClean, depth+1)
			}
		}
	}

	if outside != "" {
		if len(authors) == 0 && strings.Contains(outsideWithParens, " - ") {
			parts := strings.SplitN(outsideWithParens, " - ", 2)
			left := cleanTitleSurface(parts[0])
			right := cleanTitleSurface(parts[1])
			if left != "" && right != "" {
				addCandidate(outsideWithParens, 0)
				addCandidate(right, 0)
				addCandidate(left, 0)
			} else {
				addCandidate(outsideWithParens, 0)
			}
		} else {
			addCandidate(outsideWithParens, 0)
		}
	}
	for _, tok := range titleTokens {
		addCandidate(tok, 0)
	}
	if len(candidates) == 0 {
		fallback := cleanTitleSurface(simp)
		if fallback != "" {
			candidates = appendUniqueText(candidates, fallback)
		} else {
			candidates = appendUniqueText(candidates, strings.TrimSpace(simp))
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

func preprocessSurface(s string) string {
	s = strings.TrimSpace(s)
	s = leadingDateRe.ReplaceAllString(s, "")
	s = leadingOKRe.ReplaceAllString(s, "")
	s = leadingTagBeforeBr.ReplaceAllString(s, "$1")
	return strings.TrimSpace(s)
}

func cleanTitleSurface(s string) string {
	s = strings.TrimSpace(s)
	s = leadingDateRe.ReplaceAllString(s, "")
	s = leadingOKRe.ReplaceAllString(s, "")
	s = leadingNumRe.ReplaceAllString(s, "")
	for {
		next := strings.TrimSpace(trailingVolRe.ReplaceAllString(s, ""))
		if next == s {
			break
		}
		s = next
	}
	s = strings.Trim(s, " \t\r\n-–—─_·•・:：/\\|[]【】")
	s = strings.Join(strings.Fields(s), " ")
	return s
}

func extractParenParts(s string) (string, []string) {
	matches := parenGroupRe.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return s, nil
	}
	var inners []string
	for _, m := range matches {
		tok := strings.TrimSpace(m[1])
		if tok == "" {
			tok = strings.TrimSpace(m[2])
		}
		if tok != "" {
			inners = append(inners, tok)
		}
	}
	outer := strings.TrimSpace(parenGroupRe.ReplaceAllString(s, " "))
	return outer, inners
}

func stripTrailingParens(s string) string {
	cur := strings.TrimSpace(s)
	for {
		next := strings.TrimSpace(trailingParenRe.ReplaceAllString(cur, ""))
		if next == cur || len([]rune(next)) < 2 {
			break
		}
		cur = next
	}
	return cur
}

func detectGenericSubfolder(rawName, relativePath string) (parentName string, suffix string) {
	if strings.TrimSpace(relativePath) == "" {
		return "", ""
	}
	suffix = classifyGenericSubfolderSuffix(rawName)
	if suffix == "" {
		return "", ""
	}
	rel := strings.Trim(strings.ReplaceAll(relativePath, "\\", "/"), "/")
	segments := strings.Split(rel, "/")
	// segments[0] is the library root folder name; a parent series folder requires len >= 3.
	if len(segments) < 3 {
		return "", ""
	}
	for i := len(segments) - 2; i >= 1; i-- {
		candParent := strings.TrimSpace(segments[i])
		if candParent == "" {
			continue
		}
		if classifyGenericSubfolderSuffix(candParent) != "" {
			continue
		}
		lowerParent := strings.ToLower(ToSimplified(candParent))
		if genericBucketParents[lowerParent] || len([]rune(lowerParent)) <= 1 {
			return "", ""
		}
		return candParent, suffix
	}
	return "", ""
}

func classifyGenericSubfolderSuffix(rawName string) string {
	simp := strings.TrimSpace(ToSimplified(rawName))
	if simp == "" {
		return ""
	}
	trimmed := strings.Trim(simp, "[]【】()（） \t_-.")
	lower := strings.ToLower(trimmed)
	if mapped, ok := genericSubfolderExact[lower]; ok {
		return mapped
	}
	if m := numberedSubfolder.FindStringSubmatch(trimmed); len(m) == 2 {
		sub := strings.ToLower(strings.TrimSpace(m[1]))
		if mapped, ok := genericSubfolderExact[sub]; ok {
			return mapped
		}
		return strings.TrimSpace(m[1])
	}
	return ""
}

// InferPathNSFW checks whether folder/path names contain explicit adult/18+ classification markers.
// Note: Bangumi-matched subjects rely strictly on Bangumi's official boolean `nsfw == true` field;
// this helper is used only when Bangumi auto-matching does not match a subject.
func InferPathNSFW(rawName, relativePath string) bool {
	combined := strings.ToLower(ToSimplified(rawName + " " + relativePath))
	for _, kw := range adultPathKeywords {
		if strings.Contains(combined, kw) {
			return true
		}
	}
	return r18BoundaryRe.MatchString(combined)
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
	lower := strings.ToLower(ToSimplified(t))
	if unambiguousScanlators[lower] {
		return true
	}
	if noiseWordRe.MatchString(lower) {
		return true
	}
	if len([]rune(lower)) <= 9 && scanlatorSuffixRe.MatchString(lower) {
		return true
	}
	if trailingVolRe.MatchString(t) && cleanTitleSurface(t) == "" {
		return true
	}
	return false
}

func isCoAuthorBracket(token string) bool {
	if strings.Contains(token, "原作") || strings.Contains(token, "作画") || strings.Contains(token, "漫画:") || strings.Contains(token, "漫画：") || strings.Contains(token, "脚本") {
		return true
	}
	if !strings.ContainsAny(token, "×✕xX") {
		return false
	}
	parts := coAuthorSplitRe.Split(parenGroupRe.ReplaceAllString(token, ""), -1)
	if len(parts) < 2 || len(parts) > 4 {
		return false
	}
	var prev string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if !isLikelyCJKPersonName(p) {
			return false
		}
		if strings.EqualFold(p, prev) {
			return false // e.g. 猎人×猎人
		}
		prev = p
	}
	return true
}

func isLikelyCJKPersonName(s string) bool {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) < 2 || len(runes) > 8 {
		return false
	}
	if editionRe.MatchString(s) || isNoiseBracket(s) {
		return false
	}
	cjkCount := 0
	for _, r := range runes {
		switch {
		case unicode.Is(unicode.Han, r), unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r):
			cjkCount++
		case r == '・' || r == '·' || r == ' ':
			continue
		default:
			return false
		}
	}
	return cjkCount >= 2
}

func isStrongDashPart(s string) bool {
	runes := []rune(s)
	han := 0
	latin := 0
	for _, r := range runes {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			han++
		} else if unicode.IsLetter(r) {
			latin++
		}
	}
	return han >= 2 || latin >= 4
}

func splitLatinCJKPair(s string) []string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) < 5 {
		return nil
	}
	isCJK := func(r rune) bool {
		return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)
	}
	isLat := func(r rune) bool {
		return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
	}

	// Case 1: Leading Latin + Trailing CJK (e.g. "ARIA 水星领航员", "BASARA婆娑罗", "Sword Art Online刀剑神域")
	if isLat(runes[0]) {
		firstCJK := -1
		for i, r := range runes {
			if isCJK(r) {
				firstCJK = i
				break
			}
		}
		if firstCJK >= 3 {
			left := cleanTitleSurface(string(runes[:firstCJK]))
			right := cleanTitleSurface(string(runes[firstCJK:]))
			if countASCIIAlpha(left) >= 3 && countCJKIdeographs(right) >= 2 && !hasParticlePrefix(right) {
				// Ensure right has no trailing Latin mixed in
				return []string{right, left}
			}
		}
	}

	// Case 2: Leading CJK + Trailing Latin (e.g. "死神BLEACH")
	if isCJK(runes[0]) {
		firstLat := -1
		for i, r := range runes {
			if isLat(r) {
				firstLat = i
				break
			}
		}
		if firstLat >= 2 {
			left := cleanTitleSurface(string(runes[:firstLat]))
			right := cleanTitleSurface(string(runes[firstLat:]))
			if countCJKIdeographs(left) >= 2 && countASCIIAlpha(right) >= 3 && countCJKIdeographs(right) == 0 {
				return []string{left, right}
			}
		}
	}
	return nil
}

func countASCIIAlpha(s string) int {
	n := 0
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			n++
		}
	}
	return n
}

func countCJKIdeographs(s string) int {
	n := 0
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			n++
		}
	}
	return n
}

func hasParticlePrefix(s string) bool {
	for _, p := range []string{"的", "之", "篇", "版", "传", "记", "集", "卷", "部"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func splitAuthorsToken(token string) []string {
	cleaned := parenGroupRe.ReplaceAllString(token, "")
	for _, prefix := range []string{"原作:", "原作：", "作画:", "作画：", "作者:", "作者：", "漫画:", "漫画：", "脚本:", "脚本：", "著:", "著："} {
		cleaned = strings.ReplaceAll(cleaned, prefix, " ")
	}
	if isCoAuthorBracket(token) {
		cleaned = coAuthorSplitRe.ReplaceAllString(cleaned, " / ")
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
		lower := strings.ToLower(ToSimplified(f))
		if len([]rune(f)) >= 2 && !isNoiseBracket(f) && !ambiguousScanlators[lower] {
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
