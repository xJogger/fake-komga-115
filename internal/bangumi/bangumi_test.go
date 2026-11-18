package bangumi

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/thumbnail"
)

func TestCJKTableAlignmentAndVariants(t *testing.T) {
	if len([]rune(tradRunes)) != len([]rune(simpRunes)) {
		t.Fatalf("tradRunes (%d) != simpRunes (%d)", len([]rune(tradRunes)), len([]rune(simpRunes)))
	}
	got := ToSimplified("妳的一週間 禪院 臺灣 姊妹 菓子 亂鬪 貓耳 姫様 隣の怪物")
	want := "你的一周间 禅院 台湾 姐妹 果子 乱斗 猫耳 姬样 邻の怪物"
	if got != want {
		t.Fatalf("ToSimplified = %q, want %q", got, want)
	}
}

func TestParseSeriesNameRealWorldPatterns(t *testing.T) {
	cases := []struct {
		raw        string
		relPath    string
		wantTitle  string
		wantAuthor string
		wantPub    string
		wantSuffix string
	}{
		{
			raw:        "[灌籃高手][井上雄彥][東立][31卷完]",
			wantTitle:  "灌篮高手",
			wantAuthor: "井上雄彦",
			wantPub:    "东立",
		},
		{
			raw:        "[浦澤直樹][20世紀少年][東立][22完]",
			wantTitle:  "20世纪少年",
			wantAuthor: "浦泽直树",
			wantPub:    "东立",
		},
		{
			raw:        "a [清原紘][Another][角川][4完]",
			wantTitle:  "Another",
			wantAuthor: "清原纮",
			wantPub:    "角川",
		},
		{
			raw:        "[鐮池和馬x冬川基][某科學的超電磁砲][角川]",
			wantTitle:  "某科学的超电磁炮",
			wantAuthor: "镰池和马",
			wantPub:    "角川",
		},
		{
			raw:       "獵魔戰記（大劍）",
			wantTitle: "大剑",
		},
		{
			raw:       "ARIA 水星領航員",
			wantTitle: "水星领航员",
		},
		{
			raw:        "台版",
			relPath:    "漫画库/[天野梢][水星領航員]/台版",
			wantTitle:  "水星领航员",
			wantAuthor: "天野梢",
			wantSuffix: "台版",
		},
		{
			raw:        "(一般コミック) [山田鐘人×阿部司] 葬送のフリーレン 第01-13巻 [小学館]",
			wantTitle:  "葬送のフリーレン",
			wantAuthor: "山田钟人",
			wantPub:    "小学馆",
		},
		{
			raw:        "[玉皇朝] 海賊王_ONE PIECE [尾田榮一郎] Vol.01-108 (未完)",
			wantTitle:  "海贼王",
			wantAuthor: "尾田荣一郎",
			wantPub:    "玉皇朝",
		},
		{
			raw:        "005. 孤独摇滚！ [滨路晶] [芳文社] [高清扫图]",
			wantTitle:  "孤独摇滚!",
			wantAuthor: "滨路晶",
			wantPub:    "芳文社",
		},
		{
			raw:       "2.5次元旅记",
			wantTitle: "2.5次元旅记",
		},
		{
			raw:        "[機械旅團][高橋明][文化傳信]7+1完",
			wantTitle:  "机械旅团",
			wantAuthor: "高桥明",
			wantPub:    "文化传信",
		},
		{
			raw:        "[嚙合偵探][松井優作][東立][ZERO][23完]",
			wantTitle:  "啮合侦探",
			wantAuthor: "松井优作",
			wantPub:    "东立",
		},
		{
			raw:        "[貓車站CAT'S STATION完全版][北村穗][玉皇朝]15完",
			wantTitle:  "CAT'S STATION",
			wantAuthor: "北村穗",
			wantPub:    "玉皇朝",
		},
		{
			raw:        "[暮色航線&DUSK LINE][冨田優][東立][3完]",
			wantTitle:  "暮色航线&DUSK LINE",
			wantAuthor: "富田优",
			wantPub:    "东立",
		},
		{
			raw:        "[林檎x茶葉][宮川美穗][尖端][ZERO][22完]",
			wantTitle:  "林檎x茶叶",
			wantAuthor: "宫川美穗",
			wantPub:    "尖端",
		},
	}
	for _, tc := range cases {
		parsed := ParseSeriesWithPath(tc.raw, tc.relPath)
		if !slices.Contains(parsed.Candidates, tc.wantTitle) {
			t.Fatalf("ParseSeriesWithPath(%q, %q): candidates=%v, missing %q", tc.raw, tc.relPath, parsed.Candidates, tc.wantTitle)
		}
		if tc.wantAuthor != "" && !slices.Contains(parsed.Authors, tc.wantAuthor) {
			t.Fatalf("ParseSeriesWithPath(%q, %q): authors=%v, missing %q", tc.raw, tc.relPath, parsed.Authors, tc.wantAuthor)
		}
		if tc.wantPub != "" && parsed.Publisher != tc.wantPub {
			t.Fatalf("ParseSeriesWithPath(%q, %q): pub=%q, want %q", tc.raw, tc.relPath, parsed.Publisher, tc.wantPub)
		}
		if parsed.SubfolderSuffix != tc.wantSuffix {
			t.Fatalf("ParseSeriesWithPath(%q, %q): suffix=%q, want %q", tc.raw, tc.relPath, parsed.SubfolderSuffix, tc.wantSuffix)
		}
	}

	// Verify scanlator homonym [恶之华] does not hijack unrelated unmatched manga
	homonym := ParseSeriesName("[惡之華][不存在的神秘推理書]")
	candAkuNoHana := SubjectCandidate{
		ID:       999,
		Platform: PlatformComic,
		IsSeries: true,
		Name:     "惡の華",
		NameCN:   "恶之华",
		Authors:  []database.Author{{Name: "押见修造", Role: "writer"}},
	}
	if conf := ScoreCandidate(homonym, candAkuNoHana, false); conf >= AutoMatchMinConfidence {
		t.Fatalf("expected scanlator tag [恶之华] on unrelated title to stay below threshold, got %v", conf)
	}
	// Whereas genuine [押见修造][恶之华] must match with high confidence!
	realAku := ParseSeriesName("[押見修造][惡之華][東立][11完]")
	if conf := ScoreCandidate(realAku, candAkuNoHana, false); conf < 0.95 {
		t.Fatalf("expected genuine [押见修造][恶之华] to score >= 0.95, got %v", conf)
	}

	// Explicit [小說] / 轻小说 folder must allow PlatformNovel to score above threshold, while manga folders still penalize novels!
	candNovel := SubjectCandidate{
		ID:       909,
		Platform: PlatformNovel,
		IsSeries: true,
		Name:     "雲と羅針盤",
		NameCN:   "CLOUD×COMPASS！",
		Authors:  []database.Author{{Name: "竹宫千佳", Role: "writer"}},
	}
	novelQuery := ParseSeriesWithPath("[小說][CLOUDxCOMPASS！][竹宮千佳][角川][10完]", "书库/译版轻小说/[小說][CLOUDxCOMPASS！]")
	if !novelQuery.IsNovelHint {
		t.Fatal("expected IsNovelHint=true for explicit [小說] folder")
	}
	if conf := ScoreCandidate(novelQuery, candNovel, false); conf < AutoMatchMinConfidence {
		t.Fatalf("expected explicit novel folder to match PlatformNovel above threshold, got %v", conf)
	}
	mangaQuery := ParseSeriesName("[CLOUDxCOMPASS！][10完]")
	if conf := ScoreCandidate(mangaQuery, candNovel, false); conf >= 0.80 {
		t.Fatalf("expected non-novel folder to penalize PlatformNovel, got %v", conf)
	}
}

func TestOfflineArchiveImportBatchScrapeAndNSFWVisibility(t *testing.T) {
	ctx := t.Context()
	tmp := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	store, err := database.Open(filepath.Join(tmp, "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	svc, err := NewService(store, filepath.Join(tmp, "bangumi"), logger)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	// Build a synthetic Bangumi Archive dump zip containing:
	// - 100: Main Manga Series "SLAM DUNK" (name_cn: "灌篮高手", platform: 1001, series: true, nsfw: false)
	// - 101: Tankobon Vol.1 "SLAM DUNK (1)" linked via relation 1003 from 100 -> MUST be excluded!
	// - 102: Artbook "灌篮高手 原画集" (platform: 1003) -> MUST score lower than 100!
	// - 103: NSFW Manga (nsfw: true) -> MUST be imported & matched with IsNSFW=true, hidden by default!
	// - 104: Restricted-tagged Manga (nsfw: false, user tag "R18") -> MUST be imported with IsNSFW=false!
	zipPath := filepath.Join(tmp, "dump-test.zip")
	writeSyntheticBangumiZip(t, zipPath)

	if err := svc.ImportArchiveZip(ctx, zipPath, "dump-test.zip", "2026-11-17T00:00:00Z"); err != nil {
		t.Fatalf("ImportArchiveZip: %v", err)
	}
	st := svc.ArchiveStatus()
	if !st.Ready || st.SubjectCount != 4 {
		t.Fatalf("expected 4 indexed subjects (100, 102, 103, 104; excluding tankobon 101), got %+v", st)
	}

	lib := database.Library{
		ID: "l_test", Name: "漫画库", ProviderType: database.ProviderLocal,
		RootCID: "local:/tmp", RootPath: "/tmp", Enabled: true,
	}
	if err := store.UpsertLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, row := range []struct{ id, cid, name, relPath string }{
		{"s_slam", "c1", "[井上雄彥][灌籃高手][東立][31完]", "漫画库/[井上雄彥][灌籃高手][東立][31完]"},
		{"s_slam_tw", "c1_tw", "台版", "漫画库/[井上雄彥][灌籃高手][東立][31完]/台版"},
		{"s_nsfw", "c2", "禁忌成人漫画测试", "漫画库/禁忌成人漫画测试"},
		{"s_r18tag_not_nsfw", "c4", "边缘青年漫画测试", "漫画库/边缘青年漫画测试"},
		{"s_path_nsfw", "c5", "[未來數位] 未收录成人单行本", "漫画库/[未來數位] 未收录成人单行本"},
		{"s_unknown", "c3", "完全不存在的自制涂鸦本12345", "漫画库/完全不存在的自制涂鸦本12345"},
	} {
		if _, err := store.DB().ExecContext(ctx, `
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,created_at,updated_at,seen_scan_id)
VALUES(?,?,?,?,?,0,?,?, 'scan1')`, row.id, lib.ID, row.cid, row.name, row.relPath, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB().ExecContext(ctx, `
INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,number_sort,created_at,updated_at,seen_scan_id)
VALUES(?,?,?,?,'p','v01.cbz',100,'',1,?,?, 'scan1')`, "b_"+row.id, row.id, lib.ID, "f_"+row.id, now, now); err != nil {
			t.Fatal(err)
		}
	}

	run, err := svc.StartBatch(ctx, lib.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, _ = svc.Run(ctx, run.ID)
		if run.Status == "success" || run.Status == "partial" || run.Status == "failed" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if run.Status != "success" || run.MatchedCount != 4 || run.UnmatchedCount != 2 {
		t.Fatalf("unexpected scrape run result: %+v", run)
	}

	slamSeries, _ := store.SeriesByID(ctx, "s_slam")
	meta, err := store.SeriesMetadataByID(ctx, slamSeries)
	if err != nil {
		t.Fatal(err)
	}
	if meta.BgmSubjectID == nil || *meta.BgmSubjectID != 100 || meta.Title != "灌篮高手" || meta.Status != "ENDED" || meta.IsNSFW {
		t.Fatalf("unexpected matched metadata: %+v", meta)
	}

	// Subfolder fallback must append （台版）
	slamTWSeries, _ := store.SeriesByID(ctx, "s_slam_tw")
	twMeta, err := store.SeriesMetadataByID(ctx, slamTWSeries)
	if err != nil || twMeta.Title != "灌篮高手（台版）" {
		t.Fatalf("expected subfolder title '灌篮高手（台版）', got %+v err=%v", twMeta, err)
	}

	// Subject 104 (nsfw=false, R18 user tag) MUST stay visible (IsNSFW=false)
	r18NotNSFW, err := store.SeriesByID(ctx, "s_r18tag_not_nsfw")
	if err != nil {
		t.Fatalf("nsfw:false series with R18 tag should NOT be hidden: %v", err)
	}
	r18Meta, _ := store.SeriesMetadataByID(ctx, r18NotNSFW)
	if r18Meta.IsNSFW {
		t.Fatalf("expected IsNSFW=false for Bangumi nsfw:false subject, got true")
	}

	// Default show_nsfw=false: s_nsfw (Bangumi nsfw=true) and s_path_nsfw ([未來數位]) must be hidden from SeriesByID / SeriesPage!
	if _, err := store.SeriesByID(ctx, "s_nsfw"); err == nil {
		t.Fatal("expected s_nsfw to be hidden by default when show_nsfw=false")
	}
	if _, err := store.SeriesByID(ctx, "s_path_nsfw"); err == nil {
		t.Fatal("expected s_path_nsfw to be hidden by default when show_nsfw=false")
	}
	if nsfwCnt, _ := store.NSFWSeriesCount(ctx); nsfwCnt != 2 {
		t.Fatalf("expected 2 NSFW series in active library, got %d", nsfwCnt)
	}
	// Enabling show_nsfw=true makes both visible immediately!
	if err := store.SetSetting(ctx, "show_nsfw", "true"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SeriesByID(ctx, "s_nsfw"); err != nil {
		t.Fatalf("expected s_nsfw visible when show_nsfw=true: %v", err)
	}
	if _, err := store.SeriesByID(ctx, "s_path_nsfw"); err != nil {
		t.Fatalf("expected s_path_nsfw visible when show_nsfw=true: %v", err)
	}
	_ = store.SetSetting(ctx, "show_nsfw", "false")

	// Second incremental batch run must skip all 6; retry_unmatched must process only the 2 unmatched!
	run2, err := svc.StartBatchWithMode(ctx, lib.ID, ScrapeModeIncremental)
	if err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(deadline) {
		run2, _ = svc.Run(ctx, run2.ID)
		if run2.Status == "success" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if run2.SkippedCount != 6 || run2.MatchedCount != 0 {
		t.Fatalf("expected incremental run to skip all 6, got %+v", run2)
	}

	runRetry, err := svc.StartBatchWithMode(ctx, lib.ID, ScrapeModeRetryUnmatched)
	if err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(deadline) {
		runRetry, _ = svc.Run(ctx, runRetry.ID)
		if runRetry.Status == "success" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if runRetry.SkippedCount != 4 || runRetry.UnmatchedCount != 2 {
		t.Fatalf("expected retry_unmatched to skip 4 matched and retry 2 unmatched, got %+v", runRetry)
	}

	// Test lazy Bangumi cover caching & coverSource toggle in thumbnail.Service
	thumbSvc, err := thumbnail.New(store, filepath.Join(tmp, "thumbnails", "series"), logger)
	if err != nil {
		t.Fatal(err)
	}
	var coverFetches atomic.Int64
	samplePNG := makeTinyPNG(t, color.RGBA{R: 220, G: 40, B: 40, A: 255})
	thumbSvc.SetBangumiCoverFetcher(func(_ context.Context, _ string, subjectID int64) ([]byte, error) {
		coverFetches.Add(1)
		if subjectID != 100 {
			t.Fatalf("unexpected subjectID %d", subjectID)
		}
		return samplePNG, nil
	})

	// First Get lazily fetches & caches on disk; second Get hits disk without calling fetcher!
	d1, ok, err := thumbSvc.Get(ctx, "s_slam")
	if err != nil || !ok || len(d1.Bytes) == 0 {
		t.Fatalf("lazy Bangumi cover Get #1 failed: ok=%v err=%v", ok, err)
	}
	d2, ok, err := thumbSvc.Get(ctx, "s_slam")
	if err != nil || !ok || len(d2.Bytes) == 0 {
		t.Fatalf("lazy Bangumi cover Get #2 failed: ok=%v err=%v", ok, err)
	}
	if coverFetches.Load() != 1 {
		t.Fatalf("expected 1 network fetch due to disk cache, got %d", coverFetches.Load())
	}

	// Switching coverSource to "book" bypasses Bangumi cover
	bookMode := database.CoverSourceBook
	if _, err := store.PatchSeriesMetadata(ctx, slamSeries, database.SeriesMetadataPatch{CoverSource: &bookMode}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := thumbSvc.Get(ctx, "s_slam"); ok {
		t.Fatal("expected no book thumbnail yet when coverSource='book'")
	}

	// Hiding the library excludes it from ScrapeSummary and rejects StartBatch, while preserving scraped rows
	lib.Enabled = false
	if err := store.UpsertLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	if mCount, uCount, tot, err := store.ScrapeSummary(ctx); err != nil || mCount != 0 || uCount != 0 || tot != 0 {
		t.Fatalf("expected 0/0/0 ScrapeSummary when library is hidden, got %d/%d/%d err=%v", mCount, uCount, tot, err)
	}
	if _, err := svc.StartBatch(ctx, lib.ID, false); !errors.Is(err, ErrLibraryHidden) {
		t.Fatalf("StartBatch on hidden library = %v, want ErrLibraryHidden", err)
	}
	lib.Enabled = true
	if err := store.UpsertLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	if mCount, uCount, tot, err := store.ScrapeSummary(ctx); err != nil || mCount != 3 || uCount != 1 || tot != 4 {
		t.Fatalf("expected 3/1/4 ScrapeSummary (excluding 2 hidden NSFW series) after unhiding library, got %d/%d/%d err=%v", mCount, uCount, tot, err)
	}
}

func writeSyntheticBangumiZip(t *testing.T, path string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	addFile := func(name, content string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}

	addFile("subject.jsonlines",
		`{"id":100,"type":1,"platform":1001,"name":"SLAM DUNK","name_cn":"灌篮高手","infobox":"{{Infobox animanga/Manga\n|中文名= 灌篮高手\n|别名={\n[男儿当入樽]\n}\n|出版社= 集英社\n|册数= 31\n|开始= 1990-10-01\n|结束= 1996-06-17\n}}","summary":"篮球经典","nsfw":false,"tags":[{"name":"篮球","count":50},{"name":"热血","count":40}],"meta_tags":["运动","少年漫画"],"score":9.4,"rank":3,"date":"1990-10-01","series":true}`+"\n"+
			`{"id":101,"type":1,"platform":1001,"name":"SLAM DUNK (1)","name_cn":"灌篮高手 (1)","infobox":"","summary":"","nsfw":false,"tags":[],"meta_tags":[],"score":8.5,"rank":0,"date":"1991-02-08","series":false}`+"\n"+
			`{"id":102,"type":1,"platform":1003,"name":"INOUE TAKEHIKO ILLUSTRATIONS","name_cn":"灌篮高手 原画集","infobox":"{{Infobox animanga/Book\n|作者= 井上雄彦\n}}","summary":"画集","nsfw":false,"tags":[],"meta_tags":["画集"],"score":9.1,"rank":50,"date":"1997-06-04","series":false}`+"\n"+
			`{"id":103,"type":1,"platform":1001,"name":"禁忌成人漫画测试","name_cn":"禁忌成人漫画测试","infobox":"","summary":"","nsfw":true,"tags":[],"meta_tags":[],"score":7.0,"rank":0,"date":"2020-01-01","series":true}`+"\n"+
			`{"id":104,"type":1,"platform":1001,"name":"边缘青年漫画测试","name_cn":"边缘青年漫画测试","infobox":"","summary":"","nsfw":false,"tags":[{"name":"R18","count":8}],"meta_tags":["青年漫画"],"score":7.5,"rank":0,"date":"2023-01-01","series":true}`+"\n",
	)
	addFile("subject-relations.jsonlines",
		`{"subject_id":100,"relation_type":1003,"related_subject_id":101,"order":1}`+"\n",
	)
	addFile("subject-persons.jsonlines",
		`{"person_id":500,"subject_id":100,"position":2001}`+"\n",
	)
	addFile("person.jsonlines",
		`{"id":500,"name":"井上雄彦","infobox":"{{Infobox Crt\n|简体中文名= 井上雄彦\n}}"}`+"\n",
	)

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func makeTinyPNG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 60, 90))
	for y := range 90 {
		for x := range 60 {
			img.Set(x, y, c)
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestBangumiProxyValidationRedactionAndProbe(t *testing.T) {
	for _, valid := range []string{
		"",
		"http://127.0.0.1:7890",
		"https://proxy.example.com:8443",
		"socks5://127.0.0.1:1080",
		"socks5h://user:secret@127.0.0.1:1080",
	} {
		if _, _, err := ValidateProxyURL(valid); err != nil {
			t.Fatalf("ValidateProxyURL(%q) unexpected error: %v", valid, err)
		}
	}
	for _, invalid := range []string{"ftp://127.0.0.1:21", "127.0.0.1:7890", "http://"} {
		if _, _, err := ValidateProxyURL(invalid); err == nil {
			t.Fatalf("ValidateProxyURL(%q) expected error", invalid)
		}
	}

	rawAuth := "http://alice:TopSecret99@127.0.0.1:7890"
	redacted := RedactProxyURL(rawAuth)
	if redacted != "http://alice:xxxxx@127.0.0.1:7890" {
		t.Fatalf("RedactProxyURL = %q", redacted)
	}
	merged, err := MergeProxyURLUpdate(redacted, rawAuth)
	if err != nil || merged != rawAuth {
		t.Fatalf("MergeProxyURLUpdate preserved password failed: got %q err=%v", merged, err)
	}

	if !IsAllowedBangumiCoverURL("https://lain.bgm.tv/r/400/pic/cover/l/01/02/100_abc.jpg") ||
		!IsAllowedBangumiCoverURL("https://api.bgm.tv/v0/subjects/100/image?type=common") {
		t.Fatal("expected official Bangumi cover URLs to be allowed")
	}
	if IsAllowedBangumiCoverURL("http://169.254.169.254/latest/meta-data") ||
		IsAllowedBangumiCoverURL("https://evil.example.com/a.jpg") {
		t.Fatal("expected non-Bangumi cover URLs to be rejected")
	}

	// Verify HTTP proxy routing for both API and Archive metadata probes
	var proxyHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/subjects/1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":1,"type":1,"name":"Test"}`))
		case "/latest.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"dump-2026-11-18.zip","size":12345}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		outReq, err := http.NewRequestWithContext(r.Context(), r.Method, r.RequestURI, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		resp, err := http.DefaultClient.Do(outReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxySrv.Close()

	tmp := t.TempDir()
	store, err := database.Open(filepath.Join(tmp, "proxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := NewService(store, filepath.Join(tmp, "bgm"), logger)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	svc.live.baseURL = upstream.URL
	svc.archive.metaURL = upstream.URL + "/latest.json"

	ctx := context.Background()
	if _, err := svc.SetProxyURL(ctx, proxySrv.URL); err != nil {
		t.Fatal(err)
	}
	// Generic settings map must not expose bangumi_proxy_url
	settingsMap, err := store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := settingsMap[SettingProxyURL]; leaked {
		t.Fatal("bangumi_proxy_url should not be exposed in generic Settings()")
	}

	report, err := svc.TestConnectivity(ctx, nil)
	if err != nil {
		t.Fatalf("TestConnectivity error: %v", err)
	}
	if !report.API.OK || !report.Archive.OK || report.Archive.Detail != "dump-2026-11-18.zip" {
		t.Fatalf("unexpected connectivity report: %+v", report)
	}
	if proxyHits.Load() < 2 {
		t.Fatalf("expected both API and Archive probes to traverse proxy, hits=%d", proxyHits.Load())
	}
}
