package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
)

var (
	ErrDuplicateName = errors.New("name already exists")
	ErrEmptyName     = errors.New("name cannot be empty")
)

func newOrganizationID(prefix string) string {
	var raw [12]byte
	_, _ = rand.Read(raw[:])
	return prefix + hex.EncodeToString(raw[:])
}

func (s *Store) ListCollections(
	ctx context.Context,
	search string,
	libraryIDs []string,
	page, size int,
	unpaged bool,
	sort string,
) ([]Collection, int64, error) {
	if page < 0 {
		page = 0
	}
	if size <= 0 || size > 500 {
		size = 20
	}
	var clauses []string
	var args []any
	if q := strings.TrimSpace(search); q != "" {
		clauses = append(clauses, `c.name LIKE ? ESCAPE '\' COLLATE NOCASE`)
		args = append(args, "%"+escapeLike(q)+"%")
	}
	if len(libraryIDs) > 0 {
		libs := cleanStringSliceCaseSensitive(libraryIDs)
		clauses = append(clauses, `EXISTS (SELECT 1 FROM collection_series cs JOIN series s ON s.id=cs.series_id WHERE cs.collection_id=c.id AND s.library_id IN `+Placeholders(len(libs))+`)`)
		for _, id := range libs {
			args = append(args, id)
		}
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM collections c`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "c.name COLLATE NOCASE ASC, c.id ASC"
	sortVal := strings.ToLower(strings.TrimSpace(sort))
	switch {
	case strings.HasPrefix(sortVal, "lastmodifieddate"):
		order = "c.updated_at " + sortDirection(sortVal) + ", c.name COLLATE NOCASE ASC, c.id ASC"
	case strings.HasPrefix(sortVal, "createddate"):
		order = "c.created_at " + sortDirection(sortVal) + ", c.name COLLATE NOCASE ASC, c.id ASC"
	case strings.HasPrefix(sortVal, "name"):
		order = "c.name COLLATE NOCASE " + sortDirection(sortVal) + ", c.id ASC"
	}
	query := `SELECT c.id,c.name,c.ordered,c.created_at,c.updated_at FROM collections c` + where + ` ORDER BY ` + order
	qArgs := append([]any(nil), args...)
	if !unpaged {
		query += ` LIMIT ? OFFSET ?`
		qArgs = append(qArgs, size, page*size)
	}
	rows, err := s.db.QueryContext(ctx, query, qArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]Collection, 0)
	for rows.Next() {
		var c Collection
		var ordered int
		var created, updated string
		if err := rows.Scan(&c.ID, &c.Name, &ordered, &created, &updated); err != nil {
			return nil, 0, err
		}
		c.Ordered = ordered != 0
		c.CreatedAt = parseTime(created)
		c.UpdatedAt = parseTime(updated)
		c.SeriesIDs = []string{}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := s.populateCollectionMembers(ctx, items); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *Store) CollectionByID(ctx context.Context, id string) (Collection, error) {
	var c Collection
	var ordered int
	var created, updated string
	err := s.db.QueryRowContext(ctx,
		`SELECT id,name,ordered,created_at,updated_at FROM collections WHERE id=?`, id,
	).Scan(&c.ID, &c.Name, &ordered, &created, &updated)
	if err != nil {
		return Collection{}, err
	}
	c.Ordered = ordered != 0
	c.CreatedAt = parseTime(created)
	c.UpdatedAt = parseTime(updated)
	c.SeriesIDs = []string{}
	items := []Collection{c}
	if err := s.populateCollectionMembers(ctx, items); err != nil {
		return Collection{}, err
	}
	return items[0], nil
}

func (s *Store) CollectionsForSeries(ctx context.Context, seriesID string) ([]Collection, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT c.id,c.name,c.ordered,c.created_at,c.updated_at
FROM collections c
JOIN collection_series cs ON cs.collection_id=c.id
WHERE cs.series_id=?
ORDER BY c.name COLLATE NOCASE ASC, c.id ASC`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Collection, 0)
	for rows.Next() {
		var c Collection
		var ordered int
		var created, updated string
		if err := rows.Scan(&c.ID, &c.Name, &ordered, &created, &updated); err != nil {
			return nil, err
		}
		c.Ordered = ordered != 0
		c.CreatedAt = parseTime(created)
		c.UpdatedAt = parseTime(updated)
		c.SeriesIDs = []string{}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.populateCollectionMembers(ctx, items); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) populateCollectionMembers(ctx context.Context, items []Collection) error {
	if len(items) == 0 {
		return nil
	}
	excludeNSFW := !s.ShowNSFW(ctx)
	idxByID := make(map[string]int, len(items))
	ids := make([]string, len(items))
	for i, item := range items {
		idxByID[item.ID] = i
		ids[i] = item.ID
	}
	for _, chunk := range chunkStrings(ids, 400) {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		query := `
SELECT cs.collection_id, cs.series_id, l.enabled, COALESCE(sm.is_nsfw, 0)
FROM collection_series cs
JOIN series s ON s.id=cs.series_id
JOIN libraries l ON l.id=s.library_id
LEFT JOIN series_metadata sm ON sm.series_id=s.id
WHERE cs.collection_id IN ` + Placeholders(len(chunk)) + `
ORDER BY cs.collection_id ASC, cs.position ASC`
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var cid, sid string
			var enabled, isNSFW int
			if err := rows.Scan(&cid, &sid, &enabled, &isNSFW); err != nil {
				rows.Close()
				return err
			}
			idx := idxByID[cid]
			if enabled == 0 || (excludeNSFW && isNSFW != 0) {
				items[idx].Filtered = true
				continue
			}
			items[idx].SeriesIDs = append(items[idx].SeriesIDs, sid)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	return nil
}

func (s *Store) CreateCollection(ctx context.Context, name string, ordered bool, seriesIDs []string) (Collection, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Collection{}, ErrEmptyName
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM collections WHERE name=? COLLATE NOCASE`, name).Scan(&exists); err != nil {
		return Collection{}, err
	}
	if exists > 0 {
		return Collection{}, ErrDuplicateName
	}
	id := newOrganizationID("c_")
	now := nowText()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Collection{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO collections(id,name,ordered,created_at,updated_at) VALUES(?,?,?,?,?)`,
		id, name, boolInt(ordered), now, now); err != nil {
		return Collection{}, err
	}
	if err := replaceCollectionSeriesTx(ctx, tx, id, seriesIDs); err != nil {
		return Collection{}, err
	}
	if err := tx.Commit(); err != nil {
		return Collection{}, err
	}
	return s.CollectionByID(ctx, id)
}

func (s *Store) UpdateCollection(
	ctx context.Context,
	id string,
	name *string,
	ordered *bool,
	seriesIDs *[]string,
) (Collection, error) {
	current, err := s.CollectionByID(ctx, id)
	if err != nil {
		return Collection{}, err
	}
	nextName := current.Name
	if name != nil {
		nextName = strings.TrimSpace(*name)
		if nextName == "" {
			return Collection{}, ErrEmptyName
		}
		if !strings.EqualFold(nextName, current.Name) {
			var exists int
			if err := s.db.QueryRowContext(ctx,
				`SELECT count(*) FROM collections WHERE name=? COLLATE NOCASE AND id<>?`, nextName, id).Scan(&exists); err != nil {
				return Collection{}, err
			}
			if exists > 0 {
				return Collection{}, ErrDuplicateName
			}
		}
	}
	nextOrdered := current.Ordered
	if ordered != nil {
		nextOrdered = *ordered
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Collection{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE collections SET name=?,ordered=?,updated_at=? WHERE id=?`,
		nextName, boolInt(nextOrdered), nowText(), id); err != nil {
		return Collection{}, err
	}
	if seriesIDs != nil {
		if err := replaceCollectionSeriesTx(ctx, tx, id, *seriesIDs); err != nil {
			return Collection{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Collection{}, err
	}
	return s.CollectionByID(ctx, id)
}

func (s *Store) DeleteCollection(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM collections WHERE id=?`, id)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func replaceCollectionSeriesTx(ctx context.Context, tx *sql.Tx, collectionID string, seriesIDs []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM collection_series WHERE collection_id=?`, collectionID); err != nil {
		return err
	}
	unique := cleanStringSliceCaseSensitive(seriesIDs)
	pos := 0
	for _, sid := range unique {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM series WHERE id=?`, sid).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO collection_series(collection_id,series_id,position) VALUES(?,?,?)`,
			collectionID, sid, pos); err != nil {
			return err
		}
		pos++
	}
	return nil
}

func (s *Store) CollectionSeriesPage(
	ctx context.Context,
	collectionID string,
	q SeriesQuery,
	unpaged bool,
) ([]Series, int64, error) {
	c, err := s.CollectionByID(ctx, collectionID)
	if err != nil {
		return nil, 0, err
	}
	if q.Page < 0 {
		q.Page = 0
	}
	if q.Size <= 0 || q.Size > 500 {
		q.Size = 20
	}
	if q.Empty {
		return nil, 0, nil
	}
	if q.NSFWMode == "" {
		if s.ShowNSFW(ctx) {
			q.NSFWMode = "all"
		} else {
			q.NSFWMode = "exclude"
		}
	}
	enabledLibs, err := s.enabledLibraryIDs(ctx)
	if err != nil {
		return nil, 0, err
	}
	where, args, empty := buildSeriesFilters(q, enabledLibs)
	if empty {
		return []Series{}, 0, nil
	}
	from := ` FROM collection_series cs JOIN series s ON s.id=cs.series_id`
	colFilter := `cs.collection_id=?`
	colArgs := append([]any{collectionID}, args...)
	if where == "" {
		where = " WHERE " + colFilter
	} else {
		where = " WHERE " + colFilter + " AND " + strings.TrimPrefix(where, " WHERE ")
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*)`+from+where, colArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := `COALESCE(NULLIF((SELECT sm.title_sort FROM series_metadata sm WHERE sm.series_id=s.id),''), s.name) COLLATE NOCASE ASC, s.id ASC`
	if c.Ordered && strings.TrimSpace(q.Sort) == "" {
		order = `cs.position ASC, s.id ASC`
	} else if strings.TrimSpace(q.Sort) != "" {
		sortVal := strings.ToLower(strings.TrimSpace(q.Sort))
		dir := sortDirection(sortVal)
		switch {
		case strings.HasPrefix(sortVal, "lastmodifieddate"):
			order = `s.sort_modified_at ` + dir + `, s.name COLLATE NOCASE ASC, s.id ASC`
		case strings.HasPrefix(sortVal, "createddate"):
			order = `s.sort_created_at ` + dir + `, s.name COLLATE NOCASE ASC, s.id ASC`
		case strings.HasPrefix(sortVal, "bgmrank"):
			order = `(SELECT sm.bgm_rank FROM series_metadata sm WHERE sm.series_id=s.id AND sm.bgm_rank>0) ` + dir + ` NULLS LAST, s.name COLLATE NOCASE ASC, s.id ASC`
		case strings.Contains(sortVal, "random"):
			order = `RANDOM()`
		default:
			order = `COALESCE(NULLIF((SELECT sm.title_sort FROM series_metadata sm WHERE sm.series_id=s.id),''), s.name) COLLATE NOCASE ` + dir + `, s.id ` + dir
		}
	}
	query := `
SELECT s.id,s.library_id,s.cid,s.name,s.relative_path,s.one_shot,s.file_modified_at,s.created_at,s.updated_at,s.books_count` +
		from + where + ` ORDER BY ` + order
	qArgs := append([]any(nil), colArgs...)
	if !unpaged {
		query += ` LIMIT ? OFFSET ?`
		qArgs = append(qArgs, q.Size, q.Page*q.Size)
	}
	rows, err := s.db.QueryContext(ctx, query, qArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items, err := scanSeriesRows(rows)
	return items, total, err
}

func (s *Store) CollectionFirstVisibleSeriesID(ctx context.Context, collectionID string) (string, error) {
	query := `
SELECT cs.series_id
FROM collection_series cs
JOIN series s ON s.id=cs.series_id
JOIN libraries l ON l.id=s.library_id AND l.enabled=1
WHERE cs.collection_id=?`
	if !s.ShowNSFW(ctx) {
		query += ` AND s.id NOT IN (SELECT series_id FROM series_metadata WHERE is_nsfw=1)`
	}
	query += ` ORDER BY cs.position ASC LIMIT 1`
	var sid string
	err := s.db.QueryRowContext(ctx, query, collectionID).Scan(&sid)
	return sid, err
}

// --- ReadLists ---

func (s *Store) ListReadLists(
	ctx context.Context,
	search string,
	libraryIDs []string,
	page, size int,
	unpaged bool,
	sort string,
) ([]ReadList, int64, error) {
	if page < 0 {
		page = 0
	}
	if size <= 0 || size > 500 {
		size = 20
	}
	var clauses []string
	var args []any
	if q := strings.TrimSpace(search); q != "" {
		clauses = append(clauses, `r.name LIKE ? ESCAPE '\' COLLATE NOCASE`)
		args = append(args, "%"+escapeLike(q)+"%")
	}
	if len(libraryIDs) > 0 {
		libs := cleanStringSliceCaseSensitive(libraryIDs)
		clauses = append(clauses, `EXISTS (SELECT 1 FROM readlist_books rb JOIN books b ON b.id=rb.book_id WHERE rb.readlist_id=r.id AND b.library_id IN `+Placeholders(len(libs))+`)`)
		for _, id := range libs {
			args = append(args, id)
		}
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM readlists r`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "r.name COLLATE NOCASE ASC, r.id ASC"
	sortVal := strings.ToLower(strings.TrimSpace(sort))
	switch {
	case strings.HasPrefix(sortVal, "lastmodifieddate"):
		order = "r.updated_at " + sortDirection(sortVal) + ", r.name COLLATE NOCASE ASC, r.id ASC"
	case strings.HasPrefix(sortVal, "createddate"):
		order = "r.created_at " + sortDirection(sortVal) + ", r.name COLLATE NOCASE ASC, r.id ASC"
	case strings.HasPrefix(sortVal, "name"):
		order = "r.name COLLATE NOCASE " + sortDirection(sortVal) + ", r.id ASC"
	}
	query := `SELECT r.id,r.name,r.summary,r.ordered,r.created_at,r.updated_at FROM readlists r` + where + ` ORDER BY ` + order
	qArgs := append([]any(nil), args...)
	if !unpaged {
		query += ` LIMIT ? OFFSET ?`
		qArgs = append(qArgs, size, page*size)
	}
	rows, err := s.db.QueryContext(ctx, query, qArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]ReadList, 0)
	for rows.Next() {
		var rl ReadList
		var ordered int
		var created, updated string
		if err := rows.Scan(&rl.ID, &rl.Name, &rl.Summary, &ordered, &created, &updated); err != nil {
			return nil, 0, err
		}
		rl.Ordered = ordered != 0
		rl.CreatedAt = parseTime(created)
		rl.UpdatedAt = parseTime(updated)
		rl.BookIDs = []string{}
		items = append(items, rl)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := s.populateReadListMembers(ctx, items); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *Store) ReadListByID(ctx context.Context, id string) (ReadList, error) {
	var rl ReadList
	var ordered int
	var created, updated string
	err := s.db.QueryRowContext(ctx,
		`SELECT id,name,summary,ordered,created_at,updated_at FROM readlists WHERE id=?`, id,
	).Scan(&rl.ID, &rl.Name, &rl.Summary, &ordered, &created, &updated)
	if err != nil {
		return ReadList{}, err
	}
	rl.Ordered = ordered != 0
	rl.CreatedAt = parseTime(created)
	rl.UpdatedAt = parseTime(updated)
	rl.BookIDs = []string{}
	items := []ReadList{rl}
	if err := s.populateReadListMembers(ctx, items); err != nil {
		return ReadList{}, err
	}
	return items[0], nil
}

func (s *Store) ReadListsForBook(ctx context.Context, bookID string) ([]ReadList, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT r.id,r.name,r.summary,r.ordered,r.created_at,r.updated_at
FROM readlists r
JOIN readlist_books rb ON rb.readlist_id=r.id
WHERE rb.book_id=?
ORDER BY r.name COLLATE NOCASE ASC, r.id ASC`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ReadList, 0)
	for rows.Next() {
		var rl ReadList
		var ordered int
		var created, updated string
		if err := rows.Scan(&rl.ID, &rl.Name, &rl.Summary, &ordered, &created, &updated); err != nil {
			return nil, err
		}
		rl.Ordered = ordered != 0
		rl.CreatedAt = parseTime(created)
		rl.UpdatedAt = parseTime(updated)
		rl.BookIDs = []string{}
		items = append(items, rl)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.populateReadListMembers(ctx, items); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) populateReadListMembers(ctx context.Context, items []ReadList) error {
	if len(items) == 0 {
		return nil
	}
	excludeNSFW := !s.ShowNSFW(ctx)
	idxByID := make(map[string]int, len(items))
	ids := make([]string, len(items))
	for i, item := range items {
		idxByID[item.ID] = i
		ids[i] = item.ID
	}
	for _, chunk := range chunkStrings(ids, 400) {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		query := `
SELECT rb.readlist_id, rb.book_id, l.enabled, COALESCE(sm.is_nsfw, 0)
FROM readlist_books rb
JOIN books b ON b.id=rb.book_id
JOIN libraries l ON l.id=b.library_id
LEFT JOIN series_metadata sm ON sm.series_id=b.series_id
WHERE rb.readlist_id IN ` + Placeholders(len(chunk)) + `
ORDER BY rb.readlist_id ASC, rb.position ASC`
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var rid, bid string
			var enabled, isNSFW int
			if err := rows.Scan(&rid, &bid, &enabled, &isNSFW); err != nil {
				rows.Close()
				return err
			}
			idx := idxByID[rid]
			if enabled == 0 || (excludeNSFW && isNSFW != 0) {
				items[idx].Filtered = true
				continue
			}
			items[idx].BookIDs = append(items[idx].BookIDs, bid)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	return nil
}

func (s *Store) CreateReadList(ctx context.Context, name, summary string, ordered bool, bookIDs []string) (ReadList, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ReadList{}, ErrEmptyName
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM readlists WHERE name=? COLLATE NOCASE`, name).Scan(&exists); err != nil {
		return ReadList{}, err
	}
	if exists > 0 {
		return ReadList{}, ErrDuplicateName
	}
	id := newOrganizationID("r_")
	now := nowText()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReadList{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO readlists(id,name,summary,ordered,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		id, name, strings.TrimSpace(summary), boolInt(ordered), now, now); err != nil {
		return ReadList{}, err
	}
	if err := replaceReadListBooksTx(ctx, tx, id, bookIDs); err != nil {
		return ReadList{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReadList{}, err
	}
	return s.ReadListByID(ctx, id)
}

func (s *Store) UpdateReadList(
	ctx context.Context,
	id string,
	name *string,
	summary *string,
	ordered *bool,
	bookIDs *[]string,
) (ReadList, error) {
	current, err := s.ReadListByID(ctx, id)
	if err != nil {
		return ReadList{}, err
	}
	nextName := current.Name
	if name != nil {
		nextName = strings.TrimSpace(*name)
		if nextName == "" {
			return ReadList{}, ErrEmptyName
		}
		if !strings.EqualFold(nextName, current.Name) {
			var exists int
			if err := s.db.QueryRowContext(ctx,
				`SELECT count(*) FROM readlists WHERE name=? COLLATE NOCASE AND id<>?`, nextName, id).Scan(&exists); err != nil {
				return ReadList{}, err
			}
			if exists > 0 {
				return ReadList{}, ErrDuplicateName
			}
		}
	}
	nextSummary := current.Summary
	if summary != nil {
		nextSummary = strings.TrimSpace(*summary)
	}
	nextOrdered := current.Ordered
	if ordered != nil {
		nextOrdered = *ordered
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReadList{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE readlists SET name=?,summary=?,ordered=?,updated_at=? WHERE id=?`,
		nextName, nextSummary, boolInt(nextOrdered), nowText(), id); err != nil {
		return ReadList{}, err
	}
	if bookIDs != nil {
		if err := replaceReadListBooksTx(ctx, tx, id, *bookIDs); err != nil {
			return ReadList{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ReadList{}, err
	}
	return s.ReadListByID(ctx, id)
}

func (s *Store) DeleteReadList(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM readlists WHERE id=?`, id)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func replaceReadListBooksTx(ctx context.Context, tx *sql.Tx, readListID string, bookIDs []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM readlist_books WHERE readlist_id=?`, readListID); err != nil {
		return err
	}
	unique := cleanStringSliceCaseSensitive(bookIDs)
	pos := 0
	for _, bid := range unique {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM books WHERE id=?`, bid).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO readlist_books(readlist_id,book_id,position) VALUES(?,?,?)`,
			readListID, bid, pos); err != nil {
			return err
		}
		pos++
	}
	return nil
}

func (s *Store) ReadListBooksPage(
	ctx context.Context,
	readListID string,
	q BookQuery,
) ([]Book, int64, error) {
	rl, err := s.ReadListByID(ctx, readListID)
	if err != nil {
		return nil, 0, err
	}
	if q.Page < 0 {
		q.Page = 0
	}
	if q.Size <= 0 || q.Size > 1000 {
		q.Size = 20
	}
	if q.Empty {
		return nil, 0, nil
	}
	enabledLibs, err := s.enabledLibraryIDs(ctx)
	if err != nil {
		return nil, 0, err
	}
	where, args, empty := buildBookFilters(q, enabledLibs, !s.ShowNSFW(ctx))
	if empty {
		return []Book{}, 0, nil
	}
	from := ` FROM readlist_books rb JOIN books b ON b.id=rb.book_id`
	rlArgs := append([]any{readListID}, args...)
	if where == "" {
		where = ` WHERE rb.readlist_id=?`
	} else {
		where = ` WHERE rb.readlist_id=? AND ` + strings.TrimPrefix(where, " WHERE ")
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*)`+from+where, rlArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := `rb.position ASC, b.id ASC`
	if !rl.Ordered && strings.TrimSpace(q.Sort) == "" {
		order = `b.name COLLATE NOCASE ASC, b.id ASC`
	} else if strings.TrimSpace(q.Sort) != "" {
		sortVal := strings.ToLower(strings.TrimSpace(q.Sort))
		switch {
		case strings.HasPrefix(sortVal, "lastmodifieddate"):
			order = `COALESCE(b.file_modified_at,b.updated_at) ` + sortDirection(sortVal) + `, rb.position ASC`
		case strings.HasPrefix(sortVal, "createddate"):
			order = `COALESCE(b.file_created_at,b.created_at) ` + sortDirection(sortVal) + `, rb.position ASC`
		case strings.HasPrefix(sortVal, "metadata.title"):
			order = `b.name COLLATE NOCASE ` + sortDirection(sortVal) + `, b.id ASC`
		}
	}
	query := `
SELECT b.id,b.series_id,b.library_id,b.file_id,b.parent_cid,b.name,b.size,b.pick_code,b.sha1,
 b.file_created_at,b.file_modified_at,b.number_sort,b.created_at,b.updated_at,
 coalesce(z.page_count,0)` + from + ` LEFT JOIN zip_indexes z ON z.book_id=b.id` + where + ` ORDER BY ` + order
	qArgs := append([]any(nil), rlArgs...)
	if !q.Unpaged {
		query += ` LIMIT ? OFFSET ?`
		qArgs = append(qArgs, q.Size, q.Page*q.Size)
	}
	rows, err := s.db.QueryContext(ctx, query, qArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items, err := scanBookRows(rows)
	return items, total, err
}

func (s *Store) ReadListSiblingBook(ctx context.Context, readListID, bookID string, next bool) (Book, error) {
	rl, err := s.ReadListByID(ctx, readListID)
	if err != nil {
		return Book{}, err
	}
	idx := -1
	for i, bid := range rl.BookIDs {
		if bid == bookID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Book{}, sql.ErrNoRows
	}
	target := idx - 1
	if next {
		target = idx + 1
	}
	if target < 0 || target >= len(rl.BookIDs) {
		return Book{}, sql.ErrNoRows
	}
	return s.BookByID(ctx, rl.BookIDs[target])
}

func (s *Store) ReadListFirstVisibleSeriesID(ctx context.Context, readListID string) (string, error) {
	query := `
SELECT b.series_id
FROM readlist_books rb
JOIN books b ON b.id=rb.book_id
JOIN libraries l ON l.id=b.library_id AND l.enabled=1
WHERE rb.readlist_id=?`
	if !s.ShowNSFW(ctx) {
		query += ` AND b.series_id NOT IN (SELECT series_id FROM series_metadata WHERE is_nsfw=1)`
	}
	query += ` ORDER BY rb.position ASC LIMIT 1`
	var sid string
	err := s.db.QueryRowContext(ctx, query, readListID).Scan(&sid)
	return sid, err
}
