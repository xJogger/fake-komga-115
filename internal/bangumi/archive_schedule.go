package bangumi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/diagnostics"
)

const (
	SettingArchiveSchedule = "bangumi_archive_schedule_json"

	ArchiveFreqWeekly  = "weekly"
	ArchiveFreqMonthly = "monthly"
)

type ArchiveSchedule struct {
	Enabled       bool   `json:"enabled"`
	Frequency     string `json:"frequency"`  // "weekly" | "monthly"
	DayOfWeek     int    `json:"dayOfWeek"`  // 0=Sun .. 6=Sat (default 3=Wed)
	DayOfMonth    int    `json:"dayOfMonth"` // 1..28 (default 1)
	TimeOfDay     string `json:"timeOfDay"`  // "HH:MM" in local server timezone (default "06:00")
	LastSlot      string `json:"lastSlot,omitempty"`
	LastCheckedAt string `json:"lastCheckedAt,omitempty"`
	LastResult    string `json:"lastResult,omitempty"` // "updated" | "up_to_date" | "failed" | "canceled"
	LastDumpName  string `json:"lastDumpName,omitempty"`
	LastError     string `json:"lastError,omitempty"`
	NextRunAt     string `json:"nextRunAt,omitempty"`
}

type ArchiveScheduleUpdate struct {
	Enabled    bool   `json:"enabled"`
	Frequency  string `json:"frequency"`
	DayOfWeek  int    `json:"dayOfWeek"`
	DayOfMonth int    `json:"dayOfMonth"`
	TimeOfDay  string `json:"timeOfDay"`
}

func DefaultArchiveSchedule() ArchiveSchedule {
	return ArchiveSchedule{
		Enabled:    false,
		Frequency:  ArchiveFreqWeekly,
		DayOfWeek:  int(time.Wednesday),
		DayOfMonth: 1,
		TimeOfDay:  "06:00",
	}
}

func ValidateArchiveSchedule(in ArchiveScheduleUpdate) (ArchiveSchedule, error) {
	out := DefaultArchiveSchedule()
	out.Enabled = in.Enabled
	freq := strings.ToLower(strings.TrimSpace(in.Frequency))
	if freq == "" {
		freq = ArchiveFreqWeekly
	}
	switch freq {
	case ArchiveFreqWeekly:
		if in.DayOfWeek < 0 || in.DayOfWeek > 6 {
			return ArchiveSchedule{}, errors.New("dayOfWeek must be between 0 (Sunday) and 6 (Saturday)")
		}
		out.Frequency = ArchiveFreqWeekly
		out.DayOfWeek = in.DayOfWeek
	case ArchiveFreqMonthly:
		if in.DayOfMonth < 1 || in.DayOfMonth > 28 {
			return ArchiveSchedule{}, errors.New("dayOfMonth must be between 1 and 28")
		}
		out.Frequency = ArchiveFreqMonthly
		out.DayOfMonth = in.DayOfMonth
	default:
		return ArchiveSchedule{}, errors.New("frequency must be weekly or monthly")
	}
	tod, err := database.ValidateTimeOfDay(in.TimeOfDay)
	if err != nil {
		return ArchiveSchedule{}, err
	}
	out.TimeOfDay = tod
	if in.DayOfWeek >= 0 && in.DayOfWeek <= 6 {
		out.DayOfWeek = in.DayOfWeek
	}
	if in.DayOfMonth >= 1 && in.DayOfMonth <= 28 {
		out.DayOfMonth = in.DayOfMonth
	}
	return out, nil
}

func parseTimeOfDayHM(tod string) (int, int) {
	var h, m int
	if _, err := fmt.Sscanf(strings.TrimSpace(tod), "%d:%d", &h, &m); err != nil {
		return 6, 0
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 6, 0
	}
	return h, m
}

// NextArchiveRunAt computes the next scheduled run time in the local timezone.
func NextArchiveRunAt(s ArchiveSchedule, now time.Time) (time.Time, bool) {
	if !s.Enabled {
		return time.Time{}, false
	}
	localNow := now.In(time.Local)
	hour, minute := parseTimeOfDayHM(s.TimeOfDay)
	switch s.Frequency {
	case ArchiveFreqMonthly:
		day := s.DayOfMonth
		if day < 1 || day > 28 {
			day = 1
		}
		cand := time.Date(localNow.Year(), localNow.Month(), day, hour, minute, 0, 0, time.Local)
		if !cand.After(localNow) {
			cand = time.Date(localNow.Year(), localNow.Month()+1, day, hour, minute, 0, 0, time.Local)
		}
		return cand, true
	default: // weekly
		dow := s.DayOfWeek
		if dow < 0 || dow > 6 {
			dow = int(time.Wednesday)
		}
		daysAhead := (dow - int(localNow.Weekday()) + 7) % 7
		cand := time.Date(localNow.Year(), localNow.Month(), localNow.Day()+daysAhead, hour, minute, 0, 0, time.Local)
		if !cand.After(localNow) {
			cand = cand.AddDate(0, 0, 7)
		}
		return cand, true
	}
}

func archiveScheduleDueSlot(s ArchiveSchedule, now time.Time) (string, bool) {
	if !s.Enabled {
		return "", false
	}
	localNow := now.In(time.Local)
	hour, minute := parseTimeOfDayHM(s.TimeOfDay)
	var scheduled time.Time
	switch s.Frequency {
	case ArchiveFreqMonthly:
		day := s.DayOfMonth
		if day < 1 || day > 28 {
			day = 1
		}
		if localNow.Day() != day {
			return "", false
		}
		scheduled = time.Date(localNow.Year(), localNow.Month(), day, hour, minute, 0, 0, time.Local)
	default:
		dow := s.DayOfWeek
		if dow < 0 || dow > 6 {
			dow = int(time.Wednesday)
		}
		if int(localNow.Weekday()) != dow {
			return "", false
		}
		scheduled = time.Date(localNow.Year(), localNow.Month(), localNow.Day(), hour, minute, 0, 0, time.Local)
	}
	// Fire within a 15-minute grace window after the scheduled minute (recovers from brief CPU stalls/restarts).
	diff := localNow.Sub(scheduled)
	if diff < 0 || diff >= 15*time.Minute {
		return "", false
	}
	slot := scheduled.Format("2006-01-02T15:04")
	if s.LastSlot == slot {
		return "", false
	}
	return slot, true
}

func (s *Service) ArchiveSchedule(ctx context.Context) ArchiveSchedule {
	sched := DefaultArchiveSchedule()
	if raw, err := s.store.GetSetting(ctx, SettingArchiveSchedule); err == nil && strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &sched)
	}
	if sched.Frequency != ArchiveFreqWeekly && sched.Frequency != ArchiveFreqMonthly {
		sched.Frequency = ArchiveFreqWeekly
	}
	if _, err := database.ValidateTimeOfDay(sched.TimeOfDay); err != nil {
		sched.TimeOfDay = "06:00"
	}
	if next, ok := NextArchiveRunAt(sched, s.now()); ok {
		sched.NextRunAt = next.Format(time.RFC3339)
	} else {
		sched.NextRunAt = ""
	}
	return sched
}

func (s *Service) UpdateArchiveSchedule(ctx context.Context, in ArchiveScheduleUpdate) (ArchiveSchedule, error) {
	validated, err := ValidateArchiveSchedule(in)
	if err != nil {
		return ArchiveSchedule{}, err
	}
	existing := s.ArchiveSchedule(ctx)
	validated.LastSlot = existing.LastSlot
	validated.LastCheckedAt = existing.LastCheckedAt
	validated.LastResult = existing.LastResult
	validated.LastDumpName = existing.LastDumpName
	validated.LastError = existing.LastError
	if err := s.saveArchiveSchedule(ctx, validated); err != nil {
		return ArchiveSchedule{}, err
	}
	if next, ok := NextArchiveRunAt(validated, s.now()); ok {
		validated.NextRunAt = next.Format(time.RFC3339)
	}
	diagnostics.Default().Record(diagnostics.Event{
		Level:     diagnostics.LevelInfo,
		Component: "bangumi",
		Stage:     "bangumi_archive",
		Action:    "schedule_save",
		Detail:    fmt.Sprintf("enabled=%v freq=%s time=%s", validated.Enabled, validated.Frequency, validated.TimeOfDay),
	})
	return validated, nil
}

func (s *Service) saveArchiveSchedule(ctx context.Context, sched ArchiveSchedule) error {
	toStore := sched
	toStore.NextRunAt = ""
	raw, err := json.Marshal(toStore)
	if err != nil {
		return err
	}
	return s.store.SetSetting(ctx, SettingArchiveSchedule, string(raw))
}

func (s *Service) recordArchiveUpdateOutcome(result, dumpName, errMsg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sched := s.ArchiveSchedule(ctx)
	sched.LastCheckedAt = s.now().UTC().Format(time.RFC3339)
	sched.LastResult = result
	if dumpName != "" {
		sched.LastDumpName = dumpName
	}
	sched.LastError = errMsg
	_ = s.saveArchiveSchedule(ctx, sched)
}

func (s *Service) archiveScheduleLoop() {
	defer close(s.schedDone)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	s.checkArchiveScheduleOnce()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.checkArchiveScheduleOnce()
		}
	}
}

func (s *Service) checkArchiveScheduleOnce() {
	now := s.now()
	sched := s.ArchiveSchedule(s.ctx)
	slot, due := archiveScheduleDueSlot(sched, now)
	if !due {
		return
	}
	sched.LastSlot = slot
	if err := s.saveArchiveSchedule(s.ctx, sched); err != nil {
		return
	}
	if _, err := s.archive.StartOnlineUpdateWithOptions(false); err != nil {
		if s.logger != nil {
			s.logger.Warn("scheduled Bangumi archive update skipped", "slot", slot, "error", err)
		}
		return
	}
	if s.logger != nil {
		s.logger.Info("scheduled Bangumi archive check started", "slot", slot)
	}
	diagnostics.Default().Record(diagnostics.Event{
		Level:     diagnostics.LevelInfo,
		Component: "bangumi",
		Stage:     "bangumi_archive",
		Action:    "schedule_trigger",
		Detail:    "triggered scheduled Bangumi offline archive check",
		Meta:      map[string]any{"trigger": "scheduled"},
	})
}
