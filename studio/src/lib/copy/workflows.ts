/** Strings of the Workflows list page and its New workflow dialog. */

export const workflows = {
  title: 'Workflows',
  newWorkflow: 'New workflow',

  tabsLabel: 'Filter by last run',
  tabs: {
    all: 'All',
    failed: 'Last run failed',
    never: 'Not run yet',
  },

  search: {
    placeholder: 'Search by name or ID',
    label: 'Search workflows by name or ID',
  },
  sort: {
    label: 'Sort',
    updated: 'Recently updated',
    name: 'Name A–Z',
  },

  columns: {
    name: 'Name',
    lastRun: 'Last run',
    updated: 'Updated',
    workflowId: 'Workflow ID',
    actions: 'Actions',
  },

  /** The noun of the pager range: "1–20 of 65 workflows". */
  noun: ['workflow', 'workflows'] as const,
  /** The ID chip's label: "Copy workflow ID". */
  idLabel: 'workflow ID',

  notRunYet: 'Not run yet',
  /** The Last run cell's accessible name. */
  lastRunLink: (status: string, when: string, code: string) =>
    `Last run ${status.toLowerCase()}, ${when}. Open run ${code}`,
  /** Why the last run failed, from its error code and the step it names. */
  lastRunReason: (title: string, step: string | null) =>
    step ? `${title}. It stopped at ${step}.` : `${title}.`,

  rowMenu: {
    tip: 'Run, edit or see runs',
    run: 'Run',
    edit: 'Edit',
    runs: 'Runs',
  },

  states: {
    loadFailed: 'Couldn’t load workflows.',
    emptyTitle: 'No workflows yet.',
    emptyBody: 'Create one to define the steps a run will follow.',
    noMatch: (q: string, inTab: boolean) =>
      inTab ? `No workflows match “${q}” in this tab.` : `No workflows match “${q}”.`,
    clearSearch: 'Clear search',
    noFailed: 'No workflow’s last run failed.',
    noNever: 'Every workflow has run at least once.',
    showAll: 'Show all workflows',
  },

  shortcuts: {
    group: 'Workflows',
    search: 'Search workflows',
    open: 'Open the workflow',
    rowMenu: 'Open the row’s menu',
  },

  newDialog: {
    title: 'New workflow',
    name: 'Name',
    namePlaceholder: 'e.g. Release checker',
    nameMissing: 'Enter a name.',
    description: 'Description',
    cancel: 'Cancel',
    create: 'Create',
  },
} as const;
