/**
 * Every shared user-facing string in Studio. Sentence case, one idea per string, no
 * dot-joined fragments and no raw enums or keys: a raw value appears only in a tooltip,
 * Technical details or a Raw view. To change a word, change it here, not in a page.
 *
 * Strings that take a count or a value are functions, and they handle the plural.
 */

import { editor } from './editor';
import { runs } from './runs';
import { workflows } from './workflows';

const plural = (n: number, one: string, many = `${one}s`): string =>
  `${n.toLocaleString('en-US')} ${n === 1 ? one : many}`;

/** A list of names as a sentence: "A", "A and B", "A, B and C". */
const listNames = (names: readonly string[]): string =>
  names.length < 2 ? names.join('') : `${names.slice(0, -1).join(', ')} and ${names.at(-1) ?? ''}`;

export const en = {
  product: 'Emberling',
  documentTitle: (page: string) => `${page} – Emberling`,

  /** The part of each document title before the product name. */
  pageTitles: {
    runs: 'Runs',
    workflows: 'Workflows',
    approvals: 'Approvals',
    approval: (code: string) => `Approval ${code}`,
    notFound: 'Not found',
    error: 'Error',
    editor: (name: string) => `${name} – Editor`,
    workflowRuns: (name: string) => `${name} runs`,
    run: (name: string, code: string) => `${name} runs – Run ${code}`,
  },

  nav: {
    runs: 'Runs',
    workflows: 'Workflows',
    approvals: 'Approvals',
    backToApprovals: 'Back to Approvals',
    main: 'Main',
    home: 'Emberling home: go to Runs',
    backToRuns: 'Back to Runs',
    backToWorkflows: 'Back to Workflows',
    editor: 'Editor',
    collapse: 'Collapse sidebar',
    expand: 'Expand sidebar',
    recent: 'Recent',
    shortcuts: 'Keyboard shortcuts',
    notRunYet: 'Not run yet',
    live: (running: number, waiting: number) =>
      [running ? `${running} running` : '', waiting ? `${waiting} waiting` : '']
        .filter(Boolean)
        .join(', '),
    runTotal: (n: number) => plural(n, 'run'),
    workflowTotal: (n: number) => plural(n, 'workflow'),
    countTip: (label: string, total: string, live: string) =>
      live ? `${label}: ${total}, ${live}` : `${label}: ${total}`,
    lastRun: (status: string, when: string) => `Last run ${status.toLowerCase()}, ${when}`,
    breadcrumb: 'Breadcrumb',
    workflowViews: 'Workflow views',
    newWorkflowRuns: 'Save the workflow to run it.',
  },

  /** Backend connection, from GET /ready (ready or starting) or a failed request (unreachable). */
  backend: {
    ready: { label: 'Connected', help: 'The backend is reachable and ready.' },
    starting: {
      label: 'Backend starting',
      help: 'The backend is running but hasn’t finished starting up.',
    },
    unreachable: {
      label: 'Can’t reach the backend',
      help: 'Requests to the backend are failing. What you see may be out of date.',
    },
    checking: { label: 'Checking connection', help: 'Checking whether the backend is ready.' },
  },

  tz: {
    label: 'Time zone',
    listLabel: 'Time zones',
    search: 'Search city, zone or offset',
    detected: 'Detected',
    utc: 'UTC',
    all: 'All time zones',
    thisDevice: 'This device',
    utcName: 'Coordinated Universal Time',
    trigger: (city: string, offset: string) =>
      `Times are shown in ${city} time (${offset}). Change time zone`,
    triggerUtc: 'Times are shown in UTC. Change time zone',
    empty: (q: string) => `No time zone matches “${q}”. Try a city or an offset like +8.`,
    zoneTime: (city: string, offset: string) => `${city} time (${offset})`,
  },

  time: {
    justNow: 'just now',
    min: 'min',
    hours: 'h',
    day: (n: number) => (n === 1 ? 'day' : 'days'),
    months: 'months',
    year: (n: number) => (n === 1 ? 'year' : 'years'),
    ago: (t: string) => `${t} ago`,
    in: (t: string) => `in ${t}`,
    soFar: (d: string) => `${d} so far`,
    missing: '—',
  },

  ids: {
    copy: (label = 'ID') => `Copy ${label}`,
    copied: 'Copied',
    copyFailed: 'Couldn’t copy. Select the text and copy it instead.',
    open: (label: string, id: string) => `${label.charAt(0).toUpperCase()}${label.slice(1)} ${id}`,
  },

  facts: {
    notRecorded: 'Not recorded',
  },

  form: {
    optional: '(optional)',
    required: 'required',
  },

  /** The fields of an object schema: what kind of value each one takes. */
  schemaFields: {
    oneOf: (options: readonly string[]) => `One of: ${options.join(', ')}`,
    text: (min: number | undefined, max: number | undefined) => {
      const n = (x: number) => x.toLocaleString('en-US');
      if (min !== undefined && max !== undefined && min === max) {
        return `Text, exactly ${plural(min, 'character')}`;
      }
      if (min !== undefined && min > 1 && max !== undefined) {
        return `Text, ${n(min)} to ${n(max)} characters`;
      }
      if (max !== undefined) return `Text, up to ${plural(max, 'character')}`;
      if (min !== undefined && min > 1) return `Text, at least ${plural(min, 'character')}`;
      return 'Text';
    },
    number: (integer: boolean, min: number | undefined, max: number | undefined) => {
      const word = integer ? 'Whole number' : 'Number';
      const n = (x: number) => x.toLocaleString('en-US');
      if (min !== undefined && max !== undefined) return `${word}, ${n(min)} to ${n(max)}`;
      if (min !== undefined) return `${word}, ${n(min)} or more`;
      if (max !== undefined) return `${word}, ${n(max)} or less`;
      return word;
    },
    yesNo: 'Yes/no',
    object: 'JSON object',
    list: 'JSON list',
    empty: 'Empty (null)',
    anyValue: 'Any JSON value',
    required: 'Required',
    optional: 'Optional',
    choose: 'Choose a value',
  },

  techDetails: 'Technical details',
  close: 'Close',
  done: 'Done',
  cancel: 'Cancel',
  retry: 'Retry',
  loading: 'Loading',
  actionsFor: (name: string) => `Actions for ${name}`,

  shortcuts: {
    title: 'Keyboard shortcuts',
    subtitle: 'Single keys don’t work while you type in a field.',
    entryTip: 'Keyboard shortcuts (?)',
    groups: {
      everywhere: 'Everywhere',
      lists: 'Lists',
    },
    openSheet: 'Show keyboard shortcuts',
    toggleSidebar: 'Collapse or expand the sidebar',
    goUp: 'Close the top layer, then go up one level',
    moveRows: 'Move between rows',
    openRow: 'Open the row',
    rowMenu: 'Open the row’s actions',
    keys: {
      shiftF10: 'Shift+F10',
      enter: 'Enter',
      esc: 'Esc',
      up: '↑',
      down: '↓',
      left: '←',
      right: '→',
      home: 'Home',
      end: 'End',
    },
  },

  json: {
    showAs: 'Show as',
    tree: 'Tree',
    raw: 'Raw',
    treeLabel: 'JSON',
    copyPath: 'Copy path',
    copyJson: 'Copy JSON',
    copiedPath: (path: string) => `Copied ${path}`,
    copiedJson: 'Copied JSON',
    showAll: 'Show all',
    showAsText: 'Show as text',
    showAsJson: 'Show as JSON',
    items: (n: number) => plural(n, 'item'),
    keys: (n: number) => plural(n, 'key'),
  },

  media: {
    kind: {
      ASSET: 'Uploaded image',
      ARTIFACT: 'Generated image',
      EXTERNAL: 'Linked image',
    },
    open: (what: string) => `Open preview: ${what}`,
    more: (n: number) => `Show ${String(n)} more`,
    unavailable: 'Preview unavailable',
    unavailableBody: 'The image could not be loaded.',
    previous: 'Previous image',
    next: 'Next image',
    position: (i: number, n: number) => `${String(i)} of ${String(n)}`,
    imageOf: (title: string, i: number, n: number) =>
      `${title}, image ${String(i)} of ${String(n)}`,
    type: 'Type',
    size: 'Size',
    reference: 'Reference',
    referenceLabel: 'image reference',
    assetId: 'Asset ID',
    artifactId: 'Artifact ID',
    assetIdLabel: 'asset ID',
    artifactIdLabel: 'artifact ID',
    json: 'JSON',
    video: 'Video',
    play: (title: string) => `Play ${title}`,
    openImage: (title: string) => `Open ${title}`,
    videoFacts: (seconds: string, shape: string) => `${seconds} s, ${shape}`,
    shape: { vertical: 'vertical', horizontal: 'horizontal', square: 'square' },
    dimensions: (w: number, h: number) => `${w} × ${h}`,
  },

  /** Values of any run laid out by shape: a report, a task, a step's output. */
  values: {
    yes: 'Yes',
    no: 'No',
    passed: 'Passed',
    passedPhotos: (passed: number, total: number) => `${passed} of ${plural(total, 'photo')}`,
    didNotPass: 'Did not pass',
    checksPassed: (passed: number, total: number) =>
      `${passed} of ${plural(total, 'check')} passed`,
    completionChecksPassed: (passed: number, total: number) =>
      `${passed} of ${total} completion ${total === 1 ? 'check' : 'checks'} passed`,
  },

  lists: {
    options: 'Options',
    noMatches: 'No matches.',
    rowsPerPage: 'Rows per page',
    pages: 'Pages',
    previous: 'Previous',
    next: 'Next',
    range: (from: number, to: number, total: number | null, noun: readonly [string, string]) => {
      const f = (n: number) => n.toLocaleString('en-US');
      if (total === null) return `${f(from)}–${f(to)}`;
      const word = total === 1 ? noun[0] : noun[1];
      if (from === 1 && to === total) return `${f(total)} ${word}`;
      return `${f(from)}–${f(to)} of ${f(total)} ${word}`;
    },
  },

  states: {
    errorCode: (status: number, code: string) => `${status} ${code}`,
    loadFailed: (what: string) => `Couldn’t load ${what}.`,
    what: { run: 'this run', workflow: 'this workflow', workflows: 'workflows', runs: 'runs' },
    errorBody: 'Try again. If it keeps failing, the details below help when you report it.',
    pageError: {
      title: 'This page couldn’t be shown.',
      body: 'Something went wrong while opening it. Go back and try again.',
    },
  },

  notFound: {
    run: {
      title: (code: string) => `Run ${code} doesn’t exist.`,
      body: 'The link may be wrong, or the run was never created on this backend.',
      action: 'Go to Runs',
    },
    workflow: {
      title: (code: string) => `Workflow ${code} doesn’t exist.`,
      body: 'The link may be wrong, or the workflow was never saved on this backend.',
      action: 'Go to Workflows',
    },
    page: {
      title: 'This page doesn’t exist.',
      body: 'Check the address, or start from the list of runs.',
      action: 'Go to Runs',
    },
    crumb: 'Not found',
  },

  status: {
    /** What an incomplete goal still lacks, in plain words; the raw code stays in the Data tab. */
    completionMissing: {
      FINAL_RESPONSE_MISSING: 'The agent didn’t hand back a final report.',
    } as Record<string, string>,
    run: {
      RUNNING: { short: 'Running', long: 'Running', help: 'At least one step is running.' },
      PAUSED: {
        short: 'Waiting',
        long: 'Waiting for external result',
        help: 'Waiting for an outside service to reply.',
      },
      COMPLETED: { short: 'Completed', long: 'Completed', help: 'Every step finished.' },
      FAILED: { short: 'Failed', long: 'Failed', help: 'A step failed, so the run stopped.' },
    },
    step: {
      READY: { label: 'Queued', help: 'Ready to start next.' },
      RUNNING: { label: 'Running', help: 'Running now.' },
      WAITING_CALLBACK: {
        label: 'Waiting for reply',
        help: 'Waiting for an outside service to reply.',
      },
      SUCCEEDED: { label: 'Succeeded', help: 'Finished.' },
      FAILED: { label: 'Failed', help: 'This step failed.' },
      NONE: { label: 'Not started', help: 'Not reached yet.' },
      NONE_RUN_FAILED: {
        label: 'Did not run',
        help: 'An earlier step failed, so this one never started.',
      },
    },
    turn: { READY: 'Queued', RUNNING: 'Running', COMPLETED: 'Succeeded', FAILED: 'Failed' },
    action: {
      READY: 'Decided, not started',
      RUNNING: 'Running',
      WAITING_CALLBACK: 'Waiting for reply',
      WAITING_APPROVAL: 'Waiting for approval',
      WAITING_HUMAN: 'Waiting for confirmation',
      SUCCEEDED: 'Succeeded',
      FAILED: 'Failed',
    },
    authorization: {
      ALLOW: 'Allowed',
      ASK: 'Needs approval',
      DENY: 'Denied',
    },
    approval: {
      PENDING: 'Pending',
      APPROVED: 'Approved',
      REJECTED: 'Rejected',
      EXPIRED: 'Expired',
    },
    pause: {
      APPROVAL: 'Clock stopped while a person decides',
      RELEASE: 'Clock stopped while the release is confirmed',
      HUMAN: 'Clock stopped while a person confirms',
    },
    releaseOutcome: {
      PENDING: 'Awaiting production',
      UNKNOWN: 'Outcome unknown, being queried',
      DEPLOYED: 'Deployed',
      VOIDED: 'Voided',
      REFUSED: 'Refused',
    },
    releaseReason: {
      DEPLOYED: 'deployed',
      VOIDED: 'voided',
      REFUSED: 'refused by production',
      NOT_DEPLOYED: 'not deployed',
    },
    humanWait: {
      OPEN: 'Open',
      STOPPED: 'Stopped',
      EXPIRED: 'Expired',
      RESOLVED: 'Resolved',
    },
    attempt: {
      STARTED: 'Running',
      DISPATCHED: 'Waiting for reply',
      SUCCEEDED: 'Succeeded',
      FAILED: 'Failed',
    },
    connection: {
      live: { label: 'Live', help: 'New events appear as they happen.' },
      reconnecting: {
        label: 'Reconnecting',
        help: 'Connection lost. Missed events are replayed when it returns.',
      },
      final: { label: 'Final', help: 'The run has finished.' },
      stopped: { label: 'Stopped', help: 'Live updates stopped. Reload to see new events.' },
    },
    termination: {
      FINAL_RESPONSE: 'Finished with an answer',
      MAX_TURNS: 'Turn limit reached',
      TIMEOUT: 'Ran out of time',
      MODEL_ERROR: 'Model call failed',
      TOOL_ERROR: 'Tool call failed',
      INVALID_ACTION: 'Decision was rejected',
      HUMAN_STOPPED: 'Stopped by a person',
    },
    /** The task result of an agent with a goal, one card per outcome. */
    completion: {
      title: 'Task result',
      evaluated: 'Checked',
      checks: 'Checks',
      checksPassed: (passed: number, total: number) => `${passed} of ${total} passed`,
      stillMissing: 'Still missing',
      outcome: {
        READY_FOR_APPROVAL: {
          label: 'Ready for approval',
          description:
            'The patch is verified and the change request is written. A person approves and schedules the release from here.',
        },
        DEPLOYED: {
          label: 'Deployed',
          description:
            'The release was approved and confirmed deployed, and the target is healthy on the patched version.',
        },
        DEPLOYED_CHECK_FAILED: {
          label: 'Deployed, health check failed',
          description:
            'The patched version is live but its health check did not pass. The rollback plan applies.',
        },
        RELEASE_UNKNOWN: {
          label: 'Release outcome unknown',
          description:
            'A person stopped the wait before the release result was known. The target stays occupied until reconciliation settles it.',
        },
        INCOMPLETE: {
          label: 'Not complete',
          description: 'The agent stopped before every required fact was recorded.',
        },
        CLIP_DELIVERED: {
          label: 'Clip delivered',
          description:
            'The clip the agent named was rendered and its newest checks passed, including the content review when one applied.',
        },
        CANNOT_SATISFY: {
          label: 'Request cannot be satisfied',
          description:
            'The agent ended with a reason instead of a clip. Whether the reason holds is judged against the material, not by Emberling.',
        },
      },
    },
    completionSource: {
      SYNC_EXECUTION: null,
      CALLBACK: 'Result arrived by callback',
      PROVIDER_POLL: 'Result fetched by polling',
    },
    claimSource: {
      IMMEDIATE: null,
      RECONCILER: 'Picked up again after a restart',
    },
  },

  events: {
    label: {
      RUN_CREATED: 'Run started',
      RUN_PAUSED: 'Run is waiting',
      RUN_RESUMED: 'Run continued',
      RUN_COMPLETED: 'Run completed',
      RUN_FAILED: 'Run failed',
      NODE_READY: 'Step queued',
      NODE_STARTED: 'Step started',
      NODE_RETRYING: 'Step will retry',
      NODE_WAITING_CAPACITY: 'Waiting for a free slot',
      NODE_DISPATCHED: 'Sent to external service',
      NODE_CALLBACK_RECEIVED: 'External result received',
      NODE_COMPLETED: 'Step succeeded',
      NODE_FAILED: 'Step failed',
      AGENT_STARTED: 'Agent started',
      AGENT_TURN_READY: 'Turn queued',
      AGENT_TURN_STARTED: 'Turn started',
      AGENT_DECISION_COMMITTED: 'Decision saved',
      AGENT_ACTION_STARTED: 'Action started',
      AGENT_ACTION_WAITING: 'Waiting for tool result',
      AGENT_ACTION_COMPLETED: 'Action succeeded',
      AGENT_ACTION_FAILED: 'Action failed',
      AGENT_STATE_UPDATED: 'Agent state updated',
      AGENT_COMPLETED: 'Agent finished',
      AGENT_FAILED: 'Agent failed',
      AGENT_ACTION_WAITING_APPROVAL: 'Waiting for approval',
      APPROVAL_DECIDED: 'Approval decided',
      AGENT_PAUSED: 'Agent clock stopped',
      AGENT_RESUMED: 'Agent clock resumed',
      RELEASE_OUTCOME_UNKNOWN: 'Release outcome unknown',
      RELEASE_VOIDED: 'Release voided',
      AGENT_ACTION_WAITING_HUMAN: 'Waiting for confirmation',
      AGENT_COMPLETION_EVALUATED: 'Completion checked',
    },
    groups: {
      all: 'All',
      state: 'State changes',
      decisions: 'Agent decisions',
      tools: 'Tool calls',
      waits: 'Waits and callbacks',
      errors: 'Errors',
    },
  },

  factLabel: {
    patch_tested: 'Patch tested',
    image_generated: 'Image generated',
    asset_reviewed: 'Asset reviewed',
    patch_reviewed: 'Patch reviewed',
  } as Record<string, string>,

  errors: {
    TIMEOUT: {
      title: 'The agent ran out of time',
      short: 'Ran out of time',
      next: 'Adjust the timeout',
    },
    MAX_TURNS: {
      title: 'The agent hit its turn limit',
      short: 'Used all its turns',
      next: 'Adjust max turns',
    },
    PRECONDITION_UNMET: {
      title: 'A tool was blocked by a missing check',
      short: 'A tool needed a check that hadn’t passed',
      next: 'Review the instructions',
    },
    GENERATION_LIMIT_REACHED: {
      title: 'Generation limit reached',
      short: 'Used all its generation calls',
      next: 'Adjust max generation calls',
    },
    FACT_BASIS_MISSING: {
      title: 'A result could not be traced to its source',
      short: 'A result didn’t point to its source',
      next: 'Review the instructions',
    },
    MODEL_ERROR: {
      title: 'The model call failed',
      short: 'The model returned an error',
      next: 'Check the model settings',
    },
    TOOL_ERROR: {
      title: 'A tool call failed',
      short: 'A tool returned an error',
      next: 'Review the instructions',
    },
    INVALID_ACTION: {
      title: 'The agent’s answer was rejected',
      short: 'Its decision was rejected',
      next: 'Check the output schema',
    },
    EXECUTOR_ERROR: {
      title: 'A step failed',
      short: 'Couldn’t reach its service',
      next: 'Open step in the Editor',
    },
  },
  errorCause: {
    TIMEOUT: (c: { limit?: string; tool?: string }) =>
      `Its ${c.limit ?? 'time'} limit passed${c.tool ? ` while ${c.tool} was still running` : ''}.`,
    MAX_TURNS: (c: { limit?: number }) =>
      `It used all ${c.limit === undefined ? '' : `${c.limit} `}turns without finishing.`,
    PRECONDITION_UNMET: (c: { tool?: string; fact?: string }) =>
      `${c.tool ?? 'A tool'} needs a passed ${c.fact ?? 'saved result'} first, and none existed.`,
    GENERATION_LIMIT_REACHED: (c: { used: number; limit: number }) =>
      `The agent had used ${c.used} of ${c.limit} generation calls and tried another.`,
    FACT_BASIS_MISSING: () =>
      'A tool result did not point to the saved result it was based on, so Emberling stopped.',
    MODEL_ERROR: (c: { model?: string }) =>
      `The decision model${c.model ? ` (${c.model})` : ''} returned an error.`,
    TOOL_ERROR: (c: { tool?: string }) => `${c.tool ?? 'A tool'} reported an error.`,
    INVALID_ACTION: () => 'The agent produced an action or output that did not pass validation.',
    EXECUTOR_ERROR: (c: { step?: string }) =>
      `${c.step ?? 'A step'} reported an error while calling its service.`,
  },
  unknownError: (step: string) => `${step} failed.`,

  approvals: {
    title: 'Approvals',
    intro: 'Agent calls that change production wait here until a person approves or rejects them.',
    tokenLabel: 'Operator token',
    tokenHelp:
      'Sent with every approval request and kept only in this tab’s memory. Reloading forgets it.',
    tokenPlaceholder: 'Paste the operator token',
    useToken: 'Use token',
    forgetToken: 'Forget token',
    tokenInUse: 'Operator token in use for this tab.',
    tokenRequired: 'Enter the operator token to see approvals.',
    tokenEmpty: 'Paste the token first.',
    unauthorized: 'The operator token was not accepted.',
    notConfigured: 'This server has no operator token configured, so approvals cannot be decided.',
    tabsLabel: 'Approval status',
    status: {
      PENDING: 'Pending',
      APPROVED: 'Approved',
      REJECTED: 'Rejected',
      EXPIRED: 'Expired',
    },
    empty: (status: string) => `No ${status.toLowerCase()} approvals.`,
    columns: {
      tool: 'Tool',
      run: 'Run',
      requested: 'Requested',
      deadline: 'Deadline',
      status: 'Status',
    },
    expiresAt: 'Expires',
    expiredAt: 'Expired',
    decidedAt: 'Decided',
    decidedBy: 'Decided by',
    reason: 'Reason',
    requestedAt: 'Requested',
    tool: 'Tool',
    arguments: 'Call arguments',
    noArguments: 'This call has no arguments.',
    run: 'Run',
    workflow: 'Workflow',
    agentRun: 'Agent run',
    action: 'Action',
    openRun: 'Open run',
    approve: 'Approve',
    reject: 'Reject',
    reasonLabel: 'Reason',
    reasonHelp: 'Required to reject; optional to approve. At most 400 bytes.',
    reasonPlaceholder: 'Why this decision was made',
    reasonRequired: 'Give a reason to reject.',
    reasonTooLong: 'The reason must be at most 400 bytes.',
    deciding: 'Saving decision…',
    approved: 'Approved. The call runs now and the agent’s clock continues.',
    rejected: 'Rejected. The agent is told why and its clock continues.',
    repeated: 'This decision had already been recorded.',
    conflict: 'This approval was already decided differently. Showing the recorded decision.',
    notActionable:
      'The run this approval belongs to has already ended. The approval stays undecided and expires at its deadline.',
    settled: 'This approval is no longer pending.',
    pauseNote: 'While it waits, the agent’s own time limit is paused.',
    notFound: 'This approval does not exist.',
    changeRequest: 'Change request',
    noChangeRequest: 'This call names no change request.',
    changeRequestId: 'Change request',
    contentDigest: 'Content digest',
    verificationFact: 'Verification fact',
    decisionBasis: 'Decision basis',
    basisTurn: (n: number) => `Turn ${n}`,
    basisItem: (kind: string, label: string) =>
      `${kind.replace(/_/g, ' ').toLowerCase()}: ${label}`,
    basisUnresolved: 'The cited manifest item can no longer be resolved.',
    openRunTurns: 'Open the run’s turns',
    patch: 'Patch',
    noPatch: 'The workspace holds no patch with the digest this change request names.',
    patchDigest: 'Patch digest',
    patchSeq: (n: number) => `Workspace patch ${n}`,
    touchedPaths: 'Touched paths',
  },

  /** Releases production has not answered, handed to a person after their confirm deadline. */
  humanWaits: {
    title: 'Releases waiting for a person',
    intro:
      'Production has not confirmed these releases. The agent is paused; you can keep waiting or stop the task.',
    empty: 'No releases are waiting for a person.',
    loadFailed: 'Couldn’t load the releases waiting for a person.',
    target: 'Target',
    operation: 'Operation',
    confirmDeadline: 'Confirm deadline',
    waitDeadline: 'Wait ends',
    reasonLabel: 'Why',
    reason: {
      OUTCOME_UNKNOWN: 'Production has no record of this operation.',
      NOT_CONFIRMED: 'Production knows the operation but has not confirmed it.',
    },
    run: 'Run',
    stop: 'Stop waiting',
    stopping: 'Stopping…',
    confirmTitle: 'Stop waiting for this release?',
    confirmBody: (target: string) =>
      `The task ends as stopped by a person. ${target} stays held until production says whether the release deployed, so no other release can start on it before then.`,
    confirmStop: 'Stop the task',
    stopped: 'Stopped. The task has ended; the target stays held until production answers.',
    repeated: 'This wait had already been stopped.',
    resolved: 'Production answered first, so the release concluded on its own.',
    closed: 'The task already ended. This wait closes on its own at its deadline.',
    stopFailed: 'Couldn’t stop the wait. Try again.',
  },

  glossary: {
    workflow: [
      'Workflow',
      'A saved, versioned set of connected steps. Each save creates a new version; old versions never change.',
    ],
    version: [
      'Version',
      'An immutable snapshot of a workflow. A run always uses the version it started with.',
    ],
    run: ['Run', 'One execution of a workflow version with a specific input.'],
    step: ['Step', 'One node of the workflow as it ran in this run.'],
    turn: [
      'Turn',
      'One round of the agent: the model decides, then Emberling carries out that decision.',
    ],
    action: ['Action', 'What a turn decided to do: call one tool, or finish with an answer.'],
    attempt: ['Attempt', 'One try at running a step or a tool call. Earlier attempts are kept.'],
    savedResult: [
      'Saved result',
      'A fact Emberling saved from a tool result, such as “Patch tested: Passed”. Some tools require a saved result before they may run.',
    ],
    callback: [
      'Callback',
      'A message an outside service sends back to Emberling when its work is done.',
    ],
    generation: [
      'Generation call',
      'A call to a tool that creates media. The agent can be limited to a number of these.',
    ],
    sideEffect: [
      'Changes an external system',
      'The call can change something outside Emberling. Emberling never re-sends such a call on its own.',
    ],
  },

  tools: {
    async: {
      badge: 'Reports back later',
      help: 'The service answers in a separate reply. Emberling saves progress and continues when it arrives.',
      fact: 'Reports back later (waits for a reply)',
    },
    sync: {
      badge: 'Answers in the same call',
      help: 'The service answers in the same call, within the agent’s time limit.',
    },
    external: {
      badge: 'Changes external system',
      help: 'This call can change something outside Emberling, such as a deployment or a generated file.',
      fact: 'Changes things outside Emberling',
      factHelp:
        'This call can change something outside Emberling, such as a deployment or a generated file. Emberling never re-sends it on its own.',
    },
    notRetried: {
      badge: 'Not retried',
      help: 'Emberling never re-sends this call on its own. If it fails or times out, the agent stops instead of trying again.',
      keyed: ' Each call carries a request key, so the service can ignore a duplicate.',
    },
    readOnly: {
      badge: 'Read-only',
      help: 'This call reads data and changes nothing outside Emberling.',
    },
    saves: (fact: string) => `Saves ${fact}`,
    needs: (fact: string, passed: boolean) => `Needs ${passed ? 'a passed ' : ''}${fact}`,
    generationLimit: {
      badge: 'Counts toward generation limit',
      help: 'Each call uses one of the agent’s generation calls (see Limits).',
      fact: 'Uses 1 generation call',
    },
    factLabels: {
      effect: 'Effect',
      result: 'Result',
      records: 'Records',
      requires: 'Requires',
      limit: 'Limit',
    },
  },

  runDialog: {
    cancel: 'Cancel',
    start: 'Start run',
    startKey: '⌘ ↵',
    close: 'Close',
    version: (v: number) => `Workflow version ${v}`,
    fields: 'Fields',
    json: 'JSON',
    showAs: 'Show as',
    required: 'required',
    noInput: 'This workflow takes no run input.',
    missingTitle: 'Couldn’t load the run form.',
    submitFailed: 'Couldn’t start the run. The server didn’t respond. Try again.',
    summary: (n: number) => `Fix ${plural(n, 'field')} to start the run.`,
    fixJson: 'Fix the JSON to show it as fields.',
    isRequired: (label: string) => `${label} is required.`,
    isNumber: (label: string) => `${label} must be a number.`,
    isWhole: (label: string) => `${label} must be a whole number.`,
    isOneOf: (label: string, options: readonly string[]) =>
      `${label} must be one of: ${options.join(', ')}.`,
    isObject: (label: string) => `${label} must be a JSON object.`,
    lengthExactly: (label: string, n: number) =>
      `${label} must be exactly ${plural(n, 'character')}.`,
    lengthAtLeast: (label: string, n: number) =>
      `${label} must be at least ${plural(n, 'character')}.`,
    lengthAtMost: (label: string, n: number) =>
      `${label} must be at most ${plural(n, 'character')}.`,
    jsonAt: (line: number, col: number, msg: string) => `Line ${line}, column ${col}: ${msg}`,
    jsonEnds: 'the JSON ends before it is complete',
    starting: 'Starting…',
    waitUpload: 'Wait for the upload to finish.',
    loadingForm: 'Loading the run form…',
    untitled: 'Untitled workflow',
    useLast: 'Use last run’s input',
    useLastTip: (code: string, when: string) =>
      `Fills the form with the input of run ${code} from ${when}.`,
    again: (code: string, when: string) => `Starts with the input of run ${code} from ${when}.`,
    againOlder: (v: number, code: string, when: string) =>
      `Runs version ${v} again with the input of run ${code} from ${when}. Changes saved after that run aren’t included.`,
    image: {
      choose: 'Choose image',
      drop: 'or drop it here',
      replace: 'Replace',
      remove: 'Remove',
      earlier: 'Image from the earlier run',
      missing: 'Add an image to start the run.',
      replaceNamed: (name: string) => `Replace ${name}`,
      removeNamed: (name: string) => `Remove ${name}`,
    },
    photos: {
      add: 'Add photos',
      drop: 'or drop them here',
      earlier: (n: number) => `Photo ${n} from the earlier run`,
      word: (n: number) => plural(n, 'photo'),
      atLimit: (n: number) => `${plural(n, 'photo')} added. That’s the limit.`,
      addAtLeast: (min: number) => `Add at least ${plural(min, 'photo')}.`,
      addUpTo: (max: number) => `Add up to ${plural(max, 'photo')}.`,
      none: 'No photos added.',
      shortOf: (n: number, min: number) => `${plural(n, 'photo')} added. Add at least ${min}.`,
      upTo: (n: number, max: number) => `${plural(n, 'photo')} added. Add up to ${max}.`,
      added: (n: number) => `${plural(n, 'photo')} added.`,
      shortfall: (min: number) => `Add at least ${plural(min, 'photo')} to start the run.`,
    },
    hint: (types: string, max: string) =>
      types && max ? `${types}, up to ${max}` : types || (max ? `Up to ${max}` : ''),
    dropHint: (drop: string, hint: string) => (hint ? `${drop} (${hint})` : drop),
    uploading: (name: string) => `Uploading ${name}…`,
    uploadFailed: (name: string) => `Couldn’t upload ${name}. Try again.`,
    uploadRejected: (name: string, reason: string) => `Couldn’t upload ${name}: ${reason}.`,
    badType: (name: string, types: string) => `${name} isn’t a supported image. Use ${types}.`,
    tooBig: (name: string, max: string) => `${name} is larger than ${max}.`,
    tooMany: (max: number) => `You can add up to ${plural(max, 'photo')}.`,
    sizeOf: (name: string, size: string) => `${name}, ${size}`,
    or: (names: readonly string[]) =>
      names.length < 2
        ? names.join('')
        : `${names.slice(0, -1).join(', ')} or ${names.at(-1) ?? ''}`,
  },

  gallery: {
    title: 'Components',
  },

  runs,
  workflows,
  editor,

  listNames,
  plural,
} as const;
