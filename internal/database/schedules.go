package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func DefaultLibrarySchedule(libraryID string) LibrarySchedule {
	return LibrarySchedule{
		LibraryID:            libraryID,
		Enabled:              false,
		Frequency:            "daily",
		Weekday:              0,
		TimeOfDay:            "03:00",
		ScanMemberLibraryIDs: []string{},
		AutoBangumi:          true,
		AutoCoverLimit:       0,
	}
}

func ValidateTimeOfDay(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		return "", errors.New("timeOfDay must be in HH:MM format")
	}
	hour, err1 := strconv.Atoi(parts[0])
	minute, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return "", errors.New("timeOfDay must be between 00:00 and 23:59")
	}
	return fmt.Sprintf("%02d:%02d", hour, minute), nil
}

func (s *Store) LibrarySchedule(ctx context.Context, libraryID string) (LibrarySchedule, error) {
	var (
		item           LibrarySchedule
		enabledInt     int
		autoBangumiInt int
		membersJSON    string
	)
	err := s.db.QueryRowContext(ctx, `
SELECT library_id,enabled,frequency,weekday,time_of_day,scan_member_ids_json,
 auto_bangumi,auto_cover_limit,last_run_at,last_slot_key,updated_at
FROM library_schedules WHERE library_id=?`, libraryID).Scan(
		&item.LibraryID, &enabledInt, &item.Frequency, &item.Weekday, &item.TimeOfDay,
		&membersJSON, &autoBangumiInt, &item.AutoCoverLimit,
		&item.LastRunAt, &item.LastSlotKey, &item.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultLibrarySchedule(libraryID), nil
	}
	if err != nil {
		return LibrarySchedule{}, err
	}
	item.Enabled = enabledInt != 0
	item.AutoBangumi = autoBangumiInt != 0
	_ = json.Unmarshal([]byte(membersJSON), &item.ScanMemberLibraryIDs)
	if item.ScanMemberLibraryIDs == nil {
		item.ScanMemberLibraryIDs = []string{}
	}
	return item, nil
}

func (s *Store) LibrarySchedules(ctx context.Context) (map[string]LibrarySchedule, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT library_id,enabled,frequency,weekday,time_of_day,scan_member_ids_json,
 auto_bangumi,auto_cover_limit,last_run_at,last_slot_key,updated_at
FROM library_schedules`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]LibrarySchedule)
	for rows.Next() {
		var (
			item           LibrarySchedule
			enabledInt     int
			autoBangumiInt int
			membersJSON    string
		)
		if err := rows.Scan(
			&item.LibraryID, &enabledInt, &item.Frequency, &item.Weekday, &item.TimeOfDay,
			&membersJSON, &autoBangumiInt, &item.AutoCoverLimit,
			&item.LastRunAt, &item.LastSlotKey, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		item.Enabled = enabledInt != 0
		item.AutoBangumi = autoBangumiInt != 0
		_ = json.Unmarshal([]byte(membersJSON), &item.ScanMemberLibraryIDs)
		if item.ScanMemberLibraryIDs == nil {
			item.ScanMemberLibraryIDs = []string{}
		}
		out[item.LibraryID] = item
	}
	return out, rows.Err()
}

func (s *Store) UpsertLibrarySchedule(ctx context.Context, item LibrarySchedule) (LibrarySchedule, error) {
	item.LibraryID = strings.TrimSpace(item.LibraryID)
	if item.LibraryID == "" {
		return LibrarySchedule{}, errors.New("libraryId is required")
	}
	lib, err := s.LibraryBasic(ctx, item.LibraryID)
	if err != nil {
		return LibrarySchedule{}, err
	}
	freq := strings.ToLower(strings.TrimSpace(item.Frequency))
	if freq == "" {
		freq = "daily"
	}
	if freq != "daily" && freq != "weekly" {
		return LibrarySchedule{}, errors.New("frequency must be 'daily' or 'weekly'")
	}
	if item.Weekday < 0 || item.Weekday > 6 {
		return LibrarySchedule{}, errors.New("weekday must be between 0 (Sunday) and 6 (Saturday)")
	}
	tod, err := ValidateTimeOfDay(item.TimeOfDay)
	if err != nil {
		return LibrarySchedule{}, err
	}
	if item.AutoCoverLimit < -1 || item.AutoCoverLimit > 10000 {
		return LibrarySchedule{}, errors.New("autoCoverLimit must be -1 (unlimited), 0 (disabled), or 1..10000")
	}
	if item.AutoCoverLimit == -1 && lib.EffectiveProvider() != ProviderLocal && lib.EffectiveProvider() != ProviderUnion {
		return LibrarySchedule{}, errors.New("unlimited auto-cover is only supported for local/SMB or union libraries")
	}
	cleanMembers := cleanStringSliceCaseSensitive(item.ScanMemberLibraryIDs)
	if cleanMembers == nil {
		cleanMembers = []string{}
	}
	if lib.EffectiveProvider() == ProviderUnion && len(cleanMembers) > 0 {
		allowed := make(map[string]bool, len(lib.UnionLayers))
		for _, layer := range lib.UnionLayers {
			allowed[layer.MemberLibraryID] = true
		}
		filtered := make([]string, 0, len(cleanMembers))
		for _, mid := range cleanMembers {
			if allowed[mid] {
				filtered = append(filtered, mid)
			}
		}
		cleanMembers = filtered
	} else if lib.EffectiveProvider() != ProviderUnion {
		cleanMembers = []string{}
	}
	membersBytes, _ := json.Marshal(cleanMembers)
	enabledInt := 0
	if item.Enabled {
		enabledInt = 1
	}
	autoBgmInt := 0
	if item.AutoBangumi {
		autoBgmInt = 1
	}
	now := nowText()
	_, err = s.db.ExecContext(ctx, `
INSERT INTO library_schedules(
 library_id,enabled,frequency,weekday,time_of_day,scan_member_ids_json,
 auto_bangumi,auto_cover_limit,updated_at
) VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(library_id) DO UPDATE SET
 enabled=excluded.enabled,
 frequency=excluded.frequency,
 weekday=excluded.weekday,
 time_of_day=excluded.time_of_day,
 scan_member_ids_json=excluded.scan_member_ids_json,
 auto_bangumi=excluded.auto_bangumi,
 auto_cover_limit=excluded.auto_cover_limit,
 updated_at=excluded.updated_at`,
		item.LibraryID, enabledInt, freq, item.Weekday, tod, string(membersBytes),
		autoBgmInt, item.AutoCoverLimit, now)
	if err != nil {
		return LibrarySchedule{}, err
	}
	return s.LibrarySchedule(ctx, item.LibraryID)
}

func (s *Store) MarkScheduleSlotTriggered(ctx context.Context, libraryID, slotKey string) error {
	now := nowText()
	_, err := s.db.ExecContext(ctx, `
UPDATE library_schedules SET last_run_at=?,last_slot_key=?,updated_at=? WHERE library_id=?`,
		now, slotKey, now, libraryID)
	return err
}

func (s *Store) PipelineRuns(ctx context.Context, limit int) ([]PipelineRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT r.id,r.library_id,coalesce(l.name,''),r.trigger_type,r.status,r.stage,
 r.scan_run_ids_json,r.bangumi_run_id,r.cover_run_id,r.summary,r.error,
 r.cancel_requested,r.started_at,r.completed_at,r.created_at
FROM pipeline_runs r
LEFT JOIN libraries l ON l.id=r.library_id
ORDER BY r.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]PipelineRun, 0)
	for rows.Next() {
		item, err := scanPipelineRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) PipelineRunByID(ctx context.Context, id string) (PipelineRun, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT r.id,r.library_id,coalesce(l.name,''),r.trigger_type,r.status,r.stage,
 r.scan_run_ids_json,r.bangumi_run_id,r.cover_run_id,r.summary,r.error,
 r.cancel_requested,r.started_at,r.completed_at,r.created_at
FROM pipeline_runs r
LEFT JOIN libraries l ON l.id=r.library_id
WHERE r.id=?`, id)
	return scanPipelineRun(row)
}

func scanPipelineRun(scanner interface{ Scan(...any) error }) (PipelineRun, error) {
	var (
		item        PipelineRun
		scanIDsJSON string
		cancelInt   int
	)
	if err := scanner.Scan(
		&item.ID, &item.LibraryID, &item.LibraryName, &item.TriggerType, &item.Status, &item.Stage,
		&scanIDsJSON, &item.BangumiRunID, &item.CoverRunID, &item.Summary, &item.Error,
		&cancelInt, &item.StartedAt, &item.CompletedAt, &item.CreatedAt,
	); err != nil {
		return PipelineRun{}, err
	}
	item.CancelRequested = cancelInt != 0
	_ = json.Unmarshal([]byte(scanIDsJSON), &item.ScanRunIDs)
	if item.ScanRunIDs == nil {
		item.ScanRunIDs = []string{}
	}
	return item, nil
}
