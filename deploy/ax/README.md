# AX sandbox deployment

These scripts deploy Emberling with its sandbox runner on the `ax` backend to one
dedicated Linux host, so that the `sandbox_test` Tool runs each test as an AX Task. Tests
run in the gVisor sandbox of AX at commit `ac2332829f22360ff97b0ba34d94dd0dd782f17e`.
The sandbox isolates the test from the host and from credentials; it does not make the
test verdict trustworthy. A pass means only that the fixture's `go test` exited 0 inside
the sandbox.

This is an experiment and test setup, not a production deployment. Test controls are on,
every service binds to the host's loopback interface, and you reach Studio through SSH
port forwarding.

## What runs where

| Where | What |
| --- | --- |
| Your machine | `axctl.sh`, which ships `remote/` to the host and runs one script per step over SSH |
| Host, kind cluster `kind` | Substrate, AX (`ax-server`, Redis with a persistent volume, WorkerPool `ax-workers/ax`), atespace `emberling-sandbox` |
| Host, local registry `localhost:5001` | The sandbox supervisor image, referenced only by digest |
| Host, Docker | PostgreSQL 18 on `127.0.0.1:55432`; Studio on `127.0.0.1:5173` (optional) |
| Host processes | Backend `:8080`, Mock Provider `:9101`, mock production service `:9102`, sandbox runner `:9103`, router port-forward `:8000`, all on `127.0.0.1` |

Everything the scripts create on the host lives under `~/emberling-ax`: `deploy/` (the
shipped scripts), `upstream/` (pinned AX and Substrate checkouts), `emberling/` (the
source tree sent by the image step) and `run/` (binaries, logs, pid files, generated
secrets and request records).

## Pinned versions

| Component | Version |
| --- | --- |
| AX | `ac2332829f22360ff97b0ba34d94dd0dd782f17e` |
| Substrate | `944abe3278b895ccbf5d45555a49dd0f2f6ceae7` (the version AX requires) |
| Go | 1.27.2 (checksum verified) |
| kubectl | v1.37.1 (checksum verified) |
| kind | v0.33.0 |
| ko | v0.19.1 |
| Docker Engine | `5:29.9.0-1~ubuntu.24.04~noble`, containerd.io `2.4.1-2~ubuntu.24.04~noble` |

Results are claimed only for this combination. The supervisor image is built from the
Emberling commit you send and is always used by digest
(`localhost:5001/emberling-sandboxsupervisor@sha256:...`); the runner refuses a tag.

## Prerequisites

- A dedicated x86-64 host running Ubuntu 24.04 with at least 8 cores, 16 GB of memory and
  100 GB of free disk. KVM is not needed: without `/dev/kvm` the cluster script disables
  micro-VMs and gVisor still works.
- An SSH alias for the host in your `~/.ssh/config`, for a user with `sudo`. The scripts
  never store the alias; you pass it as the first argument every time.
- Outbound HTTPS from the host to GitHub, the Go module proxy, Docker Hub and the
  Ubuntu and Docker package mirrors. The host needs no Git or cloud credentials: AX and
  Substrate are public, and the Emberling source is sent with `git archive`.
- On your machine: Bash, Git, SSH and tar. Run `axctl.sh` from a clone of this repository.

## Limits to keep in mind

- **One runner per atespace.** At startup the runner deletes every resource whose name
  starts with `emberling-sbx-` in `emberling-sandbox`. Never point a second runner at the
  same atespace.
- **Inline workspace files suit small input only.** The patched fixture travels inside
  the AX Workspace record. Keep the patched tree at or below 16 KiB of file content.
  Files whose content starts with a space or a tab are refused, because AX cannot encode
  them losslessly.
- **No network from a test.** Outbound network requests from inside the sandbox are
  refused. Tests must not need the network; the supervisor image runs `go test` with
  `GOPROXY=off` against a stdlib-only fixture.
- **No credentials in the sandbox.** The Task carries an empty environment and no Model is
  configured for the atespace. The callback signing secret and the Model Provider key
  exist only in the Backend's environment on the host.
- **Task resources are not enforced by AX.** The WorkerPool's per-worker limits (2 CPUs,
  2 GiB) bound a test instead.

## Secrets and settings

`start` generates the callback signing secret and the Model Provider key on the host,
with `openssl rand`, into `~/emberling-ax/run/state/secrets.env` (mode 0600) the first time
it runs. They are loaded only into the Backend's environment and never printed, logged or
copied back. Delete that file and run `start` again to rotate them.

Optional settings go in an env file on your machine that is never committed. `start`
uses `deploy/ax/.env` when it exists (Git ignores it) or the file given with
`--env-file`, and copies it to the host with mode 0600. It may set:

| Variable | Default | Effect |
| --- | --- | --- |
| `SANDBOXRUNNER_TEST_TIMEOUT` | `300s` | Bound on one test, creation to result |
| `SANDBOXRUNNER_AX_READY_TIMEOUT` | `120s` | Bound on the wait for a Task to become ready |
| `EMBERLING_RECONCILE_INTERVAL` | `5s` | Reconciler scan interval |
| `EMBERLING_MODELS`, `EMBERLING_MODEL_BASE_URL`, `EMBERLING_MODEL_API_KEY` | empty | Optional real decision models, all three together |

Addresses, ports, the atespace and the image are fixed by the scripts.

## Step 1: deploy

From the repository root on your machine (`<alias>` is your SSH alias):

```sh
deploy/ax/axctl.sh <alias> prepare
```

Installs the pinned tools; takes a few minutes the first time and seconds after that.
Expected tail:

```text
==> tool versions
docker-ce 5:29.9.0-1~ubuntu.24.04~noble
go version go1.27.2 linux/amd64
Client Version: v1.37.1
kind v0.33.0 go1.27.2 linux/amd64
ko v0.19.1
```

The first run adds your user to the `docker` group. Each step runs in a new SSH session,
so the next step sees the group; if you use SSH connection sharing, close the shared
connection first (`ssh -O exit <alias>`).

```sh
deploy/ax/axctl.sh <alias> cluster
```

Checks out the pinned AX and Substrate commits, creates the kind cluster with its local
registry and installs Substrate (about 4 to 7 minutes). Running it again keeps an
existing cluster; `--reinstall` reinstalls Substrate and `--recreate` rebuilds the
cluster. Checkpoint, at the end of the output:

```text
/dev/kvm not available: micro-VM support disabled (gVisor still works).
==> Substrate status
deployment "ate-api-server" successfully rolled out
deployment "ate-controller" successfully rolled out
deployment "atenet-router" successfully rolled out
deployment "atenet-egress" successfully rolled out
daemon set "atelet-944abe32" successfully rolled out
NAME             CLASS    AGE
gvisor-default   gvisor   ...
5000/tcp -> 127.0.0.1:5001
```

The KVM line appears only on hosts without `/dev/kvm`. Full logs are in
`~/emberling-ax/run/logs/create-kind-cluster.log` and `install-ate-system.log`.

## Step 2: create the AX control plane and the atespace

```sh
deploy/ax/axctl.sh <alias> control-plane
```

Deploys Redis with append-only persistence on a volume, `ax-server` with snapshots in
the cluster's own object storage, and the WorkerPool; builds `ax` and `kubectl-ate`; and
creates the atespace. Running it again changes nothing that is already in place.
Checkpoints:

```text
deployment "ax-redis" successfully rolled out
deployment "ax-server" successfully rolled out
NAME   DESIRED   REPLICAS   READY   AGE
ax     2         2          2       ...
ax version v1alpha1 (standalone redis engine)
==> atespace emberling-sandbox
==> Models configured for emberling-sandbox (expected: none)
NAME   ATESPACE   PROVIDER   MODEL
```

The Models table must stay empty: no Model credential belongs in this atespace.

```sh
deploy/ax/axctl.sh <alias> image
```

Sends this repository's `HEAD` (only committed content; a warning names uncommitted
changes) and builds and pushes the sandbox supervisor image for linux/amd64.
`--source DIR` sends another clone instead. The last line is the digest reference the
runner will use, also stored in `~/emberling-ax/run/state/supervisor-image.ref`:

```text
localhost:5001/emberling-sandboxsupervisor@sha256:<64 hex digits>
```

Then confirm that the atespace holds no runner resources:

```sh
deploy/ax/axctl.sh <alias> inventory
```

```text
==> resources named emberling-sbx-* in atespace emberling-sandbox
AX Tasks: none
AX Workspaces: none
Substrate actors: none
Substrate actor templates: none
inventory empty
```

## Step 3: start the Backend, the runner and Studio

```sh
deploy/ax/axctl.sh <alias> start --studio
```

Starts PostgreSQL, builds and starts the Mock Provider, the mock production service, the
router port-forward, the sandbox runner (`SANDBOXRUNNER_BACKEND=ax`, test controls on) and
the Backend, then builds and starts Studio. Without `--studio` Studio is skipped. Running
`start` again restarts the processes on freshly built binaries. Expected status:

```text
PROCESS          STATE    HTTP URL
mockprovider     running  200 http://127.0.0.1:9101/healthz
mockproduction   running  200 http://127.0.0.1:9102/healthz
router-forward   running  404 http://127.0.0.1:8000/healthz
sandboxrunner    running  200 http://127.0.0.1:9103/healthz
backend          running  200 http://127.0.0.1:8080/ready
studio           running  200 http://127.0.0.1:5173/
postgres         running
supervisor image: localhost:5001/emberling-sandboxsupervisor@sha256:...
```

The router answers 404 to a request without an actor header, which is how a working
port-forward looks. `~/emberling-ax/run/logs/sandboxrunner.log` shows
`sandboxrunner: ax backend ready` with the image digest, the atespace and
`orphans_removed`. `deploy/ax/axctl.sh <alias> status` prints the table again at any time.

Open Studio from your machine through an SSH tunnel and browse to
`http://localhost:5173`:

```sh
ssh -N -L 5173:127.0.0.1:5173 <alias>
```

Studio proxies `/api` to the Backend on the host, so this one tunnel is enough. Add
`-L 8080:127.0.0.1:8080` to call the Backend API directly.

## Step 4: run the checks

Run these on the host (`ssh <alias>`), in `~/emberling-ax/emberling`. They need `curl`
and `jq`, which the prepare step installs.

**Software delivery scenario.** The Agent tests a patch through `sandbox_test`, which now
runs in AX, then deploys it once:

```sh
cd ~/emberling-ax/emberling
bash scripts/demo/delivery.sh main
```

Expect every check to print `PASS`, a `COMPLETED` Run and one passing test. The script
holds the runner's callback until the Agent waits for it, then releases it. While the
test runs, `ax get tasks -a emberling-sandbox` (with `PATH=~/emberling-ax/run/bin:$PATH`)
shows the Task; after it finishes the inventory is empty again.

**Backend restart while a test is outstanding.** Hold the runner's callbacks, start the
same scenario from Studio or the API, and kill the Backend while the Agent waits:

```sh
curl -fsS -X POST -H 'Content-Type: application/json' --data '{"kinds":["callback"]}' \
  http://127.0.0.1:9103/control/pause
# start a Run, then wait until GET /control/barrier lists one held callback
curl -fsS http://127.0.0.1:9103/control/barrier
```

From your machine, `deploy/ax/axctl.sh <alias> kill-restart backend` sends SIGKILL to the
Backend and starts it again (`backend restarted and healthy`). Then release the
callback:

```sh
curl -fsS -X POST http://127.0.0.1:9103/control/release
```

Expect the Run to reach `COMPLETED` with one `sandbox_test` Attempt and one deployment.

**Duplicate callback.** After a Run has completed, restart the runner:

```sh
deploy/ax/axctl.sh <alias> kill-restart sandboxrunner
```

The runner delivers its stored result again. In
`~/emberling-ax/run/records/sandboxrunner-test-record.jsonl` a `callback` entry with
`"attempt":2` (or higher) and `"httpStatus":200` appears for the same `testId`, and the
Run stays `COMPLETED` with no Event after its terminal Event.

**Isolation.** Submit a Run whose patch adds a test that makes an outbound request. The
test passes only when the request is refused:

```text
--- /dev/null
+++ b/egress_test.go
@@ -0,0 +1,16 @@
+package greet
+
+import (
+	"net/http"
+	"testing"
+	"time"
+)
+
+func TestOutboundRequestRefused(t *testing.T) {
+	client := &http.Client{Timeout: 5 * time.Second}
+	resp, err := client.Get("https://example.com/")
+	if err == nil {
+		resp.Body.Close()
+		t.Fatalf("outbound request answered %s", resp.Status)
+	}
+}
```

Use it as the `patch` of the delivery task with base commit `fixture-v1`. Expect a passing
test: outbound network requests are refused inside the sandbox. A failing test with
`outbound request answered` in its output means the sandbox reached the network; stop
and investigate before running anything else.

After every check, run the inventory again and expect `inventory empty`.

## Step 5: inventory and teardown

```sh
deploy/ax/axctl.sh <alias> inventory
```

Exits 0 with `inventory empty`, or 3 and lists what remains. AX lists only the Tasks and
Workspaces its Redis store holds, so the inventory also lists Substrate actors and actor
templates with `kubectl ate get actors` and `kubectl ate get actor-templates`; an actor
whose Task record was lost shows up only there. To clean up, stop the runner first
(`deploy/ax/axctl.sh <alias> stop`) so it does not race the cleanup, then:

```sh
deploy/ax/axctl.sh <alias> inventory --delete
```

It deletes Tasks and Workspaces with `ax`, then any remaining actor with
`kubectl ate delete actor <name> -a emberling-sandbox --any-state`, and templates last.
Delete an actor before its template: an actor whose template is gone can no longer be
suspended.

To stop the host processes and PostgreSQL but keep the cluster:

```sh
deploy/ax/axctl.sh <alias> stop
```

To remove the deployment:

```sh
deploy/ax/axctl.sh <alias> teardown
```

Stops everything, deletes the kind cluster and the local registry, and keeps the
PostgreSQL volume, the request records, the generated secrets and the pinned checkouts.
`teardown --purge-data` also removes the volume, the records, the secrets and the stored
assets. Files outside `~/emberling-ax` are never touched. `deploy/ax/axctl.sh <alias> all`
runs steps 1 to 3 and the inventory in one go after a teardown.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| `permission denied` on the Docker socket | Open a new SSH session after `prepare` (`ssh -O exit <alias>` when sharing connections) |
| `sandboxrunner` not healthy | `~/emberling-ax/run/logs/sandboxrunner.log`; the ax CLI must reach the cluster through the `kind-kind` context |
| `router-forward` not answering | `~/emberling-ax/run/logs/router-forward.log`; the loop restarts the port-forward after it exits |
| A test fails with `SANDBOX_ERROR` | `kubectl ate get actors -a emberling-sandbox` and `kubectl -n ax-system logs deploy/ax-server` |
| Inventory not empty after the runner stopped | `inventory --delete`, as above |
