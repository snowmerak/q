# Ticket schedules

Tickets can delay their first execution or define a repeating task. Configure
**Schedule execution**, **Available from**, and **Repeat every** in Studio's
ticket creation/details form. The existing requested-member selection applies to
scheduled work too; System One still checks suitability before assigning it.

The start field uses the browser's displayed time zone and is sent as an RFC3339
instant. An empty repeat interval runs the original ticket once, no earlier than
the start. Repeats accept Go durations of at least one minute, such as `30m`, `2h`,
or `24h`. These are elapsed intervals; `24h` does not mean a calendar-time rule
across daylight-saving changes. A repeating schedule may omit its start to begin
when saved. Pause stops future occurrences; previously issued run tickets continue.

The Library is the durable source. The existing coordinator checks due schedules
on its two-second sweep, before ordinary assignment. Q's coordinator host must be
running; the browser tab may be closed. Work remains subject to project/global
capacity limits and suitable joined members, so a start time is an earliest
eligible time, not a guaranteed launch deadline.

```json
{
  "schedule": {
    "not_before": "2026-11-06T09:00:00+09:00",
    "every": "24h",
    "paused": false
  }
}
```

`ticket_create` and `ticket_update` expose the same optional `schedule` object
through the native and ACP MCP tools. Omitting it in an update preserves the
current schedule. An empty object removes scheduling and returns a template to
ordinary immediate assignment. Changes are supported before execution and on
recurring templates; active runs retain their saved work contract.

A recurring parent remains a template, with `execution_state: scheduled` and a
durable `next_scheduled_at`. Each occurrence creates a separate queued ticket
with `schedule_id` and `scheduled_for`. The body, acceptance criteria, labels,
execution mode, linked issue and requested member are copied from the current
template. Each child has its own thread, worker attempt and normal manager review.
Studio links a generated run back to its original schedule.

Creation and advancement happen in one source transaction. Repeating a sweep or
restarting cannot issue the same occurrence twice. Missed intervals coalesce to
the latest due occurrence. An unresolved child prevents another run; completion,
cancellation, or rejection releases the next occurrence. Failed, blocked or
unknown runs require resolution rather than automatic replay. Skipped intervals
do not accumulate a backlog. Editing a template does not rewrite earlier runs.

Assignment, due schedules and unresolved runs use partial SQLite indexes; the
sweep does not deserialize completed ticket history. Catalog schema version 8
prevents older coordinators from opening it and ignoring the new start/pause
constraints. Existing unscheduled tickets retain their automatic assignment.
