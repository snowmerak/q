# Ticket dependencies

Choose **Prerequisite tickets** when creating a Studio ticket or editing its
details before execution. Select one or more tickets in the same project.
Search the loaded ticket titles/numbers; **More prerequisite tickets** loads
another page. The detail view links to each prerequisite and shows which ones
are still waiting for completion. Independent tickets can run concurrently.

A ticket becomes eligible only after **all** prerequisites reach `done`.
Worker success, review waiting, failure, cancellation and rejection do not
satisfy a dependency. Report tickets reach `done` after Integrator acceptance;
Git tickets reach it after the reviewed merge succeeds and is recorded in
Library. A failed or cancelled prerequisite can be retried; its later
completion releases dependents automatically. Waiting tickets consume no worker
reservation or assignment-model call.

Native Q and ACP use the same optional field in `ticket_create`:

```json
{
  "title": "Implement the approved plan",
  "content": "Read the prerequisite ticket's result before implementing.",
  "execution_mode": "git",
  "depends_on": ["<planning-ticket-id>"]
}
```

`ticket_update` omits `depends_on` to preserve the current relationships and
uses `[]` to remove them. Select **Remove** in the editor to remove a selected
prerequisite. Reads expose `depends_on` and a derived `waiting_on` list; the
waiting list reflects current source state without changing the ticket revision.
Up to 64 direct prerequisites are supported. Self references, duplicates,
cycles, deleted tickets, issues and tickets from other projects are rejected.

Relationships are editable before the first execution and on recurring
templates. Active and historical attempts retain their work contract. Each
recurring run copies its template's current dependencies. A recurring template
cannot be a prerequisite because it never reaches `done`; choose a concrete run
ticket instead. Start-time, pause and existing capacity limits also apply.

An unfinished prerequisite cannot be deleted or converted into a recurring
template while unfinished tickets reference it. Remove those relationships
first. A completed ticket may be deleted; its retained `done` source record
continues to satisfy existing dependents.

Library commits dependency edges with ticket metadata and rejects a cyclic
update atomically. It filters the assignment queue by readiness and checks
again in the fenced reservation transaction. Relationships survive restart;
existing tickets start with no prerequisites. Catalog schema 9 and Library
protocol 13 prevent older coordinators from ignoring dependencies. Restart
Studio/Library using the updated Q binary when upgrading.
