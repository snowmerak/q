# Event and file workflows

Use the same global `--session ID` (and chosen `--data-dir`, if any) for every
command in a workflow. Capitalized tokens below are placeholders for observed
IDs, paths, and page names. Parse JSON outcomes before taking the next action.
SDK payloads preserve exported field names, for example `Message`, `Type`,
`InputCompleted`, `GUID`, and `State`.

## Popup

1. `events arm --kind popup PAGE` returns `result.subscription`.
2. Trigger the intended popup once, such as `page click --ref REF PAGE`.
3. `events next SUBSCRIPTION` returns popup metadata and `result.handle`.
4. `popup adopt --name CHILD_PAGE HANDLE` attaches the observed popup to its
   opener's task. The CLI's popup expectation claims the first opener-verified
   popup for cleanup even if its event is not consumed.
5. Wait for the child document's actual condition, for example
   `page wait-selector --css SELECTOR CHILD_PAGE`. Arrival is not document load.
6. `events close SUBSCRIPTION` stops observation. It does not relinquish an
   already claimed popup; its task still owns cleanup.

Do not identify a popup by guessing that the newest browser target belongs to
the action. Popups without verified managed opener provenance are not claimed.

## JavaScript dialog

Arm `events arm --kind dialog PAGE` before the trigger. A trigger can return
`dialog_open` after dispatching input, or succeed before the modal event arrives.
Neither result calls for replaying that trigger.

Consume the armed subscription, inspect the dialog payload, and pass its typed
handle to `dialog accept HANDLE [PROMPT_TEXT]` or `dialog dismiss HANDLE` as the
task requires. If a modal was already open without a prepared subscription,
`dialog current PAGE` returns its current handle and info. Renderer commands
reject an open modal; CurrentDialog/handling is the recovery path.

Handled dialog IDs are retired. A second dialog needs its own observed handle,
even if its message is identical. Continue with an explicit page assertion, then
close the subscription. Follow the user's intended confirmation choice; merely
encountering a dialog does not authorize approving a new external action.

## Upload and chooser

For a known file input, use:

```text
page upload --ref REF PAGE ABSOLUTE_FILE ...
```

This supports hidden inputs and supported iframe/open-shadow targets. Multiple
files require a `multiple` input. Paths must identify existing regular files at
the same paths for Go and Chrome; the CLI does not upload bytes to a remote CDP
machine. Use only files supplied or otherwise authorized for the task. Omitting
file paths clears the selection; verify the page's resulting file state.

For an intercepted chooser:

1. `chooser interception PAGE on`.
2. `events arm --kind fileChooser PAGE`.
3. Trigger the chooser once, then `events next SUBSCRIPTION`.
4. `chooser set HANDLE ABSOLUTE_FILE ...`, or `chooser cancel HANDLE`.
5. Verify the page result, close the subscription, and turn interception off
   when finished with it.

Chooser handles are single-use and tied to their original document/session.
Navigation makes them stale. Cancel supplies an empty selection and can clear
previously selected files. New OOPIF sessions inherit interception when attached;
it cannot intercept an undiscovered frame retroactively.

## Download

`download enable DIRECTORY` is a deliberate policy change to the default browser
context, shared with other clients/sessions using that context. Use it when that
configuration change is within the task, with a suitable shared Go/Chrome
directory. Stop resets the policy to `default`, not another client's unknown
previous policy. Do not silently enable downloads just to observe an event.

1. `download enable DIRECTORY`, if not already configured as intended.
2. `events arm --kind download PAGE`.
3. Trigger the download once, then `events next SUBSCRIPTION`.
4. `download wait HANDLE` returns the GUID-named path after Chrome reports
   completion. `download info HANDLE` reports the current SDK download info.
5. `download save HANDLE DESTINATION` waits and copies to a new destination;
   it never overwrites a file, and the parent directory must exist.
6. Close the subscription and use `handle close HANDLE` when observation ends.
   Use `download cancel HANDLE` when cancellation is the intended outcome.

Treat a suggested filename as metadata, not a destination path. Select an
explicit destination from the task. `download_canceled` can mean cancellation or
transfer failure; a timeout alone is not download completion. Page/task cleanup
cancels active managed transfers. Completed files remain for the caller.

## Requests, timeouts, and handle lifetime

`events arm --kind request PAGE`, `requestFinished`, and `requestFailed` observe
network events independently of internal network-idle tracking. Request IDs are
scoped to the reported SDK `SessionID`; headers and bodies are not included.
Choose a specific DOM/application condition instead of assuming a request event
means the whole application finished.

Use a global deadline such as `--timeout 10s` before `events next SUBSCRIPTION`
or `download wait HANDLE`. A timed-out Next leaves its subscription active.
Close it explicitly when no longer needed. `event_overflow` is a lost-history
outcome; recheck current page state before starting a new observation.

Subscriptions and typed handles belong to one live control-process generation.
Do not reuse IDs after restart. The CLI bounds each registry at 1,024 entries,
with SDK subscription limits per page as well. Close unused subscriptions and
forget unneeded handles. `handle close` forgets observation only; task-owned
popups and active downloads remain governed by their task lifecycle.
