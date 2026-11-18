package bangumi

import (
	"archive/zip"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/time/rate"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func TestShortTitleAndNovelCategory(t *testing.T) {
	for _, name := range []string{"H2 [田中凛][小学馆]", "86 [田中凛][小学馆]", "Rev [田中凛][小学馆]"} {
		want := strings.Fields(name)[0]
		parsed := ParseSeriesName(name)
		if !slices.Contains(parsed.Candidates, want) || !slices.Contains(parsed.Authors, "田中凛") {
			t.Errorf("%q lost title or author: %+v", name, parsed)
		}
	}
	if p := ParseSeriesName("关于轻小说的漫画"); p.IsNovelHint {
		t.Fatal("a reference to light novels inside the title is not a genre label")
	}
	novel := SubjectCandidate{NameCN: "纸上的车站", Platform: PlatformNovel, IsSeries: true}
	comic := novel
	comic.Platform = PlatformComic
	for _, path := range []string{"书库/轻小说/纸上的车站", `书库\小說\纸上的车站`} {
		q := ParseSeriesWithPath("纸上的车站", path)
		if n, c := ScoreCandidate(q, novel, false), ScoreCandidate(q, comic, false); !q.IsNovelHint || n < AutoMatchMinConfidence || n <= c {
			t.Errorf("%q: novel=%v comic=%v hint=%v", path, n, c, q.IsNovelHint)
		}
	}
	q := ParseSeriesName("纸上的车站")
	if ScoreCandidate(q, novel, false) >= ScoreCandidate(q, comic, false) {
		t.Fatal("ordinary folders should still prefer the manga adaptation")
	}
}

func TestCompoundTitleEvidence(t *testing.T) {
	q := ParseSeriesName("[晨星之间 & Morning Star][田中凛][3完]")
	if slices.Contains(q.Candidates, "Morning Star") || !slices.Contains(q.lookupTitles(), "Morning Star") {
		t.Fatalf("a component should be a lookup hint only: %+v", q)
	}
	for _, c := range []SubjectCandidate{
		{Name: "Morning Star", Platform: PlatformComic, Rank: 1, IsSeries: true, Authors: []database.Author{{Name: "田中凛"}}},
		{Name: "晨星之间", AlternateTitles: []database.AlternateTitle{{Title: "Morning Star 完全版"}}},
	} {
		if got := ScoreCandidate(q, c, false); got != 0 {
			t.Errorf("only one component matched: %+v scored %.3f", c, got)
		}
	}
	for _, c := range []SubjectCandidate{
		{Name: "Morning Star", NameCN: "晨星之间", Platform: PlatformComic},
		{Name: "晨星之间", AlternateTitles: []database.AlternateTitle{{Title: "Morning Star"}}, Platform: PlatformComic},
		{Name: "晨星之间 & Morning Star", Platform: PlatformComic},
	} {
		if got := ScoreCandidate(q, c, false); got < AutoMatchMinConfidence {
			t.Errorf("exact complete evidence did not match: %+v scored %.3f", c, got)
		}
	}
	for _, pair := range [][2]string{
		{"CLOUDxCOMPASS", "CLOUD×COMPASS"}, {"CLOUD✕COMPASS", "CLOUDxCOMPASS"},
		{"CLOUD╳COMPASS", "CLOUDxCOMPASS"},
	} {
		c := SubjectCandidate{Name: pair[1], Platform: PlatformComic}
		if got := ScoreCandidate(ParseSeriesName(pair[0]), c, false); got < AutoMatchMinConfidence {
			t.Errorf("%q did not match %q: %.3f", pair[0], pair[1], got)
		}
	}
}

type matchingTransport func(*http.Request) (*http.Response, error)

func (f matchingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCompoundCandidateSearchAndManualOverride(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	idx, err := NewArchiveIndex(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	f, err := os.Create(filepath.Join(dir, "dump.zip"))
	if err != nil {
		t.Fatal(err)
	}
	zipWriter := zip.NewWriter(f)
	w, err := zipWriter.Create("subject.jsonlines")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(w, `{"id":11,"type":1,"platform":1001,"name":"BLUE TRAIN","name_cn":"蓝色列车","series":true}`+"\n"+
		`{"id":12,"type":1,"platform":1001,"name":"CLOUDxCOMPASS","series":true}`+"\n"+
		`{"id":13,"type":1,"platform":1001,"name":"MIDNIGHT×TRAIN","series":true}`); err != nil {
		t.Fatal(err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := idx.ImportFromZipFile(ctx, f.Name(), "dump.zip", ""); err != nil {
		t.Fatal(err)
	}

	for _, query := range []string{"CLOUD×COMPASS", "MIDNIGHTxTRAIN"} {
		got, err := idx.Search(ctx, ParseSeriesName(query), false, 10)
		if err != nil || len(got) != 1 || got[0].Confidence < AutoMatchMinConfidence {
			t.Fatalf("cross variant offline %s: %v %v", query, got, err)
		}
	}

	live := NewLiveClient()
	live.http = &http.Client{Transport: matchingTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"data":[{"id":11,"type":1,"platform":"漫画","name":"BLUE TRAIN","name_cn":"蓝色列车","series":true}]}`
		switch r.URL.Path {
		case "/v0/subjects/11":
			body = `{"id":11,"type":1,"platform":"漫画","name":"BLUE TRAIN","name_cn":"蓝色列车","series":true}`
		case "/v0/subjects/11/persons":
			body = "[]"
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	live.limiter = rate.NewLimiter(rate.Inf, 1)
	svc := Service{archive: idx, live: live}
	for _, test := range []struct {
		name, q string
		count   int
	}{
		{"[橙色电车＋蓝色列车][7+1完]", "", 0},
		{"[蓝色列车 & ORANGE BUS]", "", 0},
		{"[蓝色列车 & BLUE TRAIN]", "", 1},
		{"[橙色电车 + 蓝色列车]", "蓝色列车", 1},
		{"[橙色电车 + 蓝色列车]", "11", 1},
	} {
		series := database.Series{Name: test.name}
		got, _, err := svc.SearchCandidates(ctx, series, test.q)
		if err != nil || len(got) != test.count || len(got) > 0 && got[0].ID != 11 {
			t.Fatalf("%q with q=%q: returned %v, err=%v", test.name, test.q, got, err)
		}
		got, err = idx.Search(ctx, ParseSeriesName(test.name), false, 10)
		if test.q == "" && (err != nil || len(got) != test.count) {
			t.Fatalf("%q offline: returned %v, err=%v", test.name, got, err)
		}
	}
}

func TestArchiveTermsUpgradeRollsBackOnFailedInsert(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	_, err = db.ExecContext(ctx, `
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO meta VALUES ('terms_schema_version','2');
CREATE TABLE bgm_subjects (id INTEGER PRIMARY KEY, name TEXT, name_cn TEXT, aliases_json TEXT);
INSERT INTO bgm_subjects VALUES (42,'SUN×MOON','','[]');
CREATE TABLE bgm_terms (term_key TEXT, subject_id INTEGER, is_primary INTEGER, PRIMARY KEY(term_key,subject_id));
INSERT INTO bgm_terms VALUES ('sunmoon',42,1);
CREATE INDEX idx_bgm_terms_key ON bgm_terms(term_key);
CREATE TRIGGER reject_terms BEFORE INSERT ON bgm_terms BEGIN SELECT RAISE(ABORT,'test failure'); END;
`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upgradeArchiveTerms(ctx, db, true, true); err == nil {
		t.Fatal("expected insert failure")
	}
	var schema string
	var key string
	if err := db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='terms_schema_version'").Scan(&schema); err != nil || schema != "2" {
		t.Fatalf("old schema lost after failure: %q %v", schema, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT term_key FROM bgm_terms WHERE subject_id=42").Scan(&key); err != nil || key != "sunmoon" {
		t.Fatalf("old search term lost after failure: %q %v", key, err)
	}
	if _, err := db.ExecContext(ctx, "DROP TRIGGER reject_terms"); err != nil {
		t.Fatal(err)
	}
	if n, err := upgradeArchiveTerms(ctx, db, true, true); err != nil || n != 1 {
		t.Fatalf("retry: %d %v", n, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='terms_schema_version'").Scan(&schema); err != nil || schema != currentTermsSchemaVersion {
		t.Fatalf("new schema missing: %q %v", schema, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT term_key FROM bgm_terms WHERE term_key='sunxmoon'").Scan(&key); err != nil {
		t.Fatalf("new search term missing: %v", err)
	}
}
