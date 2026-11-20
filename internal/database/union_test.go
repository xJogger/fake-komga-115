package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestNormalizeUnionSeriesPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"RootA/Shonen/OnePiece", "Shonen/OnePiece"},
		{"RootB/Shonen/OnePiece", "Shonen/OnePiece"},
		{"SMB_Backup\\Shonen\\OnePiece", "Shonen/OnePiece"},
		{"OnlyRootName", "."},
		{"", "."},
		{".", "."},
		{"/RootA/Oneshot/Short01.cbz", "Oneshot/Short01.cbz"},
		{"RootA/Short01.cbz", "Short01.cbz"},
	}
	for _, tc := range cases {
		if got := NormalizeUnionSeriesPath(tc.in); got != tc.want {
			t.Errorf("NormalizeUnionSeriesPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUnionLibraryOverlayInheritanceWritebackAndDissolve(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "union.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	// Create 2 physical libraries with different root names + 1 one-shot library
	if err := store.UpsertLibrary(ctx, Library{
		ID: "lib_local", Name: "本地主库", ProviderType: ProviderLocal,
		RootCID: "local:/mnt/local", RootPath: "/mnt/local", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, Library{
		ID: "lib_115", Name: "115备份库", ProviderType: Provider115,
		RootCID: "cid_115", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, Library{
		ID: "lib_oneshot", Name: "单本库", ProviderType: ProviderLocal,
		RootCID: "local:/mnt/oneshot", RootPath: "/mnt/oneshot", Enabled: true, OneShot: true,
	}); err != nil {
		t.Fatal(err)
	}

	now := nowText()
	// Layer 0 (local): MangaA with Vol 01 (size 2000) & Vol 03 (size 3000)
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,created_at,updated_at,seen_scan_id)
VALUES('s_loc_a','lib_local','Cat/MangaA','MangaA','LocalRoot/Cat/MangaA',0,?,?,'sc1')`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,number_sort,created_at,updated_at,seen_scan_id) VALUES
('b_loc_1','s_loc_a','lib_local','Cat/MangaA/Vol 01.cbz','Cat/MangaA','Vol 01.cbz',2000,'',1,?,?,'sc1'),
('b_loc_3','s_loc_a','lib_local','Cat/MangaA/Vol 03.cbz','Cat/MangaA','Vol 03.cbz',3000,'',2,?,?,'sc1')`, now, now, now, now); err != nil {
		t.Fatal(err)
	}

	// Layer 1 (115): MangaA with Vol 01 (size 1500 - different!), Vol 02 (size 2200), Vol 04 (size 4000)
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,created_at,updated_at,seen_scan_id)
VALUES('s_115_a','lib_115','999','MangaA','CloudRoot/Cat/MangaA',0,?,?,'sc2')`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,number_sort,created_at,updated_at,seen_scan_id) VALUES
('b_115_1','s_115_a','lib_115','f1','999','Vol 01.cbz',1500,'pk1',1,?,?,'sc2'),
('b_115_2','s_115_a','lib_115','f2','999','Vol 02.cbz',2200,'pk2',2,?,?,'sc2'),
('b_115_4','s_115_a','lib_115','f4','999','Vol 04.cbz',4000,'pk4',3,?,?,'sc2')`, now, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}

	// Attach Bangumi metadata on Layer 1, progress on b_115_2, and Collection + ReadList referencing physical IDs
	s115, err := store.SeriesByID(ctx, "s_115_a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyScrapedMetadata(ctx, s115, ScrapedMetadata{
		SubjectID: 777,
		Title:     "海风物语",
		TitleSort: "海风物语",
		Tags:      []string{"冒险"},
		Genres:    []string{"少年"},
		Authors:   []Author{{Name: "测试作者", Role: "writer"}},
	}); err != nil {
		t.Fatal(err)
	}
	b115_2, err := store.BookByID(ctx, "b_115_2")
	if err != nil {
		t.Fatal(err)
	}
	done := true
	if err := store.UpdateBookReadProgress(ctx, b115_2, &done, nil); err != nil {
		t.Fatal(err)
	}
	col, err := store.CreateCollection(ctx, "我的最爱", true, []string{"s_loc_a", "s_115_a"})
	if err != nil {
		t.Fatal(err)
	}
	rl, err := store.CreateReadList(ctx, "必读清单", "", true, []string{"b_loc_1", "b_115_2"})
	if err != nil {
		t.Fatal(err)
	}

	// Validation: One-Shot mismatch must fail
	if _, _, err := store.CreateUnionLibrary(ctx, "错误联合", true, []string{"lib_local", "lib_oneshot"}); !errors.Is(err, ErrUnionOneShotMismatch) {
		t.Fatalf("expected ErrUnionOneShotMismatch, got %v", err)
	}

	// Create union library [lib_local (prio 0), lib_115 (prio 1)]
	unionLib, stats, err := store.CreateUnionLibrary(ctx, "主联合库", true, []string{"lib_local", "lib_115"})
	if err != nil {
		t.Fatal(err)
	}
	if stats.SeriesCount != 1 || stats.BooksCount != 4 || stats.InheritedMetadata != 1 || stats.InheritedProgress != 1 {
		t.Fatalf("unexpected union refresh stats: %+v", stats)
	}
	if len(unionLib.UnionLayers) != 2 || unionLib.UnionLayers[0].MemberLibraryID != "lib_local" || unionLib.UnionLayers[1].MemberLibraryID != "lib_115" {
		t.Fatalf("unexpected union layers: %+v", unionLib.UnionLayers)
	}

	// Validation: member cannot join a second union library & cannot be deleted directly
	if _, _, err := store.CreateUnionLibrary(ctx, "重复联合", true, []string{"lib_local"}); !errors.Is(err, ErrUnionMemberOwned) {
		t.Fatalf("expected ErrUnionMemberOwned, got %v", err)
	}
	if err := store.DeleteLibrary(ctx, "lib_local"); !errors.Is(err, ErrLibraryInUnion) {
		t.Fatalf("expected ErrLibraryInUnion on direct sub-library delete, got %v", err)
	}

	// Visibility: sub-libraries must be hidden from visible lists, while IsLibraryEnabled stays true
	visibleLibs, err := store.LibrariesBasic(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, vl := range visibleLibs {
		if vl.ID == "lib_local" || vl.ID == "lib_115" {
			t.Fatalf("union sub-library %s leaked into visible libraries", vl.ID)
		}
	}
	if store.IsLibraryVisible(ctx, "lib_local") || !store.IsLibraryEnabled(ctx, "lib_local") {
		t.Fatalf("expected lib_local visible=false enabled=true")
	}

	// Verify merged series, natural order of overlaid volumes, upper-layer-wins size, and metadata/progress/collections
	seriesList, total, err := store.SeriesPage(ctx, SeriesQuery{LibraryIDs: []string{unionLib.ID}, Size: 20})
	if err != nil || total != 1 || len(seriesList) != 1 {
		t.Fatalf("union series page: err=%v total=%d len=%d", err, total, len(seriesList))
	}
	uSeries := seriesList[0]
	uMeta, err := store.SeriesMetadataByID(ctx, uSeries)
	if err != nil || uMeta.BgmSubjectID == nil || *uMeta.BgmSubjectID != 777 || uMeta.Title != "海风物语" {
		t.Fatalf("inherited union metadata mismatch: err=%v meta=%+v", err, uMeta)
	}

	uBooks, bTotal, err := store.BooksPage(ctx, BookQuery{SeriesID: uSeries.ID, Unpaged: true})
	if err != nil || bTotal != 4 || len(uBooks) != 4 {
		t.Fatalf("union books: err=%v total=%d len=%d", err, bTotal, len(uBooks))
	}
	wantNames := []string{"Vol 01.cbz", "Vol 02.cbz", "Vol 03.cbz", "Vol 04.cbz"}
	for i, want := range wantNames {
		if uBooks[i].Name != want || uBooks[i].NumberSort != float64(i+1) {
			t.Fatalf("book[%d] = %q (sort=%v), want %q (sort=%d)", i, uBooks[i].Name, uBooks[i].NumberSort, want, i+1)
		}
	}
	// Same filename, different size -> upper layer (2000) wins, both replicas recorded
	if uBooks[0].Size != 2000 {
		t.Fatalf("Vol 01 size=%d, want 2000 (upper layer wins)", uBooks[0].Size)
	}
	srcBooks, srcMeta, err := store.UnionBookSourceBooks(ctx, uBooks[0].ID)
	if err != nil || len(srcBooks) != 2 || srcBooks[0].ID != "b_loc_1" || srcBooks[1].ID != "b_115_1" || srcMeta[1].SourceSize != 1500 {
		t.Fatalf("unexpected Vol 01 sources: err=%v books=%+v meta=%+v", err, srcBooks, srcMeta)
	}
	// Inherited progress on Vol 02
	if completed, err := store.BookReadCompleted(ctx, uBooks[1].ID); err != nil || !completed {
		t.Fatalf("expected Vol 02 inherited completed=true, got %v err=%v", completed, err)
	}
	// Migrated Collection & ReadList point to union IDs (deduplicated)
	colAfter, err := store.CollectionByID(ctx, col.ID)
	if err != nil || len(colAfter.SeriesIDs) != 1 || colAfter.SeriesIDs[0] != uSeries.ID {
		t.Fatalf("collection migration mismatch: err=%v ids=%v", err, colAfter.SeriesIDs)
	}
	rlAfter, err := store.ReadListByID(ctx, rl.ID)
	if err != nil || len(rlAfter.BookIDs) != 2 || rlAfter.BookIDs[0] != uBooks[0].ID || rlAfter.BookIDs[1] != uBooks[1].ID {
		t.Fatalf("readlist migration mismatch: err=%v ids=%v", err, rlAfter.BookIDs)
	}

	// Bidirectional progress writeback: reading Vol 01 on union mirrors back to BOTH physical replicas
	pageNum := 7
	inProg := false
	if err := store.UpdateBookReadProgress(ctx, uBooks[0], &inProg, &pageNum); err != nil {
		t.Fatal(err)
	}
	for _, physID := range []string{"b_loc_1", "b_115_1"} {
		p, ok, err := store.BookReadProgress(ctx, physID)
		if err != nil || !ok || p.Page == nil || *p.Page != 7 {
			t.Fatalf("expected physical replica %s progress mirrored to page 7, got ok=%v p=%+v err=%v", physID, ok, p, err)
		}
	}

	// Bangumi lookup sees ONLY the visible union series (not hidden physical sub-library)
	bgmMatches, err := store.LookupSeriesByBangumiSubject(ctx, 777)
	if err != nil || len(bgmMatches) != 1 || bgmMatches[0].SeriesID != uSeries.ID {
		t.Fatalf("LookupSeriesByBangumiSubject(777) = %+v err=%v, want only union series %s", bgmMatches, err, uSeries.ID)
	}

	// Dissolving union library restores physical libraries to visible with preserved progress
	if err := store.DeleteLibrary(ctx, unionLib.ID); err != nil {
		t.Fatal(err)
	}
	if !store.IsLibraryVisible(ctx, "lib_local") || !store.IsLibraryVisible(ctx, "lib_115") {
		t.Fatalf("expected physical libraries restored to visible after union delete")
	}
	if p, ok, err := store.BookReadProgress(ctx, "b_loc_1"); err != nil || !ok || p.Page == nil || *p.Page != 7 {
		t.Fatalf("expected b_loc_1 progress preserved after dissolving union, got ok=%v p=%+v", ok, p)
	}
}

func TestUnionRefreshDiffZeroMutationAndWinningSourceZipInvalidation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "union_diff.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if err := store.UpsertLibrary(ctx, Library{
		ID: "lib_a", Name: "上层本地", ProviderType: ProviderLocal,
		RootCID: "local:/a", RootPath: "/a", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, Library{
		ID: "lib_b", Name: "下层网盘", ProviderType: Provider115,
		RootCID: "cid_b", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	t1 := "2026-01-01T00:00:00Z"
	t2 := "2026-01-02T00:00:00Z"
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,file_modified_at,created_at,updated_at,seen_scan_id) VALUES
('sa','lib_a','Cat/S1','S1','RootA/Cat/S1',0,?,?,?,'s1'),
('sb','lib_b','900','S1','RootB/Cat/S1',0,?,?,?,'s2')`, t1, t1, t1, t1, t1, t1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,file_created_at,file_modified_at,number_sort,created_at,updated_at,seen_scan_id) VALUES
('ba1','sa','lib_a','Cat/S1/01.cbz','Cat/S1','01.cbz',1000,'',?,?,1,?,?,'s1'),
('bb1','sb','lib_b','f1','900','01.cbz',2000,'pk1',?,?,1,?,?,'s2')`,
		t1, t1, t1, t1, t1, t1, t1, t1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO zip_indexes(book_id,version,page_count,index_json,index_duration_ns,created_at,updated_at) VALUES
('ba1','v_a',18,'[]',10,?,?),
('bb1','v_b',24,'[]',10,?,?)`, t1, t1, t1, t1); err != nil {
		t.Fatal(err)
	}

	unionLib, _, err := store.CreateUnionLibrary(ctx, "DiffUnion", true, []string{"lib_a", "lib_b"})
	if err != nil {
		t.Fatal(err)
	}

	// Capture updated_at and shelf_revision right after create.
	var uSeriesID, uBookID, updatedBefore string
	if err := store.DB().QueryRowContext(ctx, `SELECT id, updated_at FROM series WHERE library_id=?`, unionLib.ID).Scan(&uSeriesID, &updatedBefore); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT id FROM books WHERE library_id=?`, unionLib.ID).Scan(&uBookID); err != nil {
		t.Fatal(err)
	}
	var pageCount int
	if err := store.DB().QueryRowContext(ctx, `SELECT page_count FROM zip_indexes WHERE book_id=?`, uBookID).Scan(&pageCount); err != nil || pageCount != 18 {
		t.Fatalf("expected inherited page_count=18 from lib_a, got %d err=%v", pageCount, err)
	}

	revBefore, err := store.shelfRevision(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)

	// Warm 0-change refresh MUST NOT touch series.updated_at or shelf_revision.
	if _, err := store.RefreshUnionLibrary(ctx, unionLib.ID); err != nil {
		t.Fatal(err)
	}
	revAfter, err := store.shelfRevision(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if revAfter != revBefore {
		t.Fatalf("warm no-op refresh mutated shelf_revision: before=%+v after=%+v", revBefore, revAfter)
	}
	var updatedAfter string
	if err := store.DB().QueryRowContext(ctx, `SELECT updated_at FROM series WHERE id=?`, uSeriesID).Scan(&updatedAfter); err != nil {
		t.Fatal(err)
	}
	if updatedAfter != updatedBefore {
		t.Fatalf("warm no-op refresh mutated series.updated_at: %q -> %q", updatedBefore, updatedAfter)
	}

	// Reverse layer order [lib_b, lib_a]: winning source switches from ba1 (18p) to bb1 (24p),
	// so union zip_indexes must refresh to 24p and book size must update to 2000.
	if _, _, err := store.UpdateUnionLibrary(ctx, unionLib.ID, nil, nil, []string{"lib_b", "lib_a"}); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT page_count FROM zip_indexes WHERE book_id=?`, uBookID).Scan(&pageCount); err != nil || pageCount != 24 {
		t.Fatalf("expected zip_indexes page_count updated to 24 after layer switch, got %d err=%v", pageCount, err)
	}
	var uSize int64
	if err := store.DB().QueryRowContext(ctx, `SELECT size FROM books WHERE id=?`, uBookID).Scan(&uSize); err != nil || uSize != 2000 {
		t.Fatalf("expected union book size=2000 after layer switch, got %d err=%v", uSize, err)
	}

	// Incremental book addition updates rollup columns accurately.
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,file_created_at,file_modified_at,number_sort,created_at,updated_at,seen_scan_id)
VALUES('bb2','sb','lib_b','f2','900','02.cbz',3000,'pk2',?,?,2,?,?,'s2')`, t2, t2, t2, t2); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RefreshUnionLibrary(ctx, unionLib.ID); err != nil {
		t.Fatal(err)
	}
	var booksCnt int
	var sortMod string
	if err := store.DB().QueryRowContext(ctx, `SELECT books_count, sort_modified_at FROM series WHERE id=?`, uSeriesID).Scan(&booksCnt, &sortMod); err != nil {
		t.Fatal(err)
	}
	if booksCnt != 2 || sortMod != t2 {
		t.Fatalf("expected rollup books_count=2 sort_modified_at=%s, got count=%d sort_modified_at=%s", t2, booksCnt, sortMod)
	}

	// DeleteLibrary must reset bulk_mode=0 and bump shelf_revision.
	revPreDel, _ := store.shelfRevision(ctx, true)
	if err := store.DeleteLibrary(ctx, unionLib.ID); err != nil {
		t.Fatal(err)
	}
	var bulkMode int
	if err := store.DB().QueryRowContext(ctx, `SELECT bulk_mode FROM shelf_revision WHERE id=1`).Scan(&bulkMode); err != nil || bulkMode != 0 {
		t.Fatalf("expected bulk_mode=0 after DeleteLibrary, got %d err=%v", bulkMode, err)
	}
	revPostDel, _ := store.shelfRevision(ctx, true)
	if revPostDel.catalog <= revPreDel.catalog {
		t.Fatalf("expected shelf_revision.catalog bumped on DeleteLibrary: %+v -> %+v", revPreDel, revPostDel)
	}
}

func TestMigrateLegacyShelfRevisionWithoutBulkMode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy_v22.db")
	// Simulate an existing v2.2 DB whose shelf_revision has only (id, catalog, progress).
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
CREATE TABLE shelf_revision (id INTEGER PRIMARY KEY CHECK(id=1), catalog INTEGER NOT NULL, progress INTEGER NOT NULL);
INSERT INTO shelf_revision VALUES(1, 42, 7);
`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	raw.Close()

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open legacy DB failed: %v", err)
	}
	defer store.Close()

	var cat, prog, bulk int
	if err := store.DB().QueryRow(`SELECT catalog, progress, bulk_mode FROM shelf_revision WHERE id=1`).Scan(&cat, &prog, &bulk); err != nil {
		t.Fatal(err)
	}
	if cat < 42 || prog != 7 || bulk != 0 {
		t.Fatalf("unexpected shelf_revision after migration: cat=%d prog=%d bulk=%d", cat, prog, bulk)
	}
	if !triggerHasBulkGuard(store.DB(), "fk115_books_rollup_upd") || !triggerHasBulkGuard(store.DB(), "fk115_shelf_books_insert") {
		t.Fatalf("expected rollup and shelf triggers upgraded with bulk_mode guard")
	}
}

func TestUnionZipIndexInheritSameSizeOnly(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "union_zip_size.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if err := store.UpsertLibrary(ctx, Library{
		ID: "lib_top", Name: "本地上层", ProviderType: ProviderLocal,
		RootCID: "local:/top", RootPath: "/top", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, Library{
		ID: "lib_bot", Name: "115下层", ProviderType: Provider115,
		RootCID: "cid_bot", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	t1 := "2026-01-01T00:00:00Z"
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,created_at,updated_at,seen_scan_id) VALUES
('st','lib_top','Cat/M','M','Top/Cat/M',0,?,?,'s1'),
('sb','lib_bot','100','M','Bot/Cat/M',0,?,?,'s2')`, t1, t1, t1, t1); err != nil {
		t.Fatal(err)
	}
	// Vol 01: same size (5000) across layers; only lower layer has zip_indexes (32 pages) -> SHOULD inherit!
	// Vol 02: different size (top=6000, bot=7000); only lower layer has zip_indexes (40 pages) -> MUST NOT inherit!
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,number_sort,created_at,updated_at,seen_scan_id) VALUES
('bt1','st','lib_top','Cat/M/01.cbz','Cat/M','01.cbz',5000,'',1,?,?,'s1'),
('bt2','st','lib_top','Cat/M/02.cbz','Cat/M','02.cbz',6000,'',2,?,?,'s1'),
('bb1','sb','lib_bot','f1','100','01.cbz',5000,'pk1',1,?,?,'s2'),
('bb2','sb','lib_bot','f2','100','02.cbz',7000,'pk2',2,?,?,'s2')`,
		t1, t1, t1, t1, t1, t1, t1, t1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO zip_indexes(book_id,version,page_count,index_json,index_duration_ns,created_at,updated_at) VALUES
('bb1','v1',32,'[]',10,?,?),
('bb2','v2',40,'[]',10,?,?)`, t1, t1, t1, t1); err != nil {
		t.Fatal(err)
	}

	unionLib, _, err := store.CreateUnionLibrary(ctx, "ZipUnion", true, []string{"lib_top", "lib_bot"})
	if err != nil {
		t.Fatal(err)
	}
	books, _, err := store.BooksPage(ctx, BookQuery{LibraryIDs: []string{unionLib.ID}, Unpaged: true})
	if err != nil || len(books) != 2 {
		t.Fatalf("unexpected union books: len=%d err=%v", len(books), err)
	}
	pagesByName := map[string]int{}
	for _, b := range books {
		pagesByName[b.Name] = b.PageCount
	}
	if pagesByName["01.cbz"] != 32 {
		t.Fatalf("expected same-size lower replica page_count=32 inherited for 01.cbz, got %d", pagesByName["01.cbz"])
	}
	if pagesByName["02.cbz"] != 0 {
		t.Fatalf("expected different-size lower replica page_count NOT inherited for 02.cbz, got %d", pagesByName["02.cbz"])
	}
}

func TestUnionSubLibraryLegacyIDTransparentResolution(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "union_alias.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if err := store.UpsertLibrary(ctx, Library{
		ID: "lib_top", Name: "本地层", ProviderType: ProviderLocal,
		RootCID: "local:/top", RootPath: "/top", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, Library{
		ID: "lib_115", Name: "115层", ProviderType: Provider115,
		RootCID: "cid_115", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	now := nowText()
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,created_at,updated_at,seen_scan_id) VALUES
('s_top','lib_top','Cat/K','K','Top/Cat/K',0,?,?,'s1'),
('s_115','lib_115','100','K','Cloud/Cat/K',0,?,?,'s2')`, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,number_sort,created_at,updated_at,seen_scan_id) VALUES
('b_top_1','s_top','lib_top','Cat/K/01.cbz','Cat/K','01.cbz',1000,'',1,?,?,'s1'),
('b_top_2','s_top','lib_top','Cat/K/02.cbz','Cat/K','02.cbz',1200,'',2,?,?,'s1'),
('b_115_1','s_115','lib_115','f1','100','01.cbz',1000,'pk1',1,?,?,'s2')`,
		now, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}

	unionLib, _, err := store.CreateUnionLibrary(ctx, "AliasUnion", true, []string{"lib_top", "lib_115"})
	if err != nil {
		t.Fatal(err)
	}

	// Old physical sub-library series/book IDs transparently resolve to the union series/book.
	uSeries, err := store.SeriesByID(ctx, "s_115")
	if err != nil || uSeries.LibraryID != unionLib.ID || uSeries.ID == "s_115" {
		t.Fatalf("expected s_115 resolved to union series, got %+v err=%v", uSeries, err)
	}
	uBook, err := store.BookByID(ctx, "b_115_1")
	if err != nil || uBook.LibraryID != unionLib.ID || uBook.SeriesID != uSeries.ID || uBook.ID == "b_115_1" {
		t.Fatalf("expected b_115_1 resolved to union book, got %+v err=%v", uBook, err)
	}
	// BooksPage filtered by old physical series ID returns all merged union books (including Vol 02 from top layer).
	books, total, err := store.BooksPage(ctx, BookQuery{SeriesID: "s_115", Unpaged: true})
	if err != nil || total != 2 || len(books) != 2 {
		t.Fatalf("expected 2 merged books via legacy seriesID s_115, got total=%d len=%d err=%v", total, len(books), err)
	}

	// Manually hiding the parent union library must return sql.ErrNoRows for BOTH union and old sub-library IDs.
	disabled := false
	if _, _, err := store.UpdateUnionLibrary(ctx, unionLib.ID, nil, &disabled, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SeriesByID(ctx, "s_115"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows when parent union is hidden, got %v", err)
	}
	if _, err := store.BookByID(ctx, "b_115_1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows for book when parent union is hidden, got %v", err)
	}

	// Re-enable union, then manually hide lib_115: old s_115 ID must return ErrNoRows while s_top still resolves.
	enabled := true
	if _, _, err := store.UpdateUnionLibrary(ctx, unionLib.ID, nil, &enabled, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, Library{
		ID: "lib_115", Name: "115层", ProviderType: Provider115,
		RootCID: "cid_115", Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SeriesByID(ctx, "s_115"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows when source physical library is manually hidden, got %v", err)
	}
	if sTopResolved, err := store.SeriesByID(ctx, "s_top"); err != nil || sTopResolved.ID != uSeries.ID {
		t.Fatalf("expected enabled source s_top still resolves to union, got %+v err=%v", sTopResolved, err)
	}
}
