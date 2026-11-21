package bangumi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func buildMockArchiveZip(t *testing.T, subjectJSONLines string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		"subject.jsonlines":           subjectJSONLines,
		"subject-relations.jsonlines": "",
		"subject-persons.jsonlines":   "",
		"person.jsonlines":            "",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	data := buf.Bytes()
	sum := sha256.Sum256(data)
	return data, "sha256:" + hex.EncodeToString(sum[:])
}

func TestArchiveScheduleValidationAndDueSlot(t *testing.T) {
	if _, err := ValidateArchiveSchedule(ArchiveScheduleUpdate{Enabled: true, Frequency: "daily", TimeOfDay: "06:00"}); err == nil {
		t.Fatal("expected error for unsupported frequency")
	}
	if _, err := ValidateArchiveSchedule(ArchiveScheduleUpdate{Enabled: true, Frequency: "monthly", DayOfMonth: 31, TimeOfDay: "06:00"}); err == nil {
		t.Fatal("expected error for dayOfMonth > 28")
	}
	sched, err := ValidateArchiveSchedule(ArchiveScheduleUpdate{
		Enabled: true, Frequency: "weekly", DayOfWeek: int(time.Wednesday), TimeOfDay: "06:00",
	})
	if err != nil {
		t.Fatalf("ValidateArchiveSchedule: %v", err)
	}
	// Wednesday 2026-11-18 06:05 local time should be due within 15-minute grace window.
	wedDue := time.Date(2026, 11, 18, 6, 5, 0, 0, time.Local)
	slot, ok := archiveScheduleDueSlot(sched, wedDue)
	if !ok || slot != "2026-11-18T06:00" {
		t.Fatalf("due=(%v,%q), want (true, 2026-11-18T06:00)", ok, slot)
	}
	// Repeating the same slot should not fire twice.
	sched.LastSlot = slot
	if _, ok := archiveScheduleDueSlot(sched, wedDue); ok {
		t.Fatal("expected already-executed slot not to fire again")
	}
	next, ok := NextArchiveRunAt(sched, wedDue)
	if !ok || next.Weekday() != time.Wednesday || !next.After(wedDue) {
		t.Fatalf("unexpected next run: %v (%v)", next, ok)
	}
}

func TestArchiveSkipUnchangedAndAutoIncrementalMetadataSync(t *testing.T) {
	dir := t.TempDir()
	store, err := database.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	// Seed two series bound to Bangumi subject 1001:
	// ser_unlocked has no locks; ser_locked locks title & summary.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.DB().Exec(`INSERT INTO libraries(id,name,provider_type,root_cid,root_path,enabled,one_shot,created_at,updated_at) VALUES('lib1','Lib','local','local:/tmp','/tmp',1,0,?,?)`, now, now); err != nil {
		t.Fatalf("insert lib: %v", err)
	}
	if _, err := store.DB().Exec(`INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,books_count,created_at,updated_at,seen_scan_id) VALUES('ser_unlocked','lib1','s1','OldManga','OldManga',0,1,?,?,'scan1')`, now, now); err != nil {
		t.Fatalf("insert ser_unlocked: %v", err)
	}
	if _, err := store.DB().Exec(`INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,books_count,created_at,updated_at,seen_scan_id) VALUES('ser_locked','lib1','s2','LockedManga','LockedManga',0,1,?,?,'scan1')`, now, now); err != nil {
		t.Fatalf("insert ser_locked: %v", err)
	}

	v1Zip, v1Digest := buildMockArchiveZip(t,
		`{"id":1001,"type":1,"platform":1001,"name":"TestJP","name_cn":"测试漫画V1","summary":"初始简介","infobox":"","nsfw":false,"tags":[{"name":"热血","count":50}],"meta_tags":["漫画"],"score":7.5,"rank":500,"date":"2020-01-01","series":true}`+"\n",
	)
	v2Zip, v2Digest := buildMockArchiveZip(t,
		// V2 updates name_cn, score, rank, status(完结 via tags/summary), leaves summary empty on 1002 test, updates 1001 summary/score/rank/nsfw.
		`{"id":1001,"type":1,"platform":1001,"name":"TestJP","name_cn":"测试漫画V2","summary":"更新后的中文简介","infobox":"{{Infobox animanga/Manga\n|出版社=集英社\n|册数=12\n}}","nsfw":true,"tags":[{"name":"冒险","count":90}],"meta_tags":["漫画"],"score":8.8,"rank":120,"date":"2020-01-01","series":true}`+"\n",
	)

	var currentDump atomic.Value
	type dumpServing struct {
		name   string
		digest string
		data   []byte
	}
	currentDump.Store(dumpServing{name: "dump-v1.zip", digest: v1Digest, data: v1Zip})
	var zipDownloads atomic.Int32

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := currentDump.Load().(dumpServing)
		switch r.URL.Path {
		case "/latest.json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":                 cur.name,
				"browser_download_url": srv.URL + "/download.zip",
				"created_at":           "2026-11-18T00:00:00Z",
				"digest":               cur.digest,
				"size":                 len(cur.data),
			})
		case "/download.zip":
			zipDownloads.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(cur.data)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	svc, err := NewService(store, filepath.Join(dir, "bgm"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()
	svc.archive.metaURL = srv.URL + "/latest.json"

	// Bind both series with V1 metadata first (with ser_locked locking title & summary).
	serUnlocked, _ := store.SeriesByID(context.Background(), "ser_unlocked")
	serLocked, _ := store.SeriesByID(context.Background(), "ser_locked")
	score75 := 7.5
	initScraped := database.ScrapedMetadata{
		SubjectID: 1001, Confidence: 1, Score: &score75, Rank: 500,
		Status: "ONGOING", Title: "测试漫画V1", TitleSort: "测试漫画V1", Summary: "初始简介",
		Tags: []string{"热血"}, Genres: []string{"漫画"},
	}
	if _, err := store.ApplyScrapedMetadata(context.Background(), serUnlocked, initScraped); err != nil {
		t.Fatalf("ApplyScrapedMetadata unlocked: %v", err)
	}
	if _, err := store.ApplyScrapedMetadata(context.Background(), serLocked, initScraped); err != nil {
		t.Fatalf("ApplyScrapedMetadata locked: %v", err)
	}
	trueVal := true
	customTitle := "我的自定义标题"
	customSummary := "我的手动简介"
	if _, err := store.PatchSeriesMetadata(context.Background(), serLocked, database.SeriesMetadataPatch{
		Title:       &customTitle,
		TitleLock:   &trueVal,
		Summary:     &customSummary,
		SummaryLock: &trueVal,
	}); err != nil {
		t.Fatalf("PatchSeriesMetadata locked: %v", err)
	}

	waitArchiveDone := func() ArchiveStatus {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			st := svc.ArchiveStatus()
			if !st.Updating {
				return st
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("timed out waiting for archive update")
		return ArchiveStatus{}
	}

	// 1. Download V1 archive.
	if _, err := svc.StartArchiveUpdateWithOptions(false); err != nil {
		t.Fatalf("StartArchiveUpdate v1: %v", err)
	}
	st := waitArchiveDone()
	if !st.Ready || st.DumpName != "dump-v1.zip" || zipDownloads.Load() != 1 {
		t.Fatalf("v1 status unexpected: %+v downloads=%d", st, zipDownloads.Load())
	}

	// 2. Re-check with identical V1 dump -> must skip downloading zip!
	if _, err := svc.StartArchiveUpdateWithOptions(false); err != nil {
		t.Fatalf("StartArchiveUpdate repeat v1: %v", err)
	}
	st = waitArchiveDone()
	if st.LastCheckResult != "up_to_date" || zipDownloads.Load() != 1 {
		t.Fatalf("expected up_to_date skip with 1 total download, got status=%+v downloads=%d", st, zipDownloads.Load())
	}

	// 3. Publish V2 dump -> must download, import, and auto-sync unlocked fields while respecting locks!
	currentDump.Store(dumpServing{name: "dump-v2.zip", digest: v2Digest, data: v2Zip})
	if _, err := svc.StartArchiveUpdateWithOptions(false); err != nil {
		t.Fatalf("StartArchiveUpdate v2: %v", err)
	}
	st = waitArchiveDone()
	if st.DumpName != "dump-v2.zip" || zipDownloads.Load() != 2 {
		t.Fatalf("v2 status unexpected: %+v downloads=%d", st, zipDownloads.Load())
	}

	// Wait briefly for async rankWorker/syncOfflineMetadata to commit V2 sync.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if svc.ArchiveStatus().LastSyncDump == "dump-v2.zip" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if svc.ArchiveStatus().LastSyncDump != "dump-v2.zip" {
		t.Fatalf("expected auto metadata sync for dump-v2.zip, got %+v", svc.ArchiveStatus())
	}

	metaU, err := store.SeriesMetadataByID(context.Background(), serUnlocked)
	if err != nil {
		t.Fatalf("SeriesMetadataByID unlocked: %v", err)
	}
	if metaU.Title != "测试漫画V2" || metaU.Summary != "更新后的中文简介" || metaU.Publisher != "集英社" ||
		metaU.BgmRank == nil || *metaU.BgmRank != 120 || metaU.BgmScore == nil || *metaU.BgmScore != 8.8 || !metaU.IsNSFW {
		t.Fatalf("unlocked metadata was not auto-synced cleanly: %+v", metaU)
	}

	metaL, err := store.SeriesMetadataByID(context.Background(), serLocked)
	if err != nil {
		t.Fatalf("SeriesMetadataByID locked: %v", err)
	}
	if metaL.Title != customTitle || metaL.Summary != customSummary {
		t.Fatalf("locked fields were overwritten! title=%q summary=%q", metaL.Title, metaL.Summary)
	}
	if metaL.BgmRank == nil || *metaL.BgmRank != 120 || metaL.Publisher != "集英社" {
		t.Fatalf("unlocked fields on locked series were not updated: %+v", metaL)
	}

	// 4. Verify non-empty guard: importing a dump with empty summary must NOT wipe existing summary.
	v3Zip, v3Digest := buildMockArchiveZip(t,
		`{"id":1001,"type":1,"platform":1001,"name":"TestJP","name_cn":"测试漫画V2","summary":"","infobox":"","nsfw":true,"tags":[],"meta_tags":[],"score":8.9,"rank":110,"date":"","series":true}`+"\n",
	)
	currentDump.Store(dumpServing{name: "dump-v3.zip", digest: v3Digest, data: v3Zip})
	if _, err := svc.StartArchiveUpdateWithOptions(false); err != nil {
		t.Fatalf("StartArchiveUpdate v3: %v", err)
	}
	waitArchiveDone()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if svc.ArchiveStatus().LastSyncDump == "dump-v3.zip" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	metaU3, _ := store.SeriesMetadataByID(context.Background(), serUnlocked)
	if metaU3.Summary != "更新后的中文简介" || metaU3.Publisher != "集英社" || len(metaU3.Tags) == 0 {
		t.Fatalf("empty fields in v3 wiped existing metadata: %+v", metaU3)
	}
	if metaU3.BgmRank == nil || *metaU3.BgmRank != 110 {
		t.Fatalf("rank did not update in v3: %+v", metaU3.BgmRank)
	}

	// 5. Idempotency check: forcing a repeat sync against the exact same dump must write 0 rows.
	repeatUpdated, err := svc.syncOfflineMetadata(context.Background(), true)
	if err != nil {
		t.Fatalf("repeat syncOfflineMetadata: %v", err)
	}
	if repeatUpdated != 0 {
		t.Fatalf("expected 0 updated series on idempotent repeat sync, got %d", repeatUpdated)
	}
}
