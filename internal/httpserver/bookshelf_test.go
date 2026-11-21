package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func TestBookshelfVersionsAndMergedSelectionPreferMostBooks(t *testing.T) {
	ctx := context.Background()
	store, err := database.Open(filepath.Join(t.TempDir(), "bookshelf_versions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.ClaimAdmin(ctx, "admin", "password123"); err != nil {
		t.Fatal(err)
	}

	unionLib := database.Library{ID: "lib_union", Name: "AAA Union", ProviderType: database.ProviderUnion, RootCID: "union:lib_union", Enabled: true}
	localLib := database.Library{ID: "lib_local", Name: "BBB Local", ProviderType: database.ProviderLocal, RootCID: "local:/a", RootPath: "/a", Enabled: true}
	remoteLib := database.Library{ID: "lib_remote", Name: "CCC Remote", ProviderType: database.Provider115, RootCID: "cid_remote", Enabled: true}
	for _, lib := range []database.Library{unionLib, localLib, remoteLib} {
		if err := store.UpsertLibrary(ctx, lib); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	seedSeries := func(libID, seriesID, name string, count int) {
		t.Helper()
		if _, err := store.DB().ExecContext(ctx,
			`INSERT INTO series(id,library_id,cid,name,relative_path,created_at,updated_at,seen_scan_id) VALUES (?,?,?,?,?,?,?,'scan1')`,
			seriesID, libID, seriesID, name, name, now, now); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < count; i++ {
			bookID := seriesID + "_b_" + string(rune('a'+i))
			if _, err := store.DB().ExecContext(ctx,
				`INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,number_sort,created_at,updated_at,seen_scan_id) VALUES (?,?,?,?,?,?,1024,'pk',?,?,?,'scan1')`,
				bookID, seriesID, libID, bookID, seriesID, bookID+".cbz", float64(i+1), now, now); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.DB().ExecContext(ctx,
			`INSERT INTO series_metadata(series_id,bgm_subject_id,title,title_sort,updated_at) VALUES (?,?,?,?,?)`,
			seriesID, 4242, "Unified Work", "Unified Work", now); err != nil {
			t.Fatal(err)
		}
	}

	seedSeries(unionLib.ID, "s_union_2", "Edition Union 2v", 2)
	seedSeries(localLib.ID, "s_local_2", "Edition Local 2v", 2)
	seedSeries(remoteLib.ID, "s_remote_5", "Edition Remote 5v", 5)

	srv := New(store, nil, nil, nil, nil, nil, nil, nil, RuntimeInfo{}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	doAuthGet := func(path string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth("admin", "password123")
		client := &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// 1. Merged bookshelf series selects the 5-book edition.
	resp := doAuthGet("/admin/api/bookshelf/series?merge_bangumi=1&size=20")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("merged bookshelf status = %d", resp.StatusCode)
	}
	var page struct {
		TotalElements int `json:"totalElements"`
		Content       []struct {
			ID         string `json:"id"`
			BooksCount int    `json:"booksCount"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if page.TotalElements != 1 || len(page.Content) != 1 || page.Content[0].ID != "s_remote_5" || page.Content[0].BooksCount != 5 {
		t.Fatalf("unexpected merged bookshelf result: %+v", page)
	}

	// 2. Multi-version panel endpoint sorts by booksCount DESC, then union library first.
	vResp := doAuthGet("/admin/api/bookshelf/versions/s_local_2")
	defer vResp.Body.Close()
	if vResp.StatusCode != http.StatusOK {
		t.Fatalf("versions status = %d", vResp.StatusCode)
	}
	var vBody struct {
		Versions []struct {
			ID         string `json:"id"`
			BooksCount int    `json:"booksCount"`
		} `json:"versions"`
	}
	if err := json.NewDecoder(vResp.Body).Decode(&vBody); err != nil {
		t.Fatal(err)
	}
	if len(vBody.Versions) != 3 {
		t.Fatalf("expected 3 versions, got %+v", vBody.Versions)
	}
	wantOrder := []string{"s_remote_5", "s_union_2", "s_local_2"}
	for i, wantID := range wantOrder {
		if vBody.Versions[i].ID != wantID {
			t.Fatalf("versions[%d] = %+v, want %s (full=%+v)", i, vBody.Versions[i], wantID, vBody.Versions)
		}
	}

	// 3. /bgm/{subjectID} redirects to the 5-book edition.
	jResp := doAuthGet("/bgm/4242")
	defer jResp.Body.Close()
	if jResp.StatusCode != http.StatusFound || jResp.Header.Get("Location") != "/series/s_remote_5?versions=1" {
		t.Fatalf("/bgm/4242 status=%d loc=%q, want 302 /series/s_remote_5?versions=1", jResp.StatusCode, jResp.Header.Get("Location"))
	}
}
