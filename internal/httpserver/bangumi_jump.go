package httpserver

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

func (s *Server) serveBangumiJump(w http.ResponseWriter, r *http.Request) {
	rawID := strings.TrimSpace(chi.URLParam(r, "subjectID"))
	subjectID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || subjectID <= 0 {
		s.renderNotFound(w)
		return
	}
	matches, err := s.store.LookupSeriesByBangumiSubject(r.Context(), subjectID)
	if err != nil {
		s.renderWebError(w, err)
		return
	}
	if len(matches) == 1 {
		http.Redirect(w, r, "/series/"+url.PathEscape(matches[0].SeriesID), http.StatusFound)
		return
	}
	if len(matches) > 1 {
		http.Redirect(w, r, "/series/"+url.PathEscape(matches[0].SeriesID)+"?versions=1", http.StatusFound)
		return
	}

	title := strings.TrimSpace(r.URL.Query().Get("title"))
	searchURL := "/libraries"
	if title != "" {
		searchURL = "/libraries?search=" + url.QueryEscape(title)
	}
	bgmURL := fmt.Sprintf("https://bgm.tv/subject/%d", subjectID)
	titleHTML := ""
	if title != "" {
		titleHTML = fmt.Sprintf(`<p style="margin:6px 0 0;color:var(--text-muted,#64748b);font-size:14px">条目标题：%s</p>`, html.EscapeString(title))
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>暂未收录 Bangumi #%d · MangaAlpha</title>
<link id="app-favicon" rel="icon" type="image/png" href="/ui/mangaalpha-icon.png">
<link rel="stylesheet" href="/ui/app.css">
<script src="/ui/theme-icon.js"></script>
</head>
<body style="display:grid;place-items:center;min-height:100vh;margin:0;padding:24px">
<main class="card" style="max-width:460px;width:100%%;text-align:center;padding:28px 24px">
  <h1 style="margin:0 0 10px;font-size:20px">本库暂未收录 Bangumi #%d</h1>
  %s
  <p style="margin:12px 0 20px;color:var(--text-muted,#64748b);font-size:13px;line-height:1.6">
    未在当前可见书库中找到绑定该 Bangumi 条目的作品（也可能尚未刮削，或处于隐藏/NSFW 筛选中）。
  </p>
  <div style="display:flex;flex-wrap:wrap;justify-content:center;gap:10px">
    <a class="btn btn-primary" href="%s">去书架搜索</a>
    <a class="btn" href="/">返回书架首页</a>
    <a class="btn" href="%s" target="_blank" rel="noopener noreferrer">返回 Bangumi ↗</a>
  </div>
</main>
</body>
</html>`, subjectID, subjectID, titleHTML, html.EscapeString(searchURL), html.EscapeString(bgmURL))
}

func (s *Server) komgaBangumiLookup(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("subject_id"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("subjectId"))
	}
	subjectID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || subjectID <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_SUBJECT_ID", "subject_id must be a positive integer.")
		return
	}
	matches, err := s.store.LookupSeriesByBangumiSubject(r.Context(), subjectID)
	if err != nil {
		s.writeDBError(w, r, err)
		return
	}
	jumpURL := fmt.Sprintf("/bgm/%d", subjectID)
	if len(matches) == 1 {
		jumpURL = matches[0].URL
	} else if len(matches) > 1 {
		jumpURL = matches[0].URL + "?versions=1"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subjectId": subjectID,
		"found":     len(matches) > 0,
		"count":     len(matches),
		"jumpUrl":   jumpURL,
		"matches":   matches,
	})
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); proto == "http" || proto == "https" {
		scheme = proto
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func escapeJSString(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`'`, `\'`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
		"<", `\u003c`,
		">", `\u003e`,
	)
	return replacer.Replace(s)
}

func (s *Server) downloadBangumiUserscript(w http.ResponseWriter, r *http.Request) {
	raw, err := staticFiles.ReadFile("static/mangaalpha-bangumi.user.js")
	if err != nil {
		http.Error(w, "userscript template unavailable", http.StatusInternalServerError)
		return
	}
	origin := requestOrigin(r)
	script := strings.ReplaceAll(string(raw), "__MANGAALPHA_SERVER_URL__", escapeJSString(origin))
	script = strings.ReplaceAll(script, "__MANGAALPHA_API_KEY__", "")

	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="mangaalpha-bangumi.user.js"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(script))
}
