package workitems

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"time"
)

// TicketSchedule is either a single delayed execution or a recurring template.
// Every is an elapsed duration, not a calendar or daylight-saving schedule.
type TicketSchedule struct {
	NotBefore time.Time `json:"not_before,omitempty" jsonschema:"Earliest execution time as an RFC3339 instant with timezone. Omit to start now."`
	Every     string    `json:"every,omitempty" jsonschema:"Repeat interval as a Go duration, at least 1m; empty means once."`
	Paused    bool      `json:"paused,omitempty"`
}

const pendingScheduleRun = `coalesce(json_extract(payload,'$.schedule_id'),'')<>'' AND coalesce(json_extract(payload,'$.deleted'),0)=0 AND json_extract(payload,'$.state')<>'done' AND coalesce(json_extract(payload,'$.execution_state'),'') NOT IN ('cancelled','rejected')`

const recurringScheduleQueue = `kind='ticket' AND coalesce(json_extract(payload,'$.deleted'),0)=0 AND coalesce(json_extract(payload,'$.schedule.every'),'')<>'' AND coalesce(json_extract(payload,'$.schedule.paused'),0)=0 AND json_extract(payload,'$.execution_state')='scheduled' AND json_extract(payload,'$.state')='todo'`

func normalizeTicketSchedule(kind string, in *TicketSchedule) (*TicketSchedule, error) {
	if in == nil {
		return nil, nil
	}
	if kind != Ticket {
		return nil, fmt.Errorf("%w: only tickets support schedules", ErrInvalidInput)
	}
	schedule := *in
	schedule.NotBefore = schedule.NotBefore.UTC()
	if !schedule.NotBefore.IsZero() && (schedule.NotBefore.Year() < 1 || schedule.NotBefore.Year() > 9998) {
		return nil, fmt.Errorf("%w: invalid schedule start time", ErrInvalidInput)
	}
	if schedule.Every != "" {
		interval, err := time.ParseDuration(schedule.Every)
		if err != nil || interval < time.Minute {
			return nil, fmt.Errorf("%w: schedule every must be a duration of at least 1m", ErrInvalidInput)
		}
		schedule.Every = interval.String()
	}
	if schedule.NotBefore.IsZero() && schedule.Every == "" && !schedule.Paused {
		return nil, nil
	}
	return &schedule, nil
}

func applyTicketSchedule(item *Item, schedule *TicketSchedule, now time.Time) error {
	if reflect.DeepEqual(item.Schedule, schedule) {
		return nil
	}
	if item.ActiveAttemptID != "" || item.ScheduleID != "" || item.State != "todo" {
		return fmt.Errorf("%w: scheduling can only change before execution or on a recurring template", ErrConflict)
	}
	previous := item.Schedule
	item.Schedule = schedule
	if schedule != nil && schedule.Every != "" {
		if previous == nil || previous.Every != schedule.Every || !previous.NotBefore.Equal(schedule.NotBefore) || item.NextScheduledAt.IsZero() {
			item.NextScheduledAt = schedule.NotBefore
			if item.NextScheduledAt.IsZero() {
				item.NextScheduledAt = now
			}
		}
		item.ExecutionState = "scheduled"
	} else {
		item.NextScheduledAt = time.Time{}
		if previous != nil && previous.Every != "" {
			item.ExecutionState = "queued"
		}
	}
	item.NextAssignmentAt = time.Time{}
	item.ExecutionReason = ""
	return nil
}

// TicketReadyAt is checked both before assignment and inside the fenced claim.
// Manual retry cannot bypass a future start time or a paused schedule.
func TicketReadyAt(item Item, now time.Time) bool {
	return item.Schedule == nil || !item.Schedule.Paused && item.Schedule.Every == "" && !now.Before(item.Schedule.NotBefore)
}

// A historical cancelled/rejected run must not be retried beside a newer run.
func requireExclusiveScheduleRun(ctx context.Context, tx *sql.Tx, item Item) error {
	if item.ScheduleID == "" {
		return nil
	}
	var pending bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM items INDEXED BY ticket_schedule_runs WHERE `+pendingScheduleRun+` AND json_extract(payload,'$.schedule_id')=? AND id<>? LIMIT 1)`, item.ScheduleID, item.ID).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return fmt.Errorf("%w: another run of this schedule is unresolved", ErrConflict)
	}
	return nil
}

// DispatchTicketSchedules commits each run and the next deadline together.
// The due index bounds each sweep to 100 definitions. Missed intervals coalesce
// to the latest occurrence and unresolved runs suppress overlapping execution.
func (s *Store) DispatchTicketSchedules(ctx context.Context, workspaceID string, now time.Time) (ItemPage, error) {
	if workspaceID == "" || now.IsZero() {
		return ItemPage{}, ErrInvalidInput
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM items INDEXED BY ticket_schedule_due WHERE `+recurringScheduleQueue+` AND workspace_id=? AND julianday(json_extract(payload,'$.next_scheduled_at'))<=julianday(?) ORDER BY julianday(json_extract(payload,'$.next_scheduled_at')),seq LIMIT 100`, workspaceID, stamp(now))
	if err != nil {
		return ItemPage{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return ItemPage{}, err
		}
		ids = append(ids, id)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return ItemPage{}, err
	}
	out := ItemPage{Items: []Item{}}
	for _, id := range ids {
		item, err := mutate(ctx, s, workspaceID, "schedule-dispatch:"+id, "", nil, func(tx *sql.Tx) (Item, error) {
			parent, err := ticketTx(ctx, tx, id)
			if err != nil {
				return Item{}, err
			}
			if parent.Schedule == nil || parent.Schedule.Every == "" || parent.Schedule.Paused || parent.State != "todo" || parent.ExecutionState != "scheduled" || now.Before(parent.NextScheduledAt) {
				return Item{}, nil
			}
			interval, err := time.ParseDuration(parent.Schedule.Every)
			if err != nil || interval < time.Minute {
				return Item{}, ErrInvalidInput
			}
			// Preserve the schedule's phase without overflowing a duration
			// when the original start is much older than the current time.
			phase := parent.NextScheduledAt.Sub(parent.NextScheduledAt.Truncate(interval))
			due := now.UTC().Truncate(interval).Add(phase)
			if due.After(now) {
				due = due.Add(-interval)
			}
			parent.NextScheduledAt = due.Add(interval)
			var pending int
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM items INDEXED BY ticket_schedule_runs WHERE `+pendingScheduleRun+` AND json_extract(payload,'$.schedule_id')=? LIMIT 1)`, id).Scan(&pending)
			if err != nil {
				return Item{}, err
			}
			var run Item
			if pending == 0 {
				body, err := decodeRow[Post](tx.QueryRowContext(ctx, `SELECT payload FROM posts WHERE id=?`, parent.BodyPostID))
				if err != nil {
					return Item{}, err
				}
				if body.Deleted {
					return Item{}, fmt.Errorf("%w: scheduled ticket body is deleted", ErrConflict)
				}
				run, err = createItemTx(ctx, tx, CreateInput{
					WorkspaceID: parent.WorkspaceID, Kind: Ticket, Title: parent.Title,
					Content: body.Content, Author: "schedule/" + id, Refs: body.Refs,
					Labels: parent.Labels, Acceptance: parent.Acceptance,
					ExecutionMode: parent.ExecutionMode, RequestedMemberID: parent.RequestedMemberID, LinkedIssueID: parent.LinkedIssueID,
					DependsOn: parent.DependsOn,
				}, id, due)
				if err != nil {
					return Item{}, err
				}
			}
			_, err = touchTicket(ctx, tx, parent)
			return run, err
		})
		if err != nil {
			return out, err
		}
		if item.ID != "" {
			out.Items = append(out.Items, item)
		}
	}
	return out, nil
}
