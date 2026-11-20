package pipeline

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/archive"
	"github.com/xJogger/fake-komga-115/internal/bangumi"
	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
	"github.com/xJogger/fake-komga-115/internal/scanner"
	"github.com/xJogger/fake-komga-115/internal/thumbnail"
	"github.com/xJogger/fake-komga-115/internal/webdav"
)

func makeTinyCBZ(t *testing.T, dst string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.RGBA{100, 150, 200, 255})
	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, img)

	f, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("01.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(pngBuf.Bytes())
	_ = zw.Close()
	_ = f.Close()
}

func TestScheduleSlotDueLocalTime(t *testing.T) {
	loc := time.Local
	// Wednesday 2026-11-18 03:02:00 local
	now := time.Date(2026, 11, 18, 3, 2, 0, 0, loc)
	if now.Weekday() != time.Wednesday {
		t.Fatalf("expected Wednesday, got %v", now.Weekday())
	}

	daily := database.LibrarySchedule{
		LibraryID: "l1", Enabled: true, Frequency: "daily", TimeOfDay: "03:00",
	}
	slot, due := ScheduleSlotDue(daily, now)
	if !due || slot != "2026-11-18@03:00" {
		t.Fatalf("daily due=%v slot=%q", due, slot)
	}
	// Idempotent: same slot key must not re-fire
	daily.LastSlotKey = slot
	if _, due2 := ScheduleSlotDue(daily, now); due2 {
		t.Fatalf("expected same slot not to double-fire")
	}
	// Outside 10-minute window must not fire
	if _, dueLate := ScheduleSlotDue(database.LibrarySchedule{
		LibraryID: "l1", Enabled: true, Frequency: "daily", TimeOfDay: "03:00",
	}, time.Date(2026, 11, 18, 3, 15, 0, 0, loc)); dueLate {
		t.Fatalf("expected slot outside 10m window not to fire")
	}
	// Weekly: wrong weekday must not fire, matching Wednesday fires
	weeklyWrong := database.LibrarySchedule{
		LibraryID: "l1", Enabled: true, Frequency: "weekly", Weekday: int(time.Sunday), TimeOfDay: "03:00",
	}
	if _, ok := ScheduleSlotDue(weeklyWrong, now); ok {
		t.Fatalf("weekly wrong weekday should not fire")
	}
	weeklyOK := database.LibrarySchedule{
		LibraryID: "l1", Enabled: true, Frequency: "weekly", Weekday: int(time.Wednesday), TimeOfDay: "03:00",
	}
	wSlot, ok := ScheduleSlotDue(weeklyOK, now)
	if !ok || !strings.Contains(wSlot, "-w3@03:00") {
		t.Fatalf("weekly matching weekday due=%v slot=%q", ok, wSlot)
	}
}

func TestPipelineSkipWithoutBangumiOfflineDBAbortOnEmptyMountAndUnionSelectedMembers(t *testing.T) {
	tmp := t.TempDir()
	topDir := filepath.Join(tmp, "TopLocal")
	bottomDir := filepath.Join(tmp, "BottomBackup")
	makeTinyCBZ(t, filepath.Join(topDir, "Manga1", "Vol 01.cbz"))
	makeTinyCBZ(t, filepath.Join(bottomDir, "Manga1", "Vol 02.cbz"))

	store, err := database.Open(filepath.Join(tmp, "pipe.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	if err := store.UpsertLibrary(ctx, database.Library{
		ID: "lib_top", Name: "本地上层", ProviderType: database.ProviderLocal,
		RootCID: "local:" + topDir, RootPath: topDir, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, database.Library{
		ID: "lib_bot", Name: "备份下层", ProviderType: database.ProviderLocal,
		RootCID: "local:" + bottomDir, RootPath: bottomDir, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	client := oneonefive.New(store, logger)
	wdMgr := webdav.NewManager()
	scanMgr := scanner.NewWithWebDAV(store, client, wdMgr, logger)
	defer scanMgr.Close()
	cacheMgr, err := cache.New(store, filepath.Join(tmp, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	archSvc := archive.NewServiceWithWebDAV(store, client, wdMgr, cacheMgr, logger)
	defer archSvc.Close()
	thumbSvc, err := thumbnail.New(store, filepath.Join(tmp, "thumbs", "series"), logger)
	if err != nil {
		t.Fatal(err)
	}
	coverMgr := thumbnail.NewBatchManager(store, archSvc, thumbSvc, logger)
	defer coverMgr.Close()
	bgmSvc, err := bangumi.NewService(store, filepath.Join(tmp, "bangumi"), logger)
	if err != nil {
		t.Fatal(err)
	}
	defer bgmSvc.Close()

	pipeMgr := New(store, scanMgr, bgmSvc, coverMgr, logger)
	pipeMgr.pollInterval = 25 * time.Millisecond
	defer pipeMgr.Close()

	waitPipe := func(runID string) database.PipelineRun {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			cur, err := pipeMgr.Run(ctx, runID)
			if err != nil {
				t.Fatal(err)
			}
			if cur.Status != "queued" && cur.Status != "running" {
				return cur
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatalf("pipeline %s timed out", runID)
		return database.PipelineRun{}
	}

	// 1. Standalone physical library with AutoBangumi=true & AutoCoverLimit=5, but NO Bangumi offline DB:
	// must complete scan, then SKIP both Bangumi scrape and cover generation!
	if _, err := store.UpsertLibrarySchedule(ctx, database.LibrarySchedule{
		LibraryID: "lib_top", Enabled: true, Frequency: "daily", TimeOfDay: "03:00",
		AutoBangumi: true, AutoCoverLimit: 5,
	}); err != nil {
		t.Fatal(err)
	}
	run1, err := pipeMgr.Start(ctx, "lib_top", "manual")
	if err != nil {
		t.Fatal(err)
	}
	done1 := waitPipe(run1.ID)
	if done1.Status != "success" || done1.BangumiRunID != "" || done1.CoverRunID != "" || !strings.Contains(done1.Summary, "跳过") {
		t.Fatalf("expected success skipping bangumi+cover without offline DB, got %+v", done1)
	}
	if st, _ := thumbSvc.Stats(ctx); st.Files != 0 {
		t.Fatalf("expected 0 covers generated when Bangumi offline DB is absent, got %d", st.Files)
	}

	// 2. Empty mount protection: wipe topDir entries -> pipeline scan fails -> pipeline aborts cleanly
	if err := os.RemoveAll(filepath.Join(topDir, "Manga1")); err != nil {
		t.Fatal(err)
	}
	run2, err := pipeMgr.Start(ctx, "lib_top", "manual")
	if err != nil {
		t.Fatal(err)
	}
	done2 := waitPipe(run2.ID)
	if done2.Status != "failed" || done2.Stage != "scan" || !strings.Contains(done2.Error, "empty") {
		t.Fatalf("expected pipeline abort on empty-mount scan failure, got %+v", done2)
	}

	// Restore topDir + scan bottomDir once, then create Union Library selecting ONLY lib_top in schedule!
	makeTinyCBZ(t, filepath.Join(topDir, "Manga1", "Vol 01.cbz"))
	makeTinyCBZ(t, filepath.Join(topDir, "Manga1", "Vol 03.cbz"))
	if sRun, err := scanMgr.StartLibrary(ctx, "lib_bot", "manual"); err != nil {
		t.Fatal(err)
	} else {
		for {
			cur, _ := scanMgr.Run(ctx, sRun.ID)
			if cur.Status == "success" {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
	}

	unionLib, _, err := store.CreateUnionLibrary(ctx, "主联合库", true, []string{"lib_top", "lib_bot"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertLibrarySchedule(ctx, database.LibrarySchedule{
		LibraryID: unionLib.ID, Enabled: true, Frequency: "daily", TimeOfDay: "04:00",
		ScanMemberLibraryIDs: []string{"lib_top"}, // ONLY scan top local layer!
		AutoBangumi:          true,
		AutoCoverLimit:       0,
	}); err != nil {
		t.Fatal(err)
	}

	run3, err := pipeMgr.Start(ctx, unionLib.ID, "scheduled")
	if err != nil {
		t.Fatal(err)
	}
	done3 := waitPipe(run3.ID)
	if done3.Status != "success" || len(done3.ScanRunIDs) != 1 {
		t.Fatalf("expected union pipeline scanning ONLY 1 selected sub-library, got %+v", done3)
	}
	// Verify Vol 03 from topDir + Vol 02 from bottomDir + Vol 01 from topDir are all present (3 vols total!)
	uBooks, total, err := store.BooksPage(ctx, database.BookQuery{LibraryIDs: []string{unionLib.ID}, Unpaged: true})
	if err != nil || total != 3 || len(uBooks) != 3 {
		t.Fatalf("expected 3 books in union library after pipeline refresh, err=%v total=%d len=%d", err, total, len(uBooks))
	}
}
