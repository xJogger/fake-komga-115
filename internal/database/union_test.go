package database

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
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
