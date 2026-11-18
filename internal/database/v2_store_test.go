package database

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAuthLifecycle(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()

	has, err := store.HasAdmin(ctx)
	if err != nil || has {
		t.Fatalf("expected unclaimed store, got has=%v err=%v", has, err)
	}

	admin, err := store.ClaimAdmin(ctx, "manga_owner", "secret-pass-123")
	if err != nil {
		t.Fatalf("claim admin: %v", err)
	}
	if admin.Username != "manga_owner" {
		t.Fatalf("unexpected username: %q", admin.Username)
	}
	if _, err := store.ClaimAdmin(ctx, "another", "secret-pass-456"); !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("expected ErrAlreadyClaimed, got %v", err)
	}

	// First verification populates cache, second hits cache.
	for range 2 {
		user, ok, err := store.VerifyAdminPassword(ctx, "MANGA_OWNER", "secret-pass-123")
		if err != nil || !ok || user.Username != "manga_owner" {
			t.Fatalf("verify password failed: user=%+v ok=%v err=%v", user, ok, err)
		}
	}
	if _, ok, _ := store.VerifyAdminPassword(ctx, "manga_owner", "wrong"); ok {
		t.Fatal("wrong password should fail")
	}

	tok1, _, err := store.CreateSession(ctx, admin.Username, true, "TestBrowser/1.0")
	if err != nil {
		t.Fatal(err)
	}
	tok2, _, err := store.CreateSession(ctx, admin.Username, false, "OtherBrowser/1.0")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ValidateSession(ctx, tok1); err != nil || !ok {
		t.Fatalf("validate session 1 failed: ok=%v err=%v", ok, err)
	}

	// Updating password invalidates old Basic Auth cache and other sessions while keeping tok1.
	if _, err := store.UpdateAdminCredentials(ctx, "secret-pass-123", "new_owner", "new-pass-789", tok1); err != nil {
		t.Fatalf("update credentials: %v", err)
	}
	if _, ok, _ := store.VerifyAdminPassword(ctx, "manga_owner", "secret-pass-123"); ok {
		t.Fatal("old credentials remained valid after update")
	}
	if _, ok, _ := store.VerifyAdminPassword(ctx, "new_owner", "new-pass-789"); !ok {
		t.Fatal("new credentials should verify")
	}
	if _, ok, _ := store.ValidateSession(ctx, tok2); ok {
		t.Fatal("other session should have been revoked")
	}
	if sess, ok, _ := store.ValidateSession(ctx, tok1); !ok || sess.Username != "new_owner" {
		t.Fatalf("kept session should remain valid with updated username, got %+v ok=%v", sess, ok)
	}

	// API Key lifecycle
	rawKey, keyInfo, err := store.CreateAPIKey(ctx, "Koharia Phone")
	if err != nil {
		t.Fatal(err)
	}
	if validated, ok, err := store.ValidateAPIKey(ctx, rawKey); err != nil || !ok || validated.ID != keyInfo.ID {
		t.Fatalf("validate API key failed: %+v ok=%v err=%v", validated, ok, err)
	}
	if err := store.DeleteAPIKey(ctx, keyInfo.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.ValidateAPIKey(ctx, rawKey); ok {
		t.Fatal("deleted API key remained valid")
	}
}

func TestMetadataLocksAndSearchFilters(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()

	lib1 := Library{ID: "l_local1", Name: "本地漫画", ProviderType: ProviderLocal, RootCID: "local:/comics/a", RootPath: "/comics/a", Enabled: true}
	lib2 := Library{ID: "l_local2", Name: "本地短篇", ProviderType: ProviderLocal, RootCID: "local:/comics/b", RootPath: "/comics/b", Enabled: true, OneShot: true}
	if err := store.UpsertLibrary(ctx, lib1); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, lib2); err != nil {
		t.Fatal(err)
	}

	now := nowText()
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,created_at,updated_at,seen_scan_id)
VALUES('s_1','l_local1','dir1','[SLAM DUNK][Inoue]','SLAM DUNK',0,?,?, 'scan1')`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,number_sort,created_at,updated_at,seen_scan_id)
VALUES('b_1','s_1','l_local1','dir1/v01.cbz','dir1','Vol.01.cbz',1024,'',1,?,?, 'scan1')`, now, now); err != nil {
		t.Fatal(err)
	}

	series, err := store.SeriesByID(ctx, "s_1")
	if err != nil {
		t.Fatal(err)
	}
	score := 9.4
	total := 31
	meta, err := store.ApplyScrapedMetadata(ctx, series, ScrapedMetadata{
		SubjectID:       1608,
		Confidence:      0.98,
		CoverURL:        "https://api.bgm.tv/v0/subjects/1608/image?type=common",
		Score:           &score,
		Status:          "ENDED",
		Title:           "灌篮高手",
		TitleSort:       "灌篮高手",
		Summary:         "青春篮球漫画经典",
		Publisher:       "集英社",
		TotalBookCount:  &total,
		ReleaseDate:     "1990-10-01",
		Genres:          []string{"运动", "热血"},
		Tags:            []string{"篮球", "青春"},
		Authors:         []Author{{Name: "井上雄彦", Role: "writer"}, {Name: "井上雄彦", Role: "penciller"}},
		AlternateTitles: []AlternateTitle{{Label: "原名", Title: "SLAM DUNK"}},
		Links:           []WebLink{{Label: "Bangumi", URL: "https://bgm.tv/subject/1608"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "灌篮高手" || meta.Status != "ENDED" {
		t.Fatalf("unexpected scraped metadata: %+v", meta)
	}

	// Lock title manually, then re-scrape with different title -> locked title must remain!
	customTitle := "灌篮高手（完全版）"
	lockTrue := true
	if _, err := store.PatchSeriesMetadata(ctx, series, SeriesMetadataPatch{
		Title:     &customTitle,
		TitleLock: &lockTrue,
	}); err != nil {
		t.Fatal(err)
	}
	meta2, err := store.ApplyScrapedMetadata(ctx, series, ScrapedMetadata{
		SubjectID: 1608,
		Title:     "不应覆盖的标题",
		Summary:   "更新后的简介",
	})
	if err != nil {
		t.Fatal(err)
	}
	if meta2.Title != customTitle || meta2.Summary != "更新后的简介" {
		t.Fatalf("field lock failed: title=%q summary=%q", meta2.Title, meta2.Summary)
	}

	// Search by Chinese title AND original folder name AND filter by tag/author/publisher
	for _, keyword := range []string{"灌篮高手", "Inoue", "SLAM DUNK"} {
		items, count, err := store.SeriesPage(ctx, SeriesQuery{Search: keyword})
		if err != nil || count != 1 || len(items) != 1 {
			t.Fatalf("search %q expected 1 hit, got count=%d err=%v", keyword, count, err)
		}
	}
	items, count, err := store.SeriesPage(ctx, SeriesQuery{
		Genres:     []string{"运动"},
		Tags:       []string{"篮球"},
		Publishers: []string{"集英社"},
		Authors:    []string{"井上雄彦"},
		Statuses:   []string{"ENDED"},
	})
	if err != nil || count != 1 || len(items) != 1 {
		t.Fatalf("structured metadata filter expected 1 hit, got count=%d err=%v", count, err)
	}

	genres, _ := store.Genres(ctx, nil)
	tags, _ := store.Tags(ctx, nil)
	pubs, _ := store.Publishers(ctx, nil)
	authors, _ := store.Authors(ctx, nil, "井上", "")
	if len(genres) != 2 || len(tags) != 2 || len(pubs) != 1 || len(authors) != 2 {
		t.Fatalf("unexpected referentials: genres=%v tags=%v pubs=%v authors=%v", genres, tags, pubs, authors)
	}
}
