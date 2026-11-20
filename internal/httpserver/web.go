package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func (s *Server) webStyles(w http.ResponseWriter, _ *http.Request) {
	data, err := staticFiles.ReadFile("static/web.css")
	if err != nil {
		http.Error(w, "stylesheet unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(data)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") ||
		strings.HasPrefix(r.URL.Path, "/admin/api/") ||
		r.URL.Path == "/api" || r.URL.Path == "/admin/api" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Endpoint not found.")
		return
	}
	s.renderNotFound(w)
}

func (s *Server) renderNotFound(w http.ResponseWriter) {
	s.renderWeb(w, http.StatusNotFound, "web_404.html", nil)
}

func (s *Server) renderWebError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		w.WriteHeader(499)
		return
	}
	s.logger.Error("render web page", "error", err)
	s.renderWeb(w, http.StatusInternalServerError, "web_error.html", nil)
}

func (s *Server) renderWeb(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self' 'unsafe-inline'")
	w.WriteHeader(status)
	if err := s.web.ExecuteTemplate(w, name, data); err != nil {
		s.logger.Error("execute web template", "template", name, "error", err)
	}
}

func webBookPath(series database.Series, book database.Book) string {
	if series.OneShot {
		return series.RelativePath
	}
	return path.Join(series.RelativePath, book.Name)
}

func pageProgressInline(progress database.BookPageProgress) string {
	if progress.BookID == "" || progress.PageCount <= 0 {
		return ""
	}
	return fmt.Sprintf(
		"推断进度：最近 %s，最大 %s",
		pageNumberText(progress.LastLoadedPage, progress.PageCount),
		pageNumberText(progress.MaxLoadedPage, progress.PageCount),
	)
}

func indexStatusInline(stats database.ArchiveIndexStats, currentVersion string) string {
	if stats.BookID == "" {
		return "索引：未建立"
	}
	if stats.Version != currentVersion {
		return "索引：旧版本"
	}
	if stats.HasDuration {
		return fmt.Sprintf("索引：%d 页，耗时 %s", stats.PageCount, formatDuration(stats.Duration))
	}
	return fmt.Sprintf("索引：%d 页", stats.PageCount)
}

func downloadStatInline(stats database.DownloadStats) string {
	if !stats.HasDownload {
		return ""
	}
	return "真实下载：" + formatSpeed(stats.Bytes, stats.Duration)
}

func seriesIndexSummary(stats database.SeriesPerformanceStats) string {
	base := fmt.Sprintf("当前版本已建立 %d / %d 本", stats.IndexedBooksCount, stats.BooksCount)
	if stats.IndexDurationCount > 0 {
		base += "，平均耗时 " + formatDuration(stats.IndexAverage)
	}
	if !stats.IndexLatestAt.IsZero() {
		base += "，最近完成 " + webTime(stats.IndexLatestAt)
	}
	return base
}

func seriesCoverSummary(stats database.SeriesPerformanceStats) string {
	if !stats.HasCoverDuration {
		return "尚无系列封面生成耗时统计"
	}
	out := "最近生成耗时 " + formatDuration(stats.CoverDuration)
	if !stats.CoverCompletedAt.IsZero() {
		out += "，完成于 " + webTime(stats.CoverCompletedAt)
	}
	return out
}

func seriesDownloadSummary(stats database.SeriesPerformanceStats) string {
	if !stats.HasDownload {
		return "尚无真实 Range 下载统计"
	}
	out := formatSpeed(stats.DownloadBytes, stats.DownloadDuration) +
		"（累计 " + formatBytes(stats.DownloadBytes) + "，" +
		strconv.FormatInt(stats.DownloadSamples, 10) + " 次 Range）"
	if !stats.DownloadLatestAt.IsZero() {
		out += "，最近 " + webTime(stats.DownloadLatestAt)
	}
	return out
}

func inferredProgressText(bookName string, pageNumber, pageCount int) string {
	return bookName + " · " + pageNumberText(pageNumber, pageCount)
}

func pageNumberText(pageNumber, pageCount int) string {
	if pageNumber <= 0 || pageCount <= 0 {
		return "尚无页码"
	}
	return fmt.Sprintf("第 %d / %d 页", pageNumber, pageCount)
}

func formatDuration(value time.Duration) string {
	if value <= 0 {
		return "0 ms"
	}
	if value < time.Second {
		ms := float64(value) / float64(time.Millisecond)
		if ms < 10 {
			return fmt.Sprintf("%.1f ms", ms)
		}
		return fmt.Sprintf("%.0f ms", ms)
	}
	if value < time.Minute {
		return fmt.Sprintf("%.2f s", float64(value)/float64(time.Second))
	}
	minutes := int(value / time.Minute)
	seconds := int((value % time.Minute) / time.Second)
	return fmt.Sprintf("%d 分 %d 秒", minutes, seconds)
}

func formatSpeed(bytes int64, duration time.Duration) string {
	if bytes <= 0 || duration <= 0 {
		return "尚无统计"
	}
	mbps := (float64(bytes) / 1024 / 1024) / duration.Seconds()
	return fmt.Sprintf("%.2f MB/s", mbps)
}

func webTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func webArchiveType(name string) string {
	extension := strings.TrimPrefix(path.Ext(name), ".")
	if extension == "" {
		return "漫画归档"
	}
	return strings.ToUpper(extension) + " 漫画归档"
}
