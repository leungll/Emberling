import { Ellipsis, Play, Plus, Settings, Trash2, X } from 'lucide-react';
import { useState, type ReactNode } from 'react';

import { FactList, TechnicalDetails } from '@/components/facts/FactList';
import { IdChip } from '@/components/id/IdChip';
import { JsonViewer } from '@/components/json/JsonViewer';
import type { PageSize } from '@/components/list/page-size';
import { Pager } from '@/components/list/Pager';
import { Select } from '@/components/list/Select';
import { TimeZonePicker } from '@/components/prefs/TimeZonePicker';
import { Skeleton, StateBlock } from '@/components/state/StateBlock';
import { StatusIndicator } from '@/components/status/StatusIndicator';
import {
  Deadline,
  Duration,
  LiveDuration,
  Offset,
  RelativeTime,
  Timestamp,
} from '@/components/time/Time';
import { Banner, Callout } from '@/components/ui/banner';
import { Button, IconButton } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { Field, TextArea, TextInput } from '@/components/ui/field';
import { Kbd } from '@/components/ui/kbd';
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from '@/components/ui/menu';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Segmented } from '@/components/ui/segmented';
import { Tabs } from '@/components/ui/tabs';
import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import { showToast } from '@/lib/toast';
import { useShortcutsStore, useShortcutGroups } from '@/stores/shortcuts-store';

/**
 * The development gallery: every shared component in each of its states, for the UI review
 * suite to screenshot and check. Registered only in development builds. The sample text is
 * demonstration data, not product copy.
 */

const RUN_ID = 'run_d9c23ab668c54acf9ec83c59c55ce742';
const WF_ID = 'wf_f219e36bd99c403ba9158961cc29ac07';

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3 rounded-lg border border-border bg-surface p-4">
      <h2 className="m-0 text-md font-semibold">{title}</h2>
      {children}
    </section>
  );
}

function Row({ children }: { children: ReactNode }) {
  return <div className="flex flex-wrap items-center gap-3">{children}</div>;
}

export function UiGallery() {
  const copy = useCopy();
  const [dialogOpen, setDialogOpen] = useState(false);
  const [view, setView] = useState<'fields' | 'json'>('fields');
  const [tab, setTab] = useState<'all' | 'failed' | 'waiting'>('all');
  const [model, setModel] = useState('opus');
  const [page, setPage] = useState(1);
  const [size, setSize] = useState<PageSize>(20);
  const setSheetOpen = useShortcutsStore((s) => s.setOpen);
  useShortcutGroups([
    {
      title: copy.shortcuts.groups.everywhere,
      rows: [
        { keys: ['?'], description: copy.shortcuts.openSheet },
        { keys: ['['], description: copy.shortcuts.toggleSidebar },
        { keys: [copy.shortcuts.keys.esc], description: copy.shortcuts.goUp },
      ],
    },
    {
      title: copy.shortcuts.groups.lists,
      rows: [
        {
          keys: [copy.shortcuts.keys.up, copy.shortcuts.keys.down],
          description: copy.shortcuts.moveRows,
        },
        { keys: [copy.shortcuts.keys.enter], description: copy.shortcuts.openRow },
        { keys: [copy.shortcuts.keys.shiftF10], description: copy.shortcuts.rowMenu },
      ],
    },
  ]);
  const total = 65;
  // Sample times are fixed once, relative to when the gallery opened.
  const [now] = useState(() => Date.now());
  const ago = (ms: number) => new Date(now - ms).toISOString();
  const start = now - 22_612;

  return (
    <main className="min-h-screen bg-bg p-6 text-fg">
      <div style={{ maxWidth: 1280 }} className="mx-auto flex flex-col gap-4">
        <header className="flex items-center gap-3">
          <h1 className="m-0 text-xl font-semibold">{copy.gallery.title}</h1>
          <span className="flex-1" />
          <TimeZonePicker />
        </header>

        <Section title="Buttons">
          <Row>
            <Button>
              <Play aria-hidden="true" />
              Run
            </Button>
            <Button variant="secondary">Run again</Button>
            <Button variant="ghost">Resume live</Button>
            <Button variant="danger">
              <Trash2 aria-hidden="true" />
              Delete
            </Button>
            <Button variant="link">Clear filters</Button>
          </Row>
          <Row>
            <Button size="sm" variant="secondary">
              <Plus aria-hidden="true" />
              Small
            </Button>
            <Button size="lg">Start run</Button>
            <Button unavailableReason="Save your changes to run. A run always uses a saved version.">
              Run
            </Button>
            <IconButton label="Settings">
              <Settings aria-hidden="true" />
            </IconButton>
            <IconButton label="Close" size="xs">
              <X aria-hidden="true" />
            </IconButton>
            <IconButton label="More actions" size="md">
              <Ellipsis aria-hidden="true" />
            </IconButton>
            <Kbd>?</Kbd>
            <Kbd>⌘S</Kbd>
          </Row>
        </Section>

        <Section title="Status">
          <Row>
            <StatusIndicator status={{ kind: 'run', value: 'RUNNING' }} focusable />
            <StatusIndicator status={{ kind: 'run', value: 'PAUSED' }} />
            <StatusIndicator status={{ kind: 'run', value: 'COMPLETED' }} />
            <StatusIndicator status={{ kind: 'run', value: 'FAILED' }} colourLabel />
            <StatusIndicator status={{ kind: 'step', value: 'READY' }} />
            <StatusIndicator status={{ kind: 'step', value: 'NONE' }} />
            <StatusIndicator status={{ kind: 'step', value: 'NONE_RUN_FAILED' }} />
            <StatusIndicator status={{ kind: 'step', value: 'WAITING_CALLBACK' }} endedWait />
          </Row>
          <Row>
            <StatusIndicator status={{ kind: 'runLong', value: 'PAUSED' }} size="header" pill />
            <StatusIndicator status={{ kind: 'runLong', value: 'FAILED' }} size="header" pill />
            <StatusIndicator status={{ kind: 'run', value: 'COMPLETED' }} pill />
            <StatusIndicator status={{ kind: 'step', value: 'FAILED' }} size="icon" />
            <StatusIndicator status={{ kind: 'connection', value: 'live' }} />
          </Row>
        </Section>

        <Section title="IDs and times">
          <Row>
            <IdChip id={RUN_ID} kind="Run ID" label="run ID" />
            <IdChip id={WF_ID} kind="Workflow ID" label="workflow ID" size="md" />
            <IdChip id={RUN_ID} label="run ID" to={`/runs/${RUN_ID}`} match="c23a" />
            <IdChip id={RUN_ID} copy={false} />
          </Row>
          <FactList
            facts={[
              { label: 'Started', value: <Timestamp iso={ago(26 * 60_000)} /> },
              { label: 'Updated', value: <RelativeTime iso={ago(26 * 60_000)} /> },
              { label: 'Last run', value: <RelativeTime iso={ago(3 * 86_400_000)} /> },
              {
                label: 'Step started',
                value: (
                  <Offset
                    iso={new Date(start + 3200).toISOString()}
                    start={new Date(start).toISOString()}
                  />
                ),
              },
              { label: 'Duration', value: <Duration ms={22_612} /> },
              { label: 'So far', value: <LiveDuration start={ago(36_600)} /> },
              {
                label: 'Deadline',
                value: <Deadline iso={new Date(now + 240_000).toISOString()} />,
              },
              { label: 'Tokens', value: null },
              {
                label: 'Run ID',
                value: <IdChip id={RUN_ID} label="run ID" />,
                note: 'The full ID is in the tooltip.',
              },
            ]}
          />
          <TechnicalDetails storageKey="gallery">
            <pre className="m-0 font-mono text-xs">EXECUTOR_ERROR</pre>
          </TechnicalDetails>
        </Section>

        <Section title="States">
          <div className="grid grid-cols-2 gap-3">
            <StateBlock
              kind="empty"
              title="No workflows yet."
              body="Create one to define the steps a run will follow."
              action={{ label: 'New workflow', onClick: () => undefined, primary: true }}
            />
            <StateBlock
              kind="nomatch"
              title="No runs match these filters."
              action={{ label: 'Clear filters', onClick: () => undefined }}
            />
            <StateBlock
              kind="error"
              title="Couldn’t load runs."
              code="503 DEPENDENCY_UNAVAILABLE"
              action={{ label: copy.retry, onClick: () => undefined }}
            />
            <StateBlock
              kind="notfound"
              title={copy.notFound.run.title('d9c23ab6')}
              body={copy.notFound.run.body}
              code={RUN_ID}
              action={{ label: copy.notFound.run.action, to: '/runs' }}
            />
          </div>
          <Skeleton kind="rows" count={3} />
          <Skeleton kind="text" count={3} />
        </Section>

        <Section title="Banners and callouts">
          <Banner
            tone="failed"
            action={
              <Button variant="secondary" size="sm">
                Show where it failed
              </Button>
            }
          >
            Deploy failed: Couldn’t reach its service.
          </Banner>
          <Banner tone="waiting">Waiting for the sandbox runner to reply.</Banner>
          <Banner tone="caution">Reconnecting. Times shown are as of 14:32.</Banner>
          <Callout>Changes stay in the panel when you close this.</Callout>
          <Callout tone="danger">The run couldn’t start.</Callout>
          <Callout tone="caution">This call can change something outside Emberling.</Callout>
        </Section>

        <Section title="Tabs, segmented and fields">
          <Segmented
            label="Show as"
            value={view}
            onValueChange={setView}
            options={[
              { value: 'fields', label: 'Fields' },
              { value: 'json', label: 'JSON' },
            ]}
          />
          <Tabs
            label="Run status"
            value={tab}
            onValueChange={setTab}
            items={[
              { value: 'all', label: 'All', count: 1240 },
              { value: 'failed', label: 'Failed', count: 3 },
              { value: 'waiting', label: 'Waiting', count: 1 },
            ]}
          >
            <p className="m-0 py-2 text-sm text-fg-muted">Rows for the {tab} tab.</p>
          </Tabs>
          <div className="grid grid-cols-2 gap-4">
            <Field label="Name" hint="Shown in lists and in the run header.">
              {(p) => <TextInput {...p} placeholder="e.g. Release checker" />}
            </Field>
            <Field label="Max turns" optional error="Enter a whole number from 1 to 50.">
              {(p) => <TextInput {...p} defaultValue="0" />}
            </Field>
            <Field label="Instructions" required>
              {(p) => <TextArea {...p} rows={3} />}
            </Field>
            <Field label="Model">
              {(p) => (
                <Select
                  id={p.id}
                  label="Model"
                  value={model}
                  onChange={setModel}
                  search="Search models"
                  options={[
                    { value: 'opus', label: 'Claude Opus', description: 'Plans and calls tools' },
                    {
                      value: 'sonnet',
                      label: 'Claude Sonnet',
                      description: 'Balanced speed and depth',
                    },
                    { value: 'seedream', label: 'Seedream', description: 'Generates images' },
                  ]}
                />
              )}
            </Field>
          </div>
        </Section>

        <Section title="Overlays">
          <Row>
            <Menu>
              <MenuTrigger asChild>
                <IconButton label="Actions for Release checker">
                  <Ellipsis aria-hidden="true" />
                </IconButton>
              </MenuTrigger>
              <MenuContent>
                <MenuItem icon={<Play aria-hidden="true" />}>Run</MenuItem>
                <MenuItem icon={<Settings aria-hidden="true" />} shortcut="E">
                  Open in the Editor
                </MenuItem>
                <MenuSeparator />
                <MenuItem icon={<Trash2 aria-hidden="true" />} danger>
                  Delete
                </MenuItem>
              </MenuContent>
            </Menu>
            <Popover>
              <PopoverTrigger asChild>
                <Button variant="secondary">Filters</Button>
              </PopoverTrigger>
              <PopoverContent label="Filters" className="p-3">
                <Button variant="link">Clear filters</Button>
              </PopoverContent>
            </Popover>
            <Tooltip content="Times are shown in Shanghai time (UTC+8).">
              <Button variant="ghost">Tooltip</Button>
            </Tooltip>
            <Button variant="secondary" onClick={() => setDialogOpen(true)}>
              Open dialog
            </Button>
            <Button
              variant="secondary"
              onClick={() => showToast('Saved as version 4.', { done: true })}
            >
              Toast
            </Button>
            <Button
              variant="secondary"
              onClick={() =>
                showToast('Step deleted.', { action: { label: 'Undo', onAction: () => undefined } })
              }
            >
              Toast with action
            </Button>
            <Button variant="secondary" onClick={() => setSheetOpen(true)}>
              {copy.shortcuts.title}
            </Button>
          </Row>
          <Dialog
            open={dialogOpen}
            onClose={() => setDialogOpen(false)}
            title="Run workflow"
            description="Workflow version 3"
            size="lg"
            onSubmit={() => setDialogOpen(false)}
            footer={
              <>
                <Button variant="secondary" onClick={() => setDialogOpen(false)}>
                  {copy.cancel}
                </Button>
                <Button data-dialog-primary onClick={() => setDialogOpen(false)}>
                  Start run
                </Button>
              </>
            }
          >
            <Field label="Topic">{(p) => <TextInput {...p} />}</Field>
          </Dialog>
        </Section>

        <Section title="Lists and data">
          <div className="rounded-lg border border-border">
            <Pager
              from={(page - 1) * size + 1}
              to={Math.min(page * size, total)}
              total={total}
              noun={['run', 'runs']}
              size={size}
              onSize={(s) => {
                setSize(s);
                setPage(1);
              }}
              hasPrevious={page > 1}
              hasNext={page * size < total}
              onPrevious={() => setPage((p) => p - 1)}
              onNext={() => setPage((p) => p + 1)}
            />
          </div>
          <JsonViewer
            root="output"
            value={{
              summary: 'Patch tested and deployed.',
              facts: [{ type: 'patch_tested', verdict: 'PASSED' }],
              usage: { inputTokens: 69, outputTokens: 1 },
              note: 'x'.repeat(240),
              nested: '{"kind": "ASSET", "assetId": "asset_1"}',
              done: true,
              missing: null,
            }}
          />
        </Section>
      </div>
    </main>
  );
}
