-- Execution-ledger invariant report for one Run.
--
-- Usage: psql -v ON_ERROR_STOP=1 -v run_id=<run id> [-v provider_capacity=<id>=<n>,...] -f invariants.sql
-- (scripts/demo/invariants.sh runs it inside the Compose postgres container). The
-- Provider capacity is not stored in the database, so the per-Provider slot check runs
-- only when the limits are passed in, in the EMBERLING_PROVIDER_CAPACITY format.
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

\if :{?provider_capacity}
\else
\set provider_capacity ''
\endif

BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;

\echo 'checks run:'
\echo '  run_exists                         the Run row is present'
\echo '  event_seq_contiguous               Event seq is 1..N with no gap, no duplicate, and equals runs.last_seq'
\echo '  transition_has_event               every recorded state transition has its Event in the same Run;'
\echo '                                     a dispatched Attempt completes by exactly one of callback or Provider poll'
\echo '  no_inflight_work_in_terminal_run   a terminal Run has no RUNNING/WAITING NodeRun, Turn or Action and no STARTED/DISPATCHED Attempt'
\echo '  run_status_matches_noderuns        the Run status is the one its NodeRuns derive'
\echo '  one_external_task_per_attempt      no Attempt is bound to two external tasks, no external task id is bound twice'
\echo '  at_most_one_succeeded_attempt      no NodeRun or Agent Action has two SUCCEEDED Attempts'
\echo '  provider_slot_live_noderun         every Provider slot this Run holds belongs to a RUNNING or WAITING_CALLBACK NodeRun'
\echo '  patch_verified_subject_recorded    every patch_verified fact is about a patch the same Agent workspace recorded (or the empty baseline patch)'
\echo '  ready_for_approval_has_request     every Agent Run judged READY_FOR_APPROVAL has a change request about its latest workspace patch'
SELECT CASE WHEN :'provider_capacity' = ''
    THEN 'checks not run: provider_slots_within_capacity (pass -v provider_capacity=<id>=<n>,... to check every Provider''s held slots against its limit)'
    ELSE '  provider_slots_within_capacity     no Provider holds more slots than its configured limit (' || :'provider_capacity' || ')' END;
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
    -- A dispatched Attempt completes either through an accepted callback, which writes
    -- NODE_CALLBACK_RECEIVED, or through a Provider poll, which deliberately does not and
    -- instead records completionSource PROVIDER_POLL on NODE_COMPLETED. NODE_COMPLETED
    -- carries no attempt number; a NodeRun has at most one SUCCEEDED Attempt (checked
    -- below), so the NodeRun's completion source identifies how that Attempt completed.
    SELECT a.node_id || ' attempt ' || a.attempt_no || ' completed by callback without NODE_CALLBACK_RECEIVED'
    FROM run_attempts a
    WHERE a.status = 'SUCCEEDED' AND a.dispatched_at IS NOT NULL
      AND NOT EXISTS (SELECT 1 FROM run_events e WHERE e.node_run_id = a.node_run_id
                      AND e.type = 'NODE_CALLBACK_RECEIVED' AND (e.payload ->> 'attemptNo')::int = a.attempt_no)
      AND NOT EXISTS (SELECT 1 FROM run_events e WHERE e.node_run_id = a.node_run_id
                      AND e.type = 'NODE_COMPLETED' AND e.payload ->> 'completionSource' = 'PROVIDER_POLL')
    UNION ALL
    SELECT a.node_id || ' attempt ' || a.attempt_no || ' completed by Provider poll but also has NODE_CALLBACK_RECEIVED'
    FROM run_attempts a
    WHERE a.status = 'SUCCEEDED' AND a.dispatched_at IS NOT NULL
      AND EXISTS (SELECT 1 FROM run_events e WHERE e.node_run_id = a.node_run_id
                  AND e.type = 'NODE_CALLBACK_RECEIVED' AND (e.payload ->> 'attemptNo')::int = a.attempt_no)
      AND EXISTS (SELECT 1 FROM run_events e WHERE e.node_run_id = a.node_run_id
                  AND e.type = 'NODE_COMPLETED' AND e.payload ->> 'completionSource' = 'PROVIDER_POLL')
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

-- A Provider slot is taken in the transaction that claims the NodeRun and freed in the
-- one that moves it to SUCCEEDED or a terminal FAILED, so a committed slot row always
-- belongs to a NodeRun that is still RUNNING or WAITING_CALLBACK. A slot held by a
-- READY or terminal NodeRun would be occupancy that outlived its task.
v_slot_live AS (
    SELECT 'slot of ' || nr.node_id || ' held while the NodeRun is ' || nr.status AS detail
    FROM provider_slots ps JOIN run_node_runs nr ON nr.id = ps.node_run_id
    WHERE nr.status NOT IN ('RUNNING', 'WAITING_CALLBACK')
),

-- A patch_verified fact binds its verdict to the patch digest the verification named. The
-- verify Tool refuses a digest other than the workspace's current one before any Attempt
-- exists, and a patch record commits in the same Tool result transaction as the write that
-- produced it, so every subject is a patch the same Agent's workspace recorded. The one
-- digest with no record is the empty patch: a workspace verified before any write.
v_fact_patch_recorded AS (
    SELECT 'patch_verified fact ' || f.id || ' is about ' || f.subject_ref || ', which the workspace never recorded' AS detail
    FROM execution_facts f
    WHERE f.run_id = :'run_id' AND f.fact_type = 'patch_verified'
      AND f.subject_ref <> encode(sha256(''::bytea), 'hex')
      AND NOT EXISTS (SELECT 1 FROM workspace_patches p
                      WHERE p.agent_run_id = f.agent_run_id AND p.patch_digest = f.subject_ref)
),

-- The completion check judges a PREPARE_ONLY task READY_FOR_APPROVAL only when a change
-- request names the workspace's latest patch, and it reads those rows in the transaction
-- that terminates the Agent Run, after which neither the patches nor the requests change.
-- A READY_FOR_APPROVAL verdict without such a request would be a verdict that rests on
-- facts the ledger does not hold.
v_ready_has_request AS (
    SELECT 'agent run ' || ac.agent_run_id || ' is READY_FOR_APPROVAL without a change request about its latest patch' AS detail
    FROM agent_completions ac
    JOIN agent_runs ar ON ar.id = ac.agent_run_id
    JOIN run_node_runs nr ON nr.id = ar.node_run_id
    WHERE ac.outcome = 'READY_FOR_APPROVAL'
      AND NOT EXISTS (
          SELECT 1 FROM change_requests cr
          WHERE cr.agent_run_id = ac.agent_run_id
            AND cr.patch_digest = (SELECT p.patch_digest FROM workspace_patches p
                                   WHERE p.agent_run_id = ac.agent_run_id
                                   ORDER BY p.seq DESC LIMIT 1))
),

-- Admission counts the held slots under the Provider's row lock before inserting its
-- own, so the committed count never exceeds the configured limit. The limit is not a
-- database fact: it is passed in, and the check runs only when it was. It is a snapshot
-- of this moment across every Run, not of every moment.
capacity_limits AS (
    SELECT split_part(entry, '=', 1) AS provider_id, split_part(entry, '=', 2)::int AS slot_limit
    FROM string_to_table(:'provider_capacity', ',') AS entry
    WHERE entry <> ''
),
v_within_capacity AS (
    SELECT 'provider ' || l.provider_id || ' holds ' || count(ps.node_run_id) || ' slots, limit ' || l.slot_limit AS detail
    FROM capacity_limits l LEFT JOIN provider_slots ps ON ps.provider_id = l.provider_id
    GROUP BY l.provider_id, l.slot_limit
    HAVING count(ps.node_run_id) > l.slot_limit
),

results AS (
    SELECT 1 AS ord, 'run_exists' AS check_name, detail FROM v_run_exists
    UNION ALL SELECT 2, 'event_seq_contiguous', detail FROM v_event_seq
    UNION ALL SELECT 3, 'transition_has_event', detail FROM v_transition_event
    UNION ALL SELECT 4, 'no_inflight_work_in_terminal_run', detail FROM v_inflight
    UNION ALL SELECT 5, 'run_status_matches_noderuns', detail FROM v_run_status
    UNION ALL SELECT 6, 'one_external_task_per_attempt', detail FROM v_external_task
    UNION ALL SELECT 7, 'at_most_one_succeeded_attempt', detail FROM v_succeeded
    UNION ALL SELECT 8, 'provider_slot_live_noderun', detail FROM v_slot_live
    UNION ALL SELECT 9, 'patch_verified_subject_recorded', detail FROM v_fact_patch_recorded
    UNION ALL SELECT 10, 'ready_for_approval_has_request', detail FROM v_ready_has_request
    UNION ALL SELECT 11, 'provider_slots_within_capacity', detail FROM v_within_capacity
),
checks(ord, check_name) AS (
    SELECT * FROM (
        VALUES (1, 'run_exists'), (2, 'event_seq_contiguous'), (3, 'transition_has_event'),
               (4, 'no_inflight_work_in_terminal_run'), (5, 'run_status_matches_noderuns'),
               (6, 'one_external_task_per_attempt'), (7, 'at_most_one_succeeded_attempt'),
               (8, 'provider_slot_live_noderun'), (9, 'patch_verified_subject_recorded'),
               (10, 'ready_for_approval_has_request')
    ) AS always(ord, check_name)
    UNION ALL
    -- Listed, and so reported, only when the limits were passed in.
    SELECT 11, 'provider_slots_within_capacity' WHERE :'provider_capacity' <> ''
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
       || coalesce('  queued_for=' || nr.capacity_provider_id, '')
       || CASE WHEN ps.node_run_id IS NOT NULL THEN '  slot=' || ps.provider_id ELSE '' END
       || coalesce('  [' || string_agg(a.attempt_no || ':' || a.status
                         || coalesce(' bound=' || b.external_task_id, '')
                         || CASE WHEN a.poll_count > 0 THEN ' polls=' || a.poll_count ELSE '' END,
                         ', ' ORDER BY a.attempt_no) || ']', '')
       || coalesce('  completionSource=' || (
            SELECT string_agg(e.payload ->> 'completionSource', ',' ORDER BY e.seq)
            FROM events e WHERE e.node_run_id = nr.id AND e.type = 'NODE_COMPLETED'), '')
FROM node_runs nr
LEFT JOIN node_attempts a ON a.node_run_id = nr.id
LEFT JOIN callback_bindings b ON b.target_type = 'NODE_ATTEMPT' AND b.target_id = a.id
LEFT JOIN provider_slots ps ON ps.node_run_id = nr.id
WHERE nr.run_id = :'run_id'
GROUP BY nr.id, nr.node_id, nr.status, nr.attempt_count, nr.ready_at, nr.capacity_provider_id, ps.node_run_id, ps.provider_id
ORDER BY nr.ready_at, nr.node_id;

ROLLBACK;
