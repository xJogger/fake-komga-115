package bangumi

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/xJogger/fake-komga-115/internal/database"
)

type WikiInfo struct {
	TitleCN      string
	Aliases      []database.AlternateTitle
	Authors      []database.Author
	Publisher    string
	Status       string
	TotalVolumes *int
	ReleaseDate  string
}

var (
	digitsPrefixRe = regexp.MustCompile(`\d+`)
	dateExtractRe  = regexp.MustCompile(`(\d{4})(?:[年./\-](\d{1,2}))?(?:[月./\-](\d{1,2}))?`)

	knownGenres = map[string]string{
		"科幻": "科幻", "奇幻": "奇幻", "冒险": "冒险", "战斗": "战斗", "热血": "热血",
		"恋爱": "恋爱", "搞笑": "搞笑", "喜剧": "喜剧", "日常": "日常", "治愈": "治愈",
		"推理": "推理", "悬疑": "悬疑", "恐怖": "恐怖", "惊悚": "惊悚", "历史": "历史",
		"战争": "战争", "运动": "运动", "竞技": "竞技", "音乐": "音乐", "美食": "美食",
		"职场": "职场", "校园": "校园", "青春": "青春", "百合": "百合", "耽美": "耽美",
		"机战": "机战", "萝卜": "机战", "异世界": "异世界", "穿越": "穿越", "魔幻": "奇幻",
		"动作": "动作", "武侠": "武侠", "格斗": "格斗", "家庭": "家庭", "社会": "社会",
		"心理": "心理", "哲学": "哲学", "致郁": "致郁", "黑暗": "黑暗", "赛博朋克": "赛博朋克",
		"蒸汽朋克": "蒸汽朋克", "废土": "废土", "末世": "末世", "短篇": "短篇", "四格": "四格",
		"少年漫画": "少年漫画", "少女漫画": "少女漫画", "青年漫画": "青年漫画",
	}
)

// ParseRawInfobox parses Bangumi's raw MediaWiki-like "{{Infobox ...}}" string from bangumi/Archive.
func ParseRawInfobox(raw string) WikiInfo {
	var info WikiInfo
	info.Status = "ONGOING"
	if strings.TrimSpace(raw) == "" {
		return info
	}
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	var (
		currentKey string
		inBlock    bool
		blockItems []string
	)
	flushBlock := func() {
		if currentKey != "" && len(blockItems) > 0 {
			applyInfoboxBlock(&info, currentKey, blockItems)
		}
		currentKey = ""
		inBlock = false
		blockItems = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "{{") || trimmed == "}}" {
			if trimmed == "}}" && inBlock {
				flushBlock()
			}
			continue
		}
		if inBlock {
			if trimmed == "}" {
				flushBlock()
				continue
			}
			if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
				blockItems = append(blockItems, strings.TrimSpace(trimmed[1:len(trimmed)-1]))
			}
			continue
		}
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		eq := strings.IndexByte(trimmed, '=')
		if eq <= 1 {
			continue
		}
		key := strings.TrimSpace(trimmed[1:eq])
		val := strings.TrimSpace(trimmed[eq+1:])
		if val == "{" {
			currentKey = key
			inBlock = true
			blockItems = nil
			continue
		}
		if strings.HasPrefix(val, "{") && strings.HasSuffix(val, "}") {
			inner := strings.TrimSpace(val[1 : len(val)-1])
			if inner != "" {
				applyInfoboxBlock(&info, key, []string{strings.Trim(inner, "[]")})
			}
			continue
		}
		applyInfoboxSingle(&info, key, val)
	}
	if inBlock {
		flushBlock()
	}
	return info
}

func applyInfoboxSingle(info *WikiInfo, key, val string) {
	val = strings.TrimSpace(val)
	if val == "" {
		return
	}
	switch key {
	case "中文名", "简体中文名":
		if info.TitleCN == "" {
			info.TitleCN = val
		}
	case "别名", "其它名称", "其他名称":
		for _, part := range splitMultiValue(val) {
			info.Aliases = append(info.Aliases, database.AlternateTitle{Label: "别名", Title: part})
		}
	case "作者":
		for _, name := range splitMultiValue(val) {
			info.Authors = append(info.Authors, database.Author{Name: name, Role: "writer"})
			info.Authors = append(info.Authors, database.Author{Name: name, Role: "penciller"})
		}
	case "原作", "脚本", "剧本":
		for _, name := range splitMultiValue(val) {
			info.Authors = append(info.Authors, database.Author{Name: name, Role: "writer"})
		}
	case "作画", "漫画", "插图", "人物原案", "作畫":
		for _, name := range splitMultiValue(val) {
			info.Authors = append(info.Authors, database.Author{Name: name, Role: "penciller"})
		}
	case "出版社":
		if info.Publisher == "" {
			parts := splitMultiValue(val)
			if len(parts) > 0 {
				info.Publisher = parts[0]
			}
		}
	case "其他出版社":
		if info.Publisher == "" {
			parts := splitMultiValue(val)
			if len(parts) > 0 {
				info.Publisher = parts[0]
			}
		}
	case "发售日", "开始", "发行日期", "连载开始":
		if info.ReleaseDate == "" {
			info.ReleaseDate = normalizeDateString(val)
		}
	case "结束", "连载结束", "完结":
		if strings.Contains(val, "休刊") {
			info.Status = "HIATUS"
		} else {
			info.Status = "ENDED"
		}
	case "册数", "卷数":
		if m := digitsPrefixRe.FindString(val); m != "" {
			if n, err := strconv.Atoi(m); err == nil && n > 0 {
				info.TotalVolumes = &n
			}
		}
		if strings.Contains(val, "完") {
			info.Status = "ENDED"
		}
	}
}

func applyInfoboxBlock(info *WikiInfo, key string, items []string) {
	if key == "别名" || strings.HasPrefix(key, "版本") {
		for _, raw := range items {
			label := "别名"
			title := raw
			if pipe := strings.IndexByte(raw, '|'); pipe >= 0 {
				k := strings.TrimSpace(raw[:pipe])
				v := strings.TrimSpace(raw[pipe+1:])
				if key != "别名" && k != "版本名" && k != "别名" {
					continue
				}
				if k != "" {
					label = k
				}
				title = v
			}
			title = strings.TrimSpace(title)
			if title != "" {
				info.Aliases = append(info.Aliases, database.AlternateTitle{Label: label, Title: title})
			}
		}
	}
}

func splitMultiValue(val string) []string {
	fields := strings.FieldsFunc(val, func(r rune) bool {
		switch r {
		case '、', ',', '，', '/', '／', ';', '；':
			return true
		default:
			return false
		}
	})
	var out []string
	for _, f := range fields {
		f = strings.TrimSpace(parenGroupRe.ReplaceAllString(f, ""))
		if f != "" {
			out = appendUniqueText(out, f)
		}
	}
	return out
}

func normalizeDateString(raw string) string {
	m := dateExtractRe.FindStringSubmatch(raw)
	if len(m) == 0 {
		return ""
	}
	year := m[1]
	month := "01"
	day := "01"
	if m[2] != "" {
		if n, err := strconv.Atoi(m[2]); err == nil && n >= 1 && n <= 12 {
			month = strconv.Itoa(100 + n)[1:]
		}
	}
	if m[3] != "" {
		if n, err := strconv.Atoi(m[3]); err == nil && n >= 1 && n <= 31 {
			day = strconv.Itoa(100 + n)[1:]
		}
	}
	return year + "-" + month + "-" + day
}

type TagCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func ClassifyTagsAndGenres(metaTags []string, userTags []TagCount) (genres []string, tags []string) {
	addTag := func(raw string) {
		name := strings.TrimSpace(raw)
		if name == "" || len([]rune(name)) > 24 {
			return
		}
		simp := ToSimplified(name)
		if genre, ok := knownGenres[simp]; ok {
			genres = appendUniqueText(genres, genre)
		}
		// Skip pure format/year noise tags in user tags if too generic
		switch simp {
		case "漫画", "コミックス", "comic", "manga", "单行本":
			return
		}
		tags = appendUniqueText(tags, name)
	}
	for _, mt := range metaTags {
		addTag(mt)
	}
	for _, ut := range userTags {
		if ut.Count < 2 && len(tags) >= 5 {
			continue
		}
		addTag(ut.Name)
		if len(tags) >= 15 {
			break
		}
	}
	return genres, tags
}
