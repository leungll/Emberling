-- Execution-ledger invariant report for one Run.
--
-- Usage: psql -v ON_ERROR_STOP=1 -v run_id=<run id> -f invariants.sql
-- (scripts/demo/invariants.sh runs it inside the Compose postgres container).
--
-- PostgreSQL is the authority for every Emberling-owned execution fact, so this report
-- reads only committed rows: no API projection, process memory or SSE stream is consulted.
-- It runs in one REPEATABLE READ, READ ONLY transaction that is rolled back, so every
-- check sees the same snapshot and the report can never change what it inspects.
--
-- Output: one "PASS|FAIL <check> violations=<n>" line per check, each FAIL followed by
-- its offending rows indented underneath.

\set QUIET on
\set ON_ERROR_STOP on
\pset format unaligned
\pset tuples_only on
\pset footer off

BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;

\echo 'checks run:'
\echo '  run_exists                         the Run row is present'
\echo '  event_seq_contiguous               Event seq is 1..N with no gap, no duplicate, and equals runs.last_seq'
\echo '  transition_has_event               every recorded state transition has its Event in the same Run'
\echo '  no_inflight_work_in_terminal_run   a terminal Run has no RUNNING/WAITING NodeRun, Turn or Action and no STARTED/DISPATCHED Attempt'
\echo '  run_status_matches_noderuns        the Run status is the one its NodeRuns derive'
\echo '  one_external_task_per_attempt      no Attempt is bound to two external tasks, no external task id is bound twice'
\echo '  at_most_one_succeeded_attempt      no NodeRun or Agent Action has two SUCCEEDED Attempts'
\echo 'checks not run: none of the requested checks is unsupported by the schema'
\echo ''

WITH
target AS (
    SELECT r.* FROM runs r WHERE r.id = :'run_id'
),
run_events AS (
    SELECT e.* FROM events e WHERE e.run_id = :'run_id'
),
run_node_runs AS (
    SELECT nr.* FROM node_runs nr WHERE nr.run_id = :'run_id'
),
run_attempts AS (
    SELECT a.*, nr.node_id
    FROM node_attempts a JOIN run_node_runs nr ON nr.id = a.node_run_id
),
run_agent_turns AS (
    SELECT t.*, nr.node_id
    FROM agent_turns t
    JOIN agent_runs ar ON ar.id = t.agent_run_id
    JOIN run_node_runs nr ON nr.id = ar.node_run_id
),
run_agent_actions AS (
    SELECT ac.*, t.node_id, t.turn_no
    FROM agent_actions ac JOIN run_agent_turns t ON t.id = ac.turn_id
),
run_tool_attempts AS (
    SELECT ta.*, ac.node_id, ac.turn_no
    FROM tool_attempts ta JOIN run_agent_actions ac ON ac.id = ta.action_id
),

-- The Run must exist; every other check is vacuous without it.
v_run_exists AS (
    SELECT 'no runs row for ' || :'run_id' AS detail
    WHERE NOT EXISTS (SELECT 1 FROM target)
),

-- Event seq numbers are allocated under the Run aggregate lock in the same transaction as
-- the state change they describe, so a committed ledger is 1..N with no gap and no
-- duplicate, and the Run's seq watermark equals its highest committed seq. A gap would
-- mean an Event was lost or a transaction that allocated seq committed partially.
v_event_seq AS (
    SELECT 'missing seq ' || g AS detail
    FROM target t, generate_series(1, (SELECT coalesce(max(seq), 0) FROM run_events)) g
    WHERE NOT EXISTS (SELECT 1 FROM run_events e WHERE e.seq = g)
    UNION ALL
    SELECT 'seq ' || seq || ' appears ' || count(*) || ' times'
    FROM run_events GROUP BY seq HAVING count(*) > 1
    UNION ALL
    SELECT 'runs.last_seq=' || t.last_seq || ' but max(events.seq)=' || coalesce(m.max_seq, 0)
    FROM target t, (SELECT max(seq) AS max_seq FROM run_events) m
    WHERE t.last_seq <> coalesce(m.max_seq, 0)
),

-- A state transition and the Event that announces it commit in the same transaction, so
-- no committed state may lack its Event. Each row below is a persisted fact whose Event
-- is missing from this Run's ledger.
v_transition_event AS (
    SELECT 'run has no RUN_CREATED' AS detail
    FROM target WHERE NOT EXISTS (SELECT 1 FROM run_events WHERE type = 'RUN_CREATED')
    UNION ALL
    SELECT 'run ' || status || ' without RUN_COMPLETED'
    FROM target WHERE status = 'COMPLETED'
      AND NOT EXISTS (SELECT 1 FROM run_events WHERE type = 'RUN_COMPLETED')
    UNION ALL
    SELECT 'run ' || status || ' without RUN_FAILED'
    FROM target WHERE status = 'FAILED'
      AND NOT EXISTS (SELECT 1 FROM run_events WHERE type = 'RUN_FAILED')
    UNION ALL
    SELECT nr.node_id || ' has no NODE_READY'
    FROM run_node_runs nr
    WHERE NOT EXISTS (SELECT 1 FROM run_events e WHERE e.node_run_id = nr.id AND e.type = 'NODE_READY')
    UNION ALL
    SELECT nr.node_id || ' is ' || nr.status || ' without NODE_STARTED'
    FROM run_node_runs nr
    WHERE nr.status <> 'READY'
      AND NOT EXISTS (SELECT 1 FROM run_events e WHERE e.node_run_id = nr.id AND e.type = 'NODE_STARTED')
    UNION ALL
    SELECT nr.node_id || ' is SUCCEEDED without NODE_COMPLETED'
    FROM run_node_runs nr
    WHERE nr.status = 'SUCCEEDED'
      AND NOT EXISTS (SELECT 1 FROM run_events e WHERE e.node_run_id = nr.id AND e.type = 'NODE_COMPLETED')
    UNION ALL
    SELECT nr.node_id || ' is FAILED without NODE_FAILED'
    FROM run_node_runs nr
    WHERE nr.status = 'FAILED'
      AND NOT EXISTS (SELECT 1 FROM run_events e WHERE e.node_run_id = nr.id AND e.type = 'NODE_FAILED')
    UNION ALL
    SELECT nr.node_id || ' is WAITING_CALLBACK without NODE_DISPATCHED or AGENT_ACTION_WAITING'
    FROM run_node_runs nr
    WHERE nr.status = 'WAITING_CALLBACK'
      AND NOT EXISTS (SELECT 1 FROM run_events e WHERE e.node_run_id = nr.id
                      AND e.type IN ('NODE_DISPATCHED', 'AGENT_ACTION_WAITING'))
    UNION ALL
    SELECT a.node_id || ' attempt ' || a.attempt_no || ' has no NODE_STARTED for that attempt'
    FROM run_attempts a
    WHERE NOT EXISTS (SELECT 1 FROM run_events e WHERE e.node_run_id = a.node_run_id
                      AND e.type = 'NODE_STARTED' AND (e.payload ->> 'attemptNo')::int = a.attempt_no)
    UNION ALL
    SELECT a.node_id || ' attempt ' || a.attempt_no || ' was dispatched without NODE_DISPATCHED for that attempt'
    FROM run_attempts a
    WHERE a.dispatched_at IS NOT NULL
      AND NOT EXISTS (SELECT 1 FROM run_events e WHERE e.node_run_id = a.node_run_id
                      AND e.type = 'NODE_DISPATCHED' AND (e.payload ->> 'attemptNo')::int = a.attempt_no)
    UNION ALL
    SELECT a.node_id || ' attempt ' || a.attempt_no || ' FAILED and was retried without NODE_RETRYING'
    FROM run_attempts a
    WHERE a.status = 'FAILED'
      AND EXISTS (SELECT 1 FROM run_attempts n WHERE n.node_run_id = a.node_run_id AND n.attempt_no = a.attempt_no + 1)
      AND NOT EXISTS (SELECT 1 FROM run_events e WHERE e.node_run_id = a.node_run_id
                      AND e.type = 'NODE_RETRYING' AND (e.payload ->> 'attemptNo')::int = a.attempt_no)
    UNION ALL
    SELECT a.node_id || ' attempt ' || a.attempt_no || ' completed by callback without NODE_CALLBACK_RECEIVED'
    FROM run_attempts a
    WHERE a.status = 'SUCCEEDED' AND a.dispatched_at IS NOT NULL
      AND NOT EXISTS (SELECT 1 FROM run_events e WHERE e.node_run_id = a.node_run_id
                      AND e.type = 'NODE_CALLBACK_RECEIVED' AND (e.payload ->> 'attemptNo')::int = a.attempt_no)
),

-- Node execution and external calls happen only after the transaction that claimed the
-- work commits, and the terminal Run transition is itself a committed fact derived from
-- its NodeRuns. A terminal Run therefore cannot still own a running NodeRun, Turn or
-- Action, nor an Attempt that is STARTED or DISPATCHED: such a row would be work the
-- Runtime abandoned instead of resolving through its recovery path.
v_inflight AS (
    SELECT 'node_run ' || nr.node_id || ' is ' || nr.status AS detail
    FROM target t JOIN run_node_runs nr ON true
    WHERE t.status IN ('COMPLETED', 'FAILED') AND nr.status IN ('RUNNING', 'WAITING_CALLBACK')
    UNION ALL
    SELECT 'node_attempt ' || a.node_id || '#' || a.attempt_no || ' is ' || a.status
    FROM target t JOIN run_attempts a ON true
    WHERE t.status IN ('COMPLETED', 'FAILED') AND a.status IN ('STARTED', 'DISPATCHED')
    UNION ALL
    SELECT 'agent_turn ' || tu.node_id || ' turn ' || tu.turn_no || ' is ' || tu.status
    FROM target t JOIN run_agent_turns tu ON true
    WHERE t.status IN ('COMPLETED', 'FAILED') AND tu.status IN ('READY', 'RUNNING')
    UNION ALL
    SELECT 'agent_action ' || ac.node_id || ' turn ' || ac.turn_no || ' is ' || ac.status
    FROM target t JOIN run_agent_actions ac ON true
    WHERE t.status IN ('COMPLETED', 'FAILED') AND ac.status IN ('READY', 'RUNNING', 'WAITING_CALLBACK')
    UNION ALL
    SELECT 'tool_attempt ' || ta.node_id || ' turn ' || ta.turn_no || '#' || ta.attempt_no || ' is ' || ta.status
    FROM target t JOIN run_tool_attempts ta ON true
    WHERE t.status IN ('COMPLETED', 'FAILED') AND ta.status IN ('STARTED', 'DISPATCHED')
),

-- Run status is derived from NodeRun state: COMPLETED only when every NodeRun SUCCEEDED
-- and the Run output is recorded, FAILED only when some NodeRun FAILED, and a Run that is
-- still RUNNING or PAUSED has no FAILED NodeRun and is not already fully SUCCEEDED.
v_run_status AS (
    SELECT 'run COMPLETED but ' || nr.node_id || ' is ' || nr.status AS detail
    FROM target t JOIN run_node_runs nr ON true
    WHERE t.status = 'COMPLETED' AND nr.status <> 'SUCCEEDED'
    UNION ALL
    SELECT 'run COMPLETED with no output or completed_at'
    FROM target t WHERE t.status = 'COMPLETED' AND (t.output IS NULL OR t.completed_at IS NULL)
    UNION ALL
    SELECT 'run FAILED but no NodeRun FAILED'
    FROM target t
    WHERE t.status = 'FAILED' AND NOT EXISTS (SELECT 1 FROM run_node_runs WHERE status = 'FAILED')
    UNION ALL
    SELECT 'run ' || t.status || ' but ' || nr.node_id || ' is FAILED'
    FROM target t JOIN run_node_runs nr ON true
    WHERE t.status IN ('RUNNING', 'PAUSED') AND nr.status = 'FAILED'
    UNION ALL
    SELECT 'run ' || t.status || ' but every NodeRun SUCCEEDED'
    FROM target t
    WHERE t.status IN ('RUNNING', 'PAUSED')
      AND EXISTS (SELECT 1 FROM run_node_runs)
      AND NOT EXISTS (SELECT 1 FROM run_node_runs WHERE status <> 'SUCCEEDED')
),

-- Conditional updates decide the single winner when immediate advancement, callback
-- handling, timeout, polling and reconciliation race. For external work that means one
-- callback binding per Attempt and one Attempt per external task id: a second binding
-- would let two racers both believe they own the same completion. A retried KEYED
-- dispatch that the Provider answers with the original task id is still one binding,
-- held by the Attempt that received the answer.
v_external_task AS (
    SELECT 'node attempt ' || a.node_id || '#' || a.attempt_no || ' has ' || count(*) || ' bindings' AS detail
    FROM run_attempts a JOIN callback_bindings b ON b.target_type = 'NODE_ATTEMPT' AND b.target_id = a.id
    GROUP BY a.node_id, a.attempt_no HAVING count(*) > 1
    UNION ALL
    SELECT 'tool attempt ' || ta.node_id || ' turn ' || ta.turn_no || '#' || ta.attempt_no || ' has ' || count(*) || ' bindings'
    FROM run_tool_attempts ta JOIN callback_bindings b ON b.target_type = 'TOOL_ATTEMPT' AND b.target_id = ta.id
    GROUP BY ta.node_id, ta.turn_no, ta.attempt_no HAVING count(*) > 1
    UNION ALL
    SELECT 'external task ' || b.external_task_id || ' is bound ' || count(*) || ' times'
    FROM callback_bindings b
    WHERE b.target_id IN (SELECT id FROM run_attempts UNION ALL SELECT id FROM run_tool_attempts)
    GROUP BY b.external_task_id HAVING count(*) > 1
),

-- Persisted READY work is recoverable and may be claimed again after a crash, by the
-- restarted process or by the Reconciler, so recovery must never complete the same unit
-- of work twice: a NodeRun or Agent Action has at most one SUCCEEDED Attempt, and old
-- Attempts are preserved rather than overwritten.
v_succeeded AS (
    SELECT 'node ' || node_id || ' has ' || count(*) || ' SUCCEEDED attempts' AS detail
    FROM run_attempts WHERE status = 'SUCCEEDED'
    GROUP BY node_run_id, node_id HAVING count(*) > 1
    UNION ALL
    SELECT 'agent action ' || node_id || ' turn ' || turn_no || ' has ' || count(*) || ' SUCCEEDED tool attempts'
    FROM run_tool_attempts WHERE status = 'SUCCEEDED'
    GROUP BY action_id, node_id, turn_no HAVING count(*) > 1
),

results AS (
    SELECT 1 AS ord, 'run_exists' AS check_name, detail FROM v_run_exists
    UNION ALL SELECT 2, 'event_seq_contiguous', detail FROM v_event_seq
    UNION ALL SELECT 3, 'transition_has_event', detail FROM v_transition_event
    UNION ALL SELECT 4, 'no_inflight_work_in_terminal_run', detail FROM v_inflight
    UNION ALL SELECT 5, 'run_status_matches_noderuns', detail FROM v_run_status
    UNION ALL SELECT 6, 'one_external_task_per_attempt', detail FROM v_external_task
    UNION ALL SELECT 7, 'at_most_one_succeeded_attempt', detail FROM v_succeeded
),
checks(ord, check_name) AS (
    VALUES (1, 'run_exists'), (2, 'event_seq_contiguous'), (3, 'transition_has_event'),
           (4, 'no_inflight_work_in_terminal_run'), (5, 'run_status_matches_noderuns'),
           (6, 'one_external_task_per_attempt'), (7, 'at_most_one_succeeded_attempt')
)
SELECT line FROM (
    SELECT c.ord, 0 AS sub,
           CASE WHEN count(r.detail) = 0 THEN 'PASS ' ELSE 'FAIL ' END
           || rpad(c.check_name, 34) || ' violations=' || count(r.detail) AS line
    FROM checks c LEFT JOIN results r ON r.ord = c.ord
    GROUP BY c.ord, c.check_name
    UNION ALL
    SELECT ord, 1, '       - ' || detail FROM results
) report
ORDER BY ord, sub, line;

\echo ''
\echo 'run facts:'
SELECT 'run ' || id || ' status=' || status || ' last_seq=' || last_seq FROM runs WHERE id = :'run_id';
SELECT '  ' || rpad(nr.node_id, 16) || rpad(nr.status, 17) || 'attempts=' || nr.attempt_count
       || coalesce('  [' || string_agg(a.attempt_no || ':' || a.status
                         || coalesce(' bound=' || b.external_task_id, ''), ', ' ORDER BY a.attempt_no) || ']', '')
FROM node_runs nr
LEFT JOIN node_attempts a ON a.node_run_id = nr.id
LEFT JOIN callback_bindings b ON b.target_type = 'NODE_ATTEMPT' AND b.target_id = a.id
WHERE nr.run_id = :'run_id'
GROUP BY nr.id, nr.node_id, nr.status, nr.attempt_count, nr.ready_at
ORDER BY nr.ready_at, nr.node_id;

ROLLBACK;
