# Architecture

`sj` is a short-lived controller. `systemd --user` is the only authority for
unit state and process supervision; journald is the only authority for output.
There is no `sj` daemon, scheduler, socket, database, or log file.

Finite job allocation calls `StartTransientUnit` with job mode `fail` for each
slot in ascending order. A unit-name collision advances to the next slot;
other errors stop allocation. This is safe when multiple CLI processes start
jobs concurrently because systemd performs the create/start decision.
Successful and failed finite jobs remain loaded and reserve their slots until
an explicit `clean` or `reset` operation releases them. `run/exec --collect`
(`-c`) opts successful jobs into automatic collection.

Delayed jobs start a transient timer and its corresponding service as one
D-Bus transaction using an auxiliary transient unit. The timer reserves the
slot while it waits, so a later command cannot reuse its service name.
The timer and service share a basename, which determines their association.
Only a waiting timer reports `scheduled`; once triggered, the service's state
determines the job's state. Elapsed timers are released after their service
deactivates so collected jobs can release their slots.

Startup subscribes to `JobRemoved` before submitting `StartTransientUnit` and
matches the returned job path, including signals received before the method
reply. Only `done` reports success; failed or timed-out startup is an error.
A private D-Bus connection owns the subscription and closes on every exit.

Logs are queried with `journalctl --user` and, when available, the unit's
`InvocationID` is passed as `_SYSTEMD_INVOCATION_ID`. This isolates repeated
uses of the same numeric slot without parsing human-readable journal messages.

Units are namespaced by the configured prefix. Bulk cleanup only considers
`<prefix>-run-*` and optionally `<prefix>-svc-*`; it never targets arbitrary
user units.

Run regression tests with `go test ./...`. They use a private `dbus-daemon`
when available. Opt into real user-manager tests with
`SJ_SYSTEMD_TEST=1 go test ./...`; these create transient units under a unique
`sjtest-<pid>-<timestamp>` prefix and clean up their own units.
