/**
 * The words of the Runs page, the workflow Runs tab and the run view. Sentence case, one
 * idea per string; strings that take a count or a value are functions and handle the plural.
 */

const plural = (n: number, one: string, many = `${one}s`): string =>
  `${n.toLocaleString('en-US')} ${n === 1 ? one : many}`;

const listNames = (names: readonly string[]): string =>
  names.length < 2 ? names.join('') : `${names.slice(0, -1).join(', ')} and ${names.at(-1) ?? ''}`;

const capitalise = (s: string): string => (s ? `${s.charAt(0).toUpperCase()}${s.slice(1)}` : s);

/** Display names of the external services the Backend names by provider ID. */
const PROVIDER_NAME: Record<string, string> = {
  sandboxrunner: 'Sandbox runner',
  'mock-task-provider-v1': 'Mock task provider',
};

export const runs = {
  page: {
    title: 'Runs',
    runWorkflow: 'Run workflow',
    pickWorkflow: 'Pick a workflow to run',
    searchWorkflows: 'Search workflows',
    noWorkflowMatch: (q: string) => `No workflow matches “${q}”.`,
    noWorkflows: 'No workflows yet.',
    statusTabs: 'Filter by status',
    tabs: {
      all: 'All',
      running: 'Running',
      waiting: 'Waiting',
      failed: 'Failed',
      completed: 'Completed',
    },
    workflow: 'Workflow',
    allWorkflows: 'All workflows',
    started: 'Started',
    ranges: { all: 'Any time', '24h': 'Last 24 hours', '7d': 'Last 7 days' },
    clearFilters: 'Clear filters',
    goTo: 'Go to run by ID',
    goToNone: (q: string) => `No run has the ID “${q}”.`,
    goToMany: (q: string) => `More than one run starts with “${q}”. Type more of the ID.`,
    goToShort: 'Type at least 8 letters or digits of the run ID.',
    goToFailed: 'Couldn’t look up that run. Try again.',
    columns: {
      workflow: 'Workflow',
      status: 'Status',
      started: 'Started',
      duration: 'Duration',
      runId: 'Run ID',
    },
    runIdLabel: 'run ID',
    rowLabel: (name: string, status: string, when: string) => `${name}, ${status}, started ${when}`,
    noun: ['run', 'runs'] as const,
    olderVersion: 'Older version',
    empty: {
      title: 'No runs yet.',
      body: 'Run a workflow and its runs appear here.',
      action: 'Go to Workflows',
    },
    noMatch: 'No runs match these filters.',
    emptyTab: {
      all: 'No runs.',
      running: 'No runs are running.',
      waiting: 'No runs are waiting.',
      failed: 'No runs failed.',
      completed: 'No runs completed.',
    },
    showAll: 'Show all runs',
    shortcuts: {
      group: 'Runs',
      goTo: 'Go to run by ID',
      move: 'Move between rows',
      open: 'Open the run',
    },
  },

  /** One line about why a run stopped, or what it waits for, from the list's fields. */
  failure: {
    byCode: {
      TIMEOUT: (step: string | null) => `${step ?? 'A step'} ran out of time.`,
      MAX_TURNS: (step: string | null) =>
        `${step ?? 'The agent'} used all its turns without finishing.`,
      GENERATION_LIMIT_REACHED: (step: string | null) =>
        `${step ?? 'The agent'} reached its limit of generation calls.`,
      PRECONDITION_UNMET: () => 'A tool was blocked by a missing requirement.',
      EXECUTOR_ERROR: (step: string | null) => `${step ?? 'A step'} failed.`,
      MODEL_ERROR: (step: string | null) => `${step ?? 'The agent'}’s model call failed.`,
      TOOL_ERROR: (step: string | null) => `A tool call from ${step ?? 'the agent'} failed.`,
      INVALID_ACTION: (step: string | null) => `${step ?? 'The agent'}’s answer was rejected.`,
      FACT_BASIS_MISSING: () => 'A tool result couldn’t be traced to its source.',
    } as Record<string, (step: string | null) => string>,
    other: (step: string | null) => `${step ?? 'A step'} failed.`,
    unknown: 'The run failed.',
  },
  waitingFor: (steps: readonly string[]) =>
    steps.length
      ? `Waiting for ${listNames(steps)} to report back.`
      : 'Waiting for an outside service to report back.',
  olderTip: (used: number, latest: number) => {
    const n = latest - used;
    return `Ran on version ${used}. This workflow has been saved ${n === 1 ? '1 more time' : `${n} more times`} since this run. Opening it in the Editor shows the latest version.`;
  },
  /** "Failed in 22.6 s", "Waiting for 3 min 10 s". */
  inDuration: (d: string) => `in ${d}`,
  forDuration: (d: string) => `for ${d}`,

  /** The run list on the left of a workflow's Runs tab. */
  list: {
    title: 'Runs',
    label: 'Runs',
    active: (running: number, waiting: number) => {
      const parts = [running ? `${running} running` : '', waiting ? `${waiting} waiting` : ''];
      const s = parts.filter(Boolean).join(', ');
      return s || 'No active runs';
    },
    filter: 'Filter runs',
    filterActive: (n: number) => `Filter runs, ${plural(n, 'filter')} on`,
    status: 'Status',
    started: 'Started',
    statuses: {
      all: 'All statuses',
      running: 'Running',
      waiting: 'Waiting',
      failed: 'Failed',
      completed: 'Completed',
    },
    clear: 'Clear',
    clearFilters: 'Clear filters',
    hide: 'Hide the run list',
    show: 'Show the run list',
    resize: 'Resize the run list',
    empty: 'No runs yet.',
    emptyBody: 'Run this workflow and its runs appear here.',
    noMatch: 'No runs match these filters.',
    more: 'Show older runs',
    loadFailed: 'Couldn’t load the runs.',
    itemLabel: (code: string, when: string, status: string) =>
      `Run ${code}, started ${when}, ${status}`,
    shortcuts: 'Keyboard shortcuts',
  },

  view: {
    noRuns: 'This workflow hasn’t run yet.',
    noRunsBody: 'Start a run and it appears here.',
    run: 'Run',
    runAgain: 'Run again',
    runAgainTip: 'Start a new run with this run’s version and input (R)',
    resumeLive: 'Resume live',
    resumeLiveTip: 'Select the latest activity and keep following it (L)',
    ids: 'IDs',
    runId: 'Run ID',
    workflowId: 'Workflow ID',
    taskOf: (provider: string) => `${provider} task`,
    taskIdLabel: (provider: string) => `${provider.toLowerCase()} task ID`,
    olderVersion: (v: number) => `Ran on version ${v}, an older version`,
    olderVersionTip: (n: number) =>
      `This workflow has been saved ${n === 1 ? '1 more time' : `${n} more times`} since this run. Opening it in the Editor shows the latest version.`,
    reconnecting: (at: string) => `Reconnecting. Times shown are as of ${at}.`,
    stopped: 'Live updates stopped. New events won’t appear until you reload.',
    reload: 'Reload',
    showFailure: 'Show where it failed',
    showWait: 'Show the wait',
    waitingBanner: (step: string, who: string) => `${step} is waiting for ${who}.`,
    waitedSoFar: (d: string) => `It has waited ${d} so far.`,
    loadFailed: 'Couldn’t load this run.',
    copied: (code: string) => `Copied ${code}`,
    selected: (what: string) => `${what} selected.`,
  },

  graph: {
    label: (v: number) => `Workflow steps, version ${v}`,
    zoom: 'Zoom',
    zoomIn: 'Zoom in',
    zoomOut: 'Zoom out',
    fit: 'Fit view',
    turns: (n: number) => plural(n, 'turn'),
    cardLabel: (name: string, status: string, meta: string) =>
      meta ? `${name}, ${status}, ${meta}` : `${name}, ${status}`,
    loadFailed: 'Couldn’t load the workflow version this run used.',
  },

  media: {
    photo: (n: number) => `Photo ${String(n)}`,
    generatedFrom: (n: number) => `Generated from photo ${String(n)}`,
    photos: 'Photos',
    images: 'Images',
    review: 'Review',
    passed: 'Passed',
    passedOf: (n: number) => `of ${plural(n, 'photo')}`,
    policy: 'Policy',
    didNotPass: 'Did not pass',
  },

  logs: {
    view: 'Logs view',
    trace: 'Trace',
    events: 'Events',
    resize: 'Resize the logs panel',
    resizeSplit: 'Resize the details pane',
  },

  trace: {
    label: 'Trace',
    search: 'Search the trace',
    failuresOnly: 'Failures only',
    options: 'Trace options',
    expandAll: 'Expand all',
    collapseAll: 'Collapse to steps',
    bars: 'Timeline bars',
    columns: { name: 'Name', timeline: 'Timeline', start: 'Start', duration: 'Duration' },
    timelineTip:
      'Each bar spans the recorded start and end. Dashed: waiting for an outside service. An open end: not finished yet.',
    startTip: 'Time since the run started. Hover a value for the exact time.',
    barSpan: (from: string, to: string) => `${from} to ${to}`,
    barOpen: (from: string) => `Started at ${from}, not finished`,
    waitOpen: (from: string) => `Waiting since ${from}, not finished`,
    noMatch: (q: string, failuresOnly: boolean) =>
      `Nothing in this trace matches “${q}”${failuresOnly ? ' among the failures' : ''}.`,
    noMatchBody: 'Check the spelling or clear the search.',
    nothingFailed: 'Nothing failed in this run.',
    nothingFailedBody: 'Turn off Failures only to see every step.',
    expand: (name: string) => `Expand ${name}`,
    collapse: (name: string) => `Collapse ${name}`,
    didNotRun: 'Did not run',
    notStarted: 'Not started',
    run: 'Run',
    turn: (n: number, what: string) => `Turn ${n}: ${what}`,
    turnFinished: 'finished',
    turnDeciding: 'deciding',
    turnCalled: (tool: string) => `called ${tool}`,
    turnCalling: (tool: string) => `calling ${tool}`,
    turnToolFailed: (tool: string) => `${tool} failed`,
    turnBlocked: (tool: string) => `blocked from calling ${tool}`,
    group: (a: number, b: number, unit: string, reps: number) =>
      `Turns ${a}–${b}: ${unit}, ${reps} times`,
    decidedFinish: 'Decided to finish',
    decidedCall: (tool: string) => `Decided to call ${tool}`,
    modelDecision: 'Model decision',
    finalAnswer: 'Final answer',
    toolCall: 'Tool call',
    attempt: (n: number, max: number | null) => (max ? `Attempt ${n} of ${max}` : `Attempt ${n}`),
    waitingFor: (who: string) => `Waiting for ${who}`,
    waitedFor: (who: string) => `Waited for ${who}`,
    noResult: 'no result',
    resultFetched: 'result fetched',
    reportedBack: 'reported back',
    waitStatus: { live: 'Waiting for reply', none: 'No result', received: 'Reply received' },
    groupStatus: 'Succeeded',
  },

  events: {
    group: 'Event group',
    count: (n: number) => plural(n, 'event'),
    selectedOnly: 'Selected element only',
    selectedOnlyTip: (path: string) => `Only events of ${path} and what it contains`,
    selectFirst: 'Select a step, turn or call in the trace or graph first.',
    filter: 'Filter events',
    total: (n: number) => plural(n, 'event'),
    showing: (n: number, total: number) => `Showing ${n} of ${plural(total, 'event')}`,
    inElement: (head: string, path: string) => `${head} in ${path}`,
    columns: { seq: '#', time: 'Time', event: 'Event', element: 'Element', summary: 'Summary' },
    noMatch: 'No events match these filters.',
    noMatchBody: 'Pick another group, turn off Selected element only or clear the filter text.',
    clear: 'Clear filters',
    label: 'Events',
    forElement: 'Events for this element',
    summary: {
      version: (v: number) => `Workflow version ${v}`,
      attempt: (n: number) => `Attempt ${n}`,
      retry: (n: number, next: string | null) =>
        next ? `Attempt ${n} failed, next at ${next}` : `Attempt ${n} failed`,
      took: (d: string) => `Took ${d}`,
      upTo: (n: number) => `Up to ${plural(n, 'turn')}`,
      failed: 'Failed',
      stopped: 'Run stopped',
    },
  },

  details: {
    label: 'Details',
    close: 'Close details',
    closeTip: 'Close (Esc)',
    path: 'Location in this run',
    tabs: {
      overview: 'Overview',
      input: 'Input',
      output: 'Output',
      result: 'Result',
      arguments: 'Arguments',
      turns: 'Turns',
      facts: 'Saved results',
      events: 'Events',
      raw: 'Raw',
    },
    crumb: {
      run: 'Run overview',
      turns: (a: number, b: number) => `Turns ${a}–${b}`,
      turn: (n: number) => `Turn ${n}`,
      decision: 'Model decision',
      attempt: (n: number) => `Attempt ${n}`,
      wait: 'Wait',
    },
    kind: {
      run: 'Run',
      step: (type: string) => `${type} step`,
      attempt: 'Attempt',
      toolAttempt: 'Tool attempt',
      turn: 'Agent turn',
      group: 'Repeated turns',
      decision: 'Model decision',
      finalAnswer: 'Final answer',
      toolCall: 'Tool call',
      wait: 'Wait',
    },
    toolAttemptTitle: (tool: string, n: number) => `${tool}, attempt ${n}`,
    about: 'About this workflow',
    olderNote: (v: number) => `This run used version ${v}. The workflow has changed since.`,
    openLatest: 'Open the latest version in the Editor',
    completed: 'The run completed.',
    result: 'Result',
    facts: {
      started: 'Started',
      duration: 'Duration',
      tokens: 'Tokens',
      version: 'Workflow version',
      runId: 'Run ID',
      workflowId: 'Workflow ID',
      finished: 'Finished',
      notYet: 'Not yet',
      timeLimit: 'Time limit',
      perAttempt: 'per attempt',
      nextAttempt: 'Next attempt',
      scheduled: 'scheduled',
      attempt: 'Attempt',
      attempts: 'Attempts',
      ofUpTo: (n: number) => `of up to ${n}`,
      of: (n: number) => `of ${n}`,
      model: 'Model',
      turns: 'Turns',
      generationCalls: 'Generation calls',
      ended: 'Ended',
      tool: 'Tool',
      sent: 'Sent',
      service: 'Service',
      externalTask: 'External task',
      result: 'Result',
      pickedUp: 'Picked up',
      decision: 'Decision',
      action: 'Action',
      decided: 'Decided',
      agentState: 'Agent state',
      updated: 'Updated',
      unchanged: 'Unchanged',
      waitedFor: 'Waited for',
      waited: 'Waited',
      waitingFor: 'Waiting for',
      since: 'Since',
      deadline: 'Deadline',
      noneSet: 'None set',
      progress: 'Progress',
      latestEvent: 'Latest event',
      stepRunId: 'Step run ID',
      nodeId: 'Step ID',
      nodeType: 'Step type',
      agentRunId: 'Agent run ID',
      attemptId: 'Attempt ID',
      toolAttemptId: 'Tool attempt ID',
      turnId: 'Turn ID',
      actionId: 'Action ID',
      errorCode: 'Error code',
      message: 'Message',
    },
    versionLatest: (v: number) => `${v} (latest)`,
    versionOlder: (v: number, latest: number) => `${v} (the latest is ${latest})`,
    tokens: (n: number) => plural(n, 'token'),
    tokensTip: (i: number, o: number) =>
      `Input ${i.toLocaleString('en-US')}, output ${o.toLocaleString('en-US')}`,
    didNotRunBecause: 'Did not run because',
    failedSuffix: 'failed.',
    stoppedEarlier: 'Did not run because the run stopped earlier.',
    notStartedYet: 'Not started yet.',
    showTurns: 'Show turns',
    toolsItCouldUse: 'Tools it could use',
    sequence: {
      calledTimes: (tool: string, n: number) => (n > 1 ? `${tool} ${n} times` : tool),
      sentence: (
        called: string,
        end: {
          pending: string | null;
          blocked: string | null;
          failed: string | null;
          finished: boolean;
        },
      ): string => {
        let out = called ? `Called ${called}` : '';
        if (end.pending)
          out += out ? `, and is now waiting on ${end.pending}` : `Waiting on ${end.pending}`;
        else if (end.blocked)
          out += out
            ? `, then was blocked from calling ${end.blocked}`
            : `Was blocked from calling ${end.blocked}`;
        else if (end.failed) out += out ? `, then ${end.failed} failed` : `${end.failed} failed`;
        else if (end.finished) out += out ? ', then finished' : 'Finished without calling a tool';
        return out ? `${out}.` : '';
      },
    },
    notOnServer: 'Not available on this server.',
    allAttempts: 'All attempts',
    sameError: 'Failed with the same error as',
    attemptLower: (n: number) => `attempt ${n}`,
    blocked: 'The call was blocked before it started.',
    finalLead: (step: string) => `The agent’s answer became the output of ${step}.`,
    turnLead: {
      final: 'Decided to finish and passed on its answer.',
      call: (tool: string) => `Decided to call ${tool}`,
      SUCCEEDED: ', which succeeded.',
      FAILED: ', which failed.',
      BLOCKED: '; the call was blocked before it started.',
      WAITING_CALLBACK: ', which is waiting for its result.',
      RUNNING: ', which is running.',
      other: '.',
    },
    finish: 'Finish',
    call: (tool: string) => `Call ${tool}`,
    finishWithAnswer: 'Finish with an answer',
    notRecordedPrompt: 'The prompt and the model’s reply aren’t recorded.',
    receivedVia: {
      SYNC_EXECUTION: 'Returned directly',
      CALLBACK: 'By callback',
      PROVIDER_POLL: 'By polling',
    } as Record<string, string>,
    received: 'Received',
    noResult: 'No result',
    anOutsideService: 'An outside service',
    groupLead: (unit: string, reps: number) =>
      `The agent repeated ${unit} ${reps} times. Every call succeeded.`,
    turnsSection: 'Turns',
    io: {
      noInput: 'No input.',
      noOutput: 'No output.',
      outputLater: 'The output appears when the run completes.',
      outputFailed: 'The run failed before it produced an output.',
      argsGap: 'Arguments aren’t recorded for tool calls yet.',
      resultGap: 'Results aren’t recorded for tool calls yet.',
      gapBody: 'The trace keeps the tool name, timing and outcome.',
    },
    raw: {
      run: 'Run',
      definitionNode: 'Workflow step',
      nodeDetail: 'Step run and attempts',
      agent: 'Agent run, saved results and generation budget',
      turn: (n: number) => `Turn ${n}`,
      decision: (n: number) => `Turn ${n} decision`,
      action: (n: number) => `Turn ${n} action and tool attempts`,
      attempt: (n: number) => `Attempt ${n}`,
      toolAttempt: (n: number) => `Tool attempt ${n}`,
    },
    facts2: {
      noVerdict: 'No verdict',
      passed: 'Passed',
      failed: 'Failed',
      subject: 'Subject',
      turnLink: (n: number) => `Turn ${n}`,
      truncated: 'Only the first saved results are shown.',
    },
    turnsTab: {
      chooseFinish: 'Chose to finish with an answer',
      choose: (tool: string) => `Chose ${tool}`,
      blocked: (why: string) => `Blocked before it started: ${why}`,
      saved: (label: string) => `Saved ${label}`,
      verdict: (v: boolean | null) => (v === true ? ': passed' : v === false ? ': failed' : ''),
      became: (step: string) => `Became the output of ${step}`,
      repeated: (n: number) => `Repeated ${plural(n, 'time')}`,
      showEach: (n: number) => `Show each of the ${n} turns`,
      times: (n: number) => `×${n}`,
      allSucceeded: 'All succeeded',
      succeeded: 'Succeeded',
      gap: 'Tool arguments and results aren’t in the trace yet; results a tool saved are listed under its call.',
      notAllowed: 'not allowed',
    },
  },

  diagnosis: {
    failure: 'Failure',
    waiting: 'Waiting',
    effect: (names: readonly string[]) => `${listNames(names)} did not run.`,
    serviceFailed: (step: string) => `${step}’s service call failed`,
    eachAttemptLimit: (n: number, limit: string) =>
      n > 1
        ? `Each of its ${n} attempts reached the ${limit} time limit before the service answered.`
        : `Its attempt reached the ${limit} time limit before the service answered.`,
    allAttemptsFailed: (n: number) => (n > 1 ? `All ${n} attempts failed.` : 'The call failed.'),
    checkService: 'Check that the service it calls is reachable, then run again.',
    ranOutOfTime: (step: string) => `${step} ran out of time`,
    timeLimitIn: (limit: string, turn: number | null, tool: string | null) =>
      `It reached its ${limit} time limit${turn !== null && tool ? ` in turn ${turn}, while ${tool} was running` : ''}.`,
    changeTimeLimit: 'Change the time limit',
    blockedFrom: (step: string, tool: string | null) =>
      `${step} was blocked from calling ${tool ?? 'a tool'}`,
    needsFirst: (tool: string | null, noun: string, what: string) =>
      `${tool ?? 'The tool'} needs ${noun} to ${what} first, and no such result was saved.`,
    nounItem: 'the item',
    nounImage: 'the image',
    factGate: {
      asset_reviewed: 'pass review',
      patch_tested: 'pass its tests',
      image_generated: 'be generated',
    } as Record<string, string>,
    passing: (label: string) => `have a passing ${label.toLowerCase()}`,
    reviewInstructions: 'Review the instructions',
    limitReached: (step: string) => `${step} reached its generation limit`,
    usedOf: (used: string, limit: string, tool: string | null) =>
      `It had used ${used} of ${limit} generation calls when it tried to call ${tool ?? 'a tool'} again.`,
    changeGenerationLimit: 'Change the generation limit',
    stepFailed: (step: string) => `${step} failed`,
    undescribed: 'The server reported an error it did not describe.',
    openStep: 'Open step in the Editor',
    notRetried: (tool: string) =>
      `Not retried automatically: ${tool} changes an outside system and the result is unknown.`,
    checkTarget: 'Check the target before you run again.',
    waitingTitle: (step: string, who: string) => `${step} is waiting for ${who}`,
    turnCalled: (n: number, tool: string) =>
      `Turn ${n} called ${tool}. The run continues when the result arrives.`,
    continues: 'The run continues when the result arrives.',
    toReport: (who: string, tool: string) => `${who} to report the result of ${tool}`,
    sinceFor: (d: string) => `for ${d}`,
    timesOut: (step: string) =>
      `If no result arrives by then, ${step} stops and the run fails. Emberling decides this, not this page.`,
    saved: 'Saved. The wait survives a restart of Emberling.',
    sentAt: (at: string) => `sent ${at}`,
  },

  shortcuts: {
    runs: 'Runs',
    runsMove: 'In the run list: open the previous or next run',
    logs: 'Logs',
    trace: 'Trace',
    events: 'Events',
    moving: 'Moving around',
    move: 'Move between rows',
    jk: 'Move down or up, like ↓ and ↑',
    collapse: 'Collapse or expand a row',
    show: 'Show details',
    up: 'Up one level: attempt, tool call, turn, step, Run overview (closes the details drawer first when it is open)',
    traceGroup: 'Trace',
    search: 'Search the trace (in Events: filter events)',
    clearSearch: 'Clear the search',
    actions: 'Actions',
    failure: 'Go to the failure',
    live: 'Resume live',
    editor: 'Open in the Editor',
    again: 'Run again',
    copyId: 'Copy the selected ID',
    sheet: 'Show this list',
  },

  provider: (id: string | null | undefined): string =>
    id ? (PROVIDER_NAME[id] ?? id) : 'an outside service',
  providerCap: (id: string | null | undefined): string =>
    capitalise(id ? (PROVIDER_NAME[id] ?? id) : 'an outside service'),
  listNames,
  plural,
} as const;
