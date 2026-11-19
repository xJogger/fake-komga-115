package bangumi

import (
	"context"

	"github.com/xJogger/fake-komga-115/internal/database"
)

// refreshLocalRanks copies ranking metadata only, without re-binding series
// or touching any user metadata, progress, or archive contents.
func (s *Service) refreshLocalRanks(ctx context.Context) error {
	a := s.archive
	a.mu.RLock()
	db := a.db
	a.mu.RUnlock()
	if db == nil {
		return nil
	}
	rows, err := s.store.DB().QueryContext(ctx, "SELECT DISTINCT bgm_subject_id FROM series_metadata WHERE bgm_subject_id>0")
	if err != nil {
		return err
	}
	var matches []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		matches = append(matches, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for start := 0; start < len(matches); start += 250 {
		ids := matches[start:min(start+250, len(matches))]
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		rows, err := db.QueryContext(ctx, "SELECT id,rank FROM bgm_subjects WHERE rank>0 AND id IN "+database.Placeholders(len(ids)), args...)
		if err != nil {
			return err
		}
		ranks := make(map[int64]int, len(ids))
		for rows.Next() {
			var id int64
			var rank int
			if err := rows.Scan(&id, &rank); err != nil {
				rows.Close()
				return err
			}
			ranks[id] = rank
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(ranks) != 0 {
			if err := s.writeRanks(ctx, ranks); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) writeRanks(ctx context.Context, ranks map[int64]int) error {
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, "UPDATE series_metadata SET bgm_rank=? WHERE bgm_subject_id=? AND bgm_rank IS NOT ?")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for id, rank := range ranks {
		if _, err := stmt.ExecContext(ctx, rank, id, rank); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) rankWorker(pending <-chan struct{}) {
	defer close(s.rankDone)
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-pending:
			if err := s.refreshLocalRanks(s.ctx); err != nil && s.ctx.Err() == nil && s.logger != nil {
				s.logger.Warn("refresh offline Bangumi ranks", "error", err)
			}
		}
	}
}
