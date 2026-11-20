package database

import (
	"strings"
	"unicode"
)

// searchExpr represents a parsed Komga / Koharia search expression.
type searchExpr interface {
	isSearchExpr()
}

type searchOr struct {
	items []searchExpr
}

func (searchOr) isSearchExpr() {}

type searchAnd struct {
	items []searchExpr
}

func (searchAnd) isSearchExpr() {}

type searchNot struct {
	inner searchExpr
}

func (searchNot) isSearchExpr() {}

type searchTerm struct {
	field string // lowercase field prefix or "" for general full-text
	value string
}

func (searchTerm) isSearchExpr() {}

type searchToken struct {
	kind  string // "WORD", "LPAREN", "RPAREN", "AND", "OR", "NOT", "FIELD"
	value string
}

var knownSearchFields = map[string]bool{
	"title": true, "tag": true, "series_tag": true, "book_tag": true,
	"author": true, "writer": true, "penciller": true, "inker": true,
	"colorist": true, "letterer": true, "cover": true, "editor": true,
	"translator": true, "artist": true, "illustrator": true, "conceptor": true,
	"narrator": true, "contributor": true, "publisher": true, "genre": true,
	"language": true, "status": true,
}

func tokenizeSearch(input string) []searchToken {
	var tokens []searchToken
	runes := []rune(strings.TrimSpace(input))
	n := len(runes)
	for i := 0; i < n; {
		r := runes[i]
		if unicode.IsSpace(r) {
			i++
			continue
		}
		if r == '(' {
			tokens = append(tokens, searchToken{kind: "LPAREN", value: "("})
			i++
			continue
		}
		if r == ')' {
			tokens = append(tokens, searchToken{kind: "RPAREN", value: ")"})
			i++
			continue
		}
		if r == '"' {
			i++
			start := i
			for i < n && runes[i] != '"' {
				i++
			}
			val := strings.TrimSpace(string(runes[start:i]))
			if i < n && runes[i] == '"' {
				i++
			}
			if val != "" {
				tokens = append(tokens, searchToken{kind: "WORD", value: val})
			}
			continue
		}
		start := i
		for i < n && !unicode.IsSpace(runes[i]) && runes[i] != '(' && runes[i] != ')' && runes[i] != '"' {
			i++
		}
		word := string(runes[start:i])
		if colon := strings.IndexByte(word, ':'); colon > 0 {
			prefix := strings.ToLower(word[:colon])
			if knownSearchFields[prefix] {
				rest := word[colon+1:]
				tokens = append(tokens, searchToken{kind: "FIELD", value: prefix})
				if rest != "" {
					tokens = append(tokens, searchToken{kind: "WORD", value: rest})
				}
				continue
			}
		}
		switch word {
		case "OR":
			tokens = append(tokens, searchToken{kind: "OR", value: "OR"})
		case "AND":
			tokens = append(tokens, searchToken{kind: "AND", value: "AND"})
		case "NOT":
			tokens = append(tokens, searchToken{kind: "NOT", value: "NOT"})
		default:
			if strings.HasPrefix(word, "-") && len(word) > 1 {
				tokens = append(tokens, searchToken{kind: "NOT", value: "NOT"})
				tokens = append(tokens, searchToken{kind: "WORD", value: word[1:]})
			} else {
				tokens = append(tokens, searchToken{kind: "WORD", value: word})
			}
		}
	}
	return tokens
}

type searchParser struct {
	tokens []searchToken
	pos    int
}

func parseSearchQuery(raw string) searchExpr {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	tokens := tokenizeSearch(raw)
	if len(tokens) == 0 {
		return searchTerm{value: raw}
	}
	p := &searchParser{tokens: tokens}
	expr := p.parseOr("")
	if expr == nil {
		return searchTerm{value: raw}
	}
	return expr
}

func (p *searchParser) peek() (searchToken, bool) {
	if p.pos >= len(p.tokens) {
		return searchToken{}, false
	}
	return p.tokens[p.pos], true
}

func (p *searchParser) next() (searchToken, bool) {
	tok, ok := p.peek()
	if ok {
		p.pos++
	}
	return tok, ok
}

func (p *searchParser) parseOr(defaultField string) searchExpr {
	left := p.parseAnd(defaultField)
	if left == nil {
		return nil
	}
	items := []searchExpr{left}
	for {
		tok, ok := p.peek()
		if !ok || tok.kind != "OR" {
			break
		}
		p.next()
		right := p.parseAnd(defaultField)
		if right != nil {
			items = append(items, right)
		}
	}
	if len(items) == 1 {
		return items[0]
	}
	return searchOr{items: items}
}

func (p *searchParser) parseAnd(defaultField string) searchExpr {
	var items []searchExpr
	for {
		tok, ok := p.peek()
		if !ok || tok.kind == "RPAREN" || tok.kind == "OR" {
			break
		}
		if tok.kind == "AND" {
			p.next()
			continue
		}
		atom := p.parseUnary(defaultField)
		if atom == nil {
			break
		}
		items = append(items, atom)
	}
	if len(items) == 0 {
		return nil
	}
	if len(items) == 1 {
		return items[0]
	}
	return searchAnd{items: items}
}

func (p *searchParser) parseUnary(defaultField string) searchExpr {
	tok, ok := p.peek()
	if !ok {
		return nil
	}
	if tok.kind == "NOT" {
		p.next()
		inner := p.parseUnary(defaultField)
		if inner == nil {
			return nil
		}
		return searchNot{inner: inner}
	}
	if tok.kind == "FIELD" {
		p.next()
		field := tok.value
		nextTok, ok := p.peek()
		if !ok {
			return searchTerm{field: defaultField, value: field + ":"}
		}
		if nextTok.kind == "LPAREN" {
			p.next()
			inner := p.parseOr(field)
			if closeTok, ok := p.peek(); ok && closeTok.kind == "RPAREN" {
				p.next()
			}
			return inner
		}
		if nextTok.kind == "WORD" {
			p.next()
			return searchTerm{field: field, value: nextTok.value}
		}
		return nil
	}
	if tok.kind == "LPAREN" {
		p.next()
		inner := p.parseOr(defaultField)
		if closeTok, ok := p.peek(); ok && closeTok.kind == "RPAREN" {
			p.next()
		}
		return inner
	}
	if tok.kind == "WORD" {
		p.next()
		return searchTerm{field: defaultField, value: tok.value}
	}
	p.next()
	return nil
}

// extractPrimarySearchPhrase extracts the clean user phrase (e.g. from "(排球) OR tag:(排球)" -> "排球")
// for relevance ranking and single-query optimization.
func extractPrimarySearchPhrase(raw string) string {
	expr := parseSearchQuery(raw)
	if expr == nil {
		return ""
	}
	var parts []string
	var collect func(e searchExpr)
	collect = func(e searchExpr) {
		switch node := e.(type) {
		case searchTerm:
			if node.field == "" || node.field == "title" {
				parts = append(parts, node.value)
			}
		case searchAnd:
			for _, child := range node.items {
				collect(child)
			}
		case searchOr:
			if len(node.items) > 0 {
				collect(node.items[0])
			}
		}
	}
	collect(expr)
	if len(parts) == 0 {
		return strings.TrimSpace(raw)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// buildSeriesSearchSQL builds a parameterized SQL condition for series table alias "s".
func buildSeriesSearchSQL(raw string) (string, []any) {
	expr := parseSearchQuery(raw)
	if expr == nil {
		return "", nil
	}
	// Fast path for Koharia's exact "(q) OR tag:(q)" pattern (possibly multi-word inside parens).
	var args []any
	sql := renderSeriesSearchExpr(expr, &args)
	if sql == "" {
		return "", nil
	}
	return "(" + sql + ")", args
}

func renderSeriesSearchExpr(expr searchExpr, args *[]any) string {
	switch node := expr.(type) {
	case searchOr:
		parts := make([]string, 0, len(node.items))
		for _, item := range node.items {
			if sub := renderSeriesSearchExpr(item, args); sub != "" {
				parts = append(parts, sub)
			}
		}
		if len(parts) == 0 {
			return ""
		}
		return "(" + strings.Join(parts, " OR ") + ")"
	case searchAnd:
		parts := make([]string, 0, len(node.items))
		for _, item := range node.items {
			if sub := renderSeriesSearchExpr(item, args); sub != "" {
				parts = append(parts, sub)
			}
		}
		if len(parts) == 0 {
			return ""
		}
		return "(" + strings.Join(parts, " AND ") + ")"
	case searchNot:
		sub := renderSeriesSearchExpr(node.inner, args)
		if sub == "" {
			return ""
		}
		return "(NOT " + sub + ")"
	case searchTerm:
		val := strings.TrimSpace(node.value)
		if val == "" {
			return ""
		}
		like := "%" + escapeLike(val) + "%"
		switch node.field {
		case "tag", "series_tag", "book_tag":
			*args = append(*args, like)
			return `EXISTS (SELECT 1 FROM series_tags st WHERE st.series_id=s.id AND st.tag LIKE ? ESCAPE '\' COLLATE NOCASE)`
		case "genre":
			*args = append(*args, like)
			return `EXISTS (SELECT 1 FROM series_genres sg WHERE sg.series_id=s.id AND sg.genre LIKE ? ESCAPE '\' COLLATE NOCASE)`
		case "publisher":
			*args = append(*args, like)
			return `EXISTS (SELECT 1 FROM series_metadata sm WHERE sm.series_id=s.id AND sm.publisher LIKE ? ESCAPE '\' COLLATE NOCASE)`
		case "language":
			*args = append(*args, val)
			return `EXISTS (SELECT 1 FROM series_metadata sm WHERE sm.series_id=s.id AND sm.language=? COLLATE NOCASE)`
		case "status":
			*args = append(*args, strings.ToUpper(val))
			return `EXISTS (SELECT 1 FROM series_metadata sm WHERE sm.series_id=s.id AND sm.status=?)`
		case "author":
			*args = append(*args, like)
			return `EXISTS (SELECT 1 FROM series_authors sa WHERE sa.series_id=s.id AND sa.name LIKE ? ESCAPE '\' COLLATE NOCASE)`
		case "writer", "penciller", "inker", "colorist", "letterer", "cover", "editor", "translator", "artist", "illustrator", "conceptor", "narrator", "contributor":
			*args = append(*args, like, node.field)
			return `EXISTS (SELECT 1 FROM series_authors sa WHERE sa.series_id=s.id AND sa.name LIKE ? ESCAPE '\' COLLATE NOCASE AND sa.role=? COLLATE NOCASE)`
		case "title":
			*args = append(*args, like, like, like)
			return `(s.name LIKE ? ESCAPE '\' COLLATE NOCASE OR EXISTS (SELECT 1 FROM series_metadata sm WHERE sm.series_id=s.id AND (sm.title LIKE ? ESCAPE '\' COLLATE NOCASE OR sm.alternate_titles_json LIKE ? ESCAPE '\' COLLATE NOCASE)))`
		default:
			*args = append(*args, like, like, like)
			return `(s.name LIKE ? ESCAPE '\' COLLATE NOCASE OR EXISTS (SELECT 1 FROM series_metadata sm WHERE sm.series_id=s.id AND (sm.title LIKE ? ESCAPE '\' COLLATE NOCASE OR sm.alternate_titles_json LIKE ? ESCAPE '\' COLLATE NOCASE)))`
		}
	}
	return ""
}

// buildBookSearchSQL builds a parameterized SQL condition for books table alias "b".
func buildBookSearchSQL(raw string) (string, []any) {
	expr := parseSearchQuery(raw)
	if expr == nil {
		return "", nil
	}
	var args []any
	sql := renderBookSearchExpr(expr, &args)
	if sql == "" {
		return "", nil
	}
	return "(" + sql + ")", args
}

func renderBookSearchExpr(expr searchExpr, args *[]any) string {
	switch node := expr.(type) {
	case searchOr:
		parts := make([]string, 0, len(node.items))
		for _, item := range node.items {
			if sub := renderBookSearchExpr(item, args); sub != "" {
				parts = append(parts, sub)
			}
		}
		if len(parts) == 0 {
			return ""
		}
		return "(" + strings.Join(parts, " OR ") + ")"
	case searchAnd:
		parts := make([]string, 0, len(node.items))
		for _, item := range node.items {
			if sub := renderBookSearchExpr(item, args); sub != "" {
				parts = append(parts, sub)
			}
		}
		if len(parts) == 0 {
			return ""
		}
		return "(" + strings.Join(parts, " AND ") + ")"
	case searchNot:
		sub := renderBookSearchExpr(node.inner, args)
		if sub == "" {
			return ""
		}
		return "(NOT " + sub + ")"
	case searchTerm:
		val := strings.TrimSpace(node.value)
		if val == "" {
			return ""
		}
		like := "%" + escapeLike(val) + "%"
		switch node.field {
		case "tag", "series_tag", "book_tag":
			*args = append(*args, like)
			return `b.series_id IN (SELECT st.series_id FROM series_tags st WHERE st.tag LIKE ? ESCAPE '\' COLLATE NOCASE)`
		case "genre":
			*args = append(*args, like)
			return `b.series_id IN (SELECT sg.series_id FROM series_genres sg WHERE sg.genre LIKE ? ESCAPE '\' COLLATE NOCASE)`
		case "publisher":
			*args = append(*args, like)
			return `b.series_id IN (SELECT sm.series_id FROM series_metadata sm WHERE sm.publisher LIKE ? ESCAPE '\' COLLATE NOCASE)`
		case "author":
			*args = append(*args, like)
			return `b.series_id IN (SELECT sa.series_id FROM series_authors sa WHERE sa.name LIKE ? ESCAPE '\' COLLATE NOCASE)`
		case "writer", "penciller", "inker", "colorist", "letterer", "cover", "editor", "translator", "artist", "illustrator", "conceptor", "narrator", "contributor":
			*args = append(*args, like, node.field)
			return `b.series_id IN (SELECT sa.series_id FROM series_authors sa WHERE sa.name LIKE ? ESCAPE '\' COLLATE NOCASE AND sa.role=? COLLATE NOCASE)`
		default:
			*args = append(*args, like, like, like, like)
			return `(b.name LIKE ? ESCAPE '\' COLLATE NOCASE OR b.series_id IN (SELECT s2.id FROM series s2 WHERE s2.name LIKE ? ESCAPE '\' COLLATE NOCASE) OR b.series_id IN (SELECT sm.series_id FROM series_metadata sm WHERE sm.title LIKE ? ESCAPE '\' COLLATE NOCASE OR sm.alternate_titles_json LIKE ? ESCAPE '\' COLLATE NOCASE))`
		}
	}
	return ""
}
