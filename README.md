# siesta

**Scheduled scale-down for your Kubernetes node pools.** Run your cluster at full
size during the day, shrink it to (almost) nothing at night — and stop paying for
idle nodes.

siesta is a tiny Go tool that sets a node pool — or a single availability zone /
machine type within it — to an exact node count. It does **not** run a scheduler
itself: you trigger it from whatever cron you already have (a Huawei
FunctionGraph timer, AWS EventBridge, a Kubernetes CronJob, plain `crontab`…) and
each run applies the size you ask for.

Today it supports **Huawei Cloud CCE**. The internals are provider-agnostic, so
other clouds can be added later.

```bash
# 3 nodes now
siesta --cluster <id> --nodepool prod --count 3 --region ap-southeast-4

# 0 nodes (e.g. overnight)
siesta --cluster <id> --nodepool prod --count 0 --region ap-southeast-4
```

It's **idempotent** (no change if already at the target), supports **`--dry-run`**,
and exits non-zero on failure so your cron notices.

## Contents

- [Concepts](#concepts) — node pool vs scale group (read this first)
- [Install](#install)
- [Credentials](#credentials)
- [Commands](#commands)
- [Configuration reference](#configuration-reference)
- [Scheduling](#scheduling) — cron and Huawei FunctionGraph
- [IAM permissions](#iam-permissions)
- [Behavior & caveats](#behavior--caveats)
- [Development](#development)

## Concepts

A CCE **node pool** is made of one or more **scale groups**, each bound to a
single *(availability zone, machine type/flavor)*. Scaling can target either
level:

| Level | What you set | API used |
|---|---|---|
| **Whole pool** | total node count; CCE places nodes itself | `UpdateNodePool` |
| **One scale group** | the count for a specific AZ/flavor | `ScaleNodePool` |

Run [`describe`](#describe) to see the groups in a pool:

```
node pool prod (np-xxxx) — total 3
GROUP                             AZ               FLAVOR        DESIRED  EXISTING  AUTOSCALING
default                           ap-southeast-4a  s7n.xlarge.2  2        2         false
is7nxlarge2-apsoutheast4b-66049y  ap-southeast-4b  s7n.xlarge.2  0        0         false
ts7nxlarge2-apsoutheast4c-69556l  ap-southeast-4c  s7n.xlarge.2  1        1         false
```

> **Important:** the **node pool name** (`prod` above) is what you pass to
> `--nodepool` / `SIESTA_NODEPOOL`. The long strings in the `GROUP` column are
> **scale groups** — select those with `--az` / `--flavor` / `--scale-group`,
> **not** with `--nodepool`. Mixing these up is the #1 source of
> "node pool not found" errors.

**Why target a single zone?** If a workload mounts a **zonal disk (EVS/PV)**, its
pod can only run on a node in that disk's AZ. Scaling the whole pool — or using an
AZ-balancing policy — gives no guarantee a node lands in the right zone. Targeting
the zone's scale group does.

## Install

Download a binary from the [releases page](https://github.com/rahadiangg/siesta/releases)
(Linux / macOS / Windows, amd64 / arm64), or:

```bash
go install github.com/rahadiangg/siesta/cmd/siesta@latest
# or
git clone https://github.com/rahadiangg/siesta && cd siesta && make build
```

## Credentials

For the CLI, set these environment variables:

| Variable | What |
|---|---|
| `HUAWEICLOUD_SDK_AK` | access key |
| `HUAWEICLOUD_SDK_SK` | secret key |
| `HUAWEICLOUD_PROJECT_ID` | project ID (per region) |

Get AK/SK from the Huawei console (*My Credentials → Access Keys*) and the
project ID from *My Credentials → API Credentials* (pick the one for your
region — each region has its own). The credential needs the
[permissions below](#iam-permissions).

> In FunctionGraph mode siesta does **not** use these — it reads temporary
> credentials from the function's agency. See [Scheduling](#scheduling).

## Commands

### Scale

Scale the **whole pool**:

```bash
siesta --cluster <id> --nodepool prod --count 3 --region ap-southeast-4
```

Scale **one zone** (siesta resolves the AZ to its scale group):

```bash
siesta --cluster <id> --nodepool prod --az ap-southeast-4c --count 1
```

Scale **one zone + machine type** (when a zone has more than one flavor):

```bash
siesta --cluster <id> --nodepool prod \
  --az ap-southeast-4c --flavor s7n.2xlarge.2 --count 2
```

Or target a scale group by its **exact name**:

```bash
siesta --cluster <id> --nodepool prod --scale-group ts7nxlarge2-apsoutheast4c-69556l --count 0
```

Useful flags: `--dry-run` (preview, no change), `--json` (machine-readable
output), `--scale-policy random|azbalance` (placement when a scale-up spans
multiple groups; default `random`). Run `siesta --help` for everything.

**Selector precedence:** `--scale-group` (exact name) wins; otherwise
`--az` (+ `--flavor` to disambiguate); if none are set, the whole pool is scaled.

### describe

List a pool's scale groups (read-only):

```bash
siesta describe --cluster <id> --nodepool prod --region ap-southeast-4
# add --json for machine-readable output
```

The `AZ` and `FLAVOR` columns are exactly what you pass to `--az` / `--flavor`;
the `GROUP` column is what you pass to `--scale-group`.

## Configuration reference

Every flag has an environment-variable equivalent (flags win). The node pool and
target can also come from the trigger payload in FunctionGraph mode.

| Flag | Env var | `user_event` (FG) | Meaning |
|---|---|---|---|
| `--count` | — | `count` | desired node count (**required**, ≥ 0) |
| `--cluster` | `SIESTA_CLUSTER` | `cluster` | CCE cluster ID |
| `--nodepool` | `SIESTA_NODEPOOL` | `nodePool` | node **pool** name |
| `--nodepool-id` | `SIESTA_NODEPOOL_ID` | `nodePoolId` | node pool ID (skips name lookup) |
| `--az` | — | `az` | target the scale group in this AZ |
| `--flavor` | — | `flavor` | disambiguate `--az` by machine type |
| `--scale-group` | — | `scaleGroup` | target a scale group by exact name |
| `--scale-policy` | — | `scalePolicy` | `random` (default) or `azbalance` |
| `--dry-run` | — | `dryRun` | compute the change, don't apply it |
| `--region` | `SIESTA_REGION` | — | region, e.g. `ap-southeast-4` |
| `--endpoint` | `SIESTA_ENDPOINT` | — | override the CCE endpoint |
| `--provider` | `SIESTA_PROVIDER` | — | provider (default `huawei-cce`) |
| `--json` | — | — | JSON output |
| `--mode` | `SIESTA_MODE` | — | `cli` or `fg` (auto-detected) |

Credentials (env only): `HUAWEICLOUD_SDK_AK`, `HUAWEICLOUD_SDK_SK`,
`HUAWEICLOUD_PROJECT_ID`, `HUAWEICLOUD_SDK_SECURITY_TOKEN` (optional).

Exit codes: `0` success · `1` runtime/scale error · `2` bad arguments.

## Scheduling

siesta runs once and exits, so point any scheduler at it.

### Plain cron

```cron
# times in UTC — adjust for your timezone (example: UTC+7)
0 0  * * *  siesta --cluster <id> --nodepool prod --count 3   # 07:00 local
0 16 * * *  siesta --cluster <id> --nodepool prod --count 0   # 23:00 local
```

### Huawei FunctionGraph

The same binary also runs as a timer-triggered function. siesta auto-detects FG
mode and reads temporary credentials from the function's **agency** — no AK/SK
stored on the function.

**1. Build the package** (or download `siesta_<version>_functiongraph_amd64.zip`
from [releases](https://github.com/rahadiangg/siesta/releases); use `arm64` for
Kunpeng runtimes):

```bash
make package-fg     # -> function.zip (a Linux executable named `handler`)
```

**2. Create the function**

- **Runtime**: `Go1.x`
- **Handler**: `handler` (the console default — must match the executable name)
- **Code**: upload the zip
- **Timeout**: ~60s · **Memory**: 128 MB

**3. Create an agency** (this is how the function gets credentials)

In *IAM → Agencies → Create Agency*:
- **Agency Type**: **Cloud service** (not Account)
- **Cloud Service**: **FunctionGraph**
- Grant it CCE permissions (see [IAM permissions](#iam-permissions)), scoped to
  your cluster's region

Then bind it to the function under *Configuration → Permissions → Agency*.

**4. Set environment variables** on the function:

| Key | Value |
|---|---|
| `SIESTA_REGION` | `ap-southeast-4` |
| `SIESTA_CLUSTER` | your cluster ID |
| `SIESTA_NODEPOOL` | your node **pool** name (not a scale group) |

**5. Add timer triggers.** One function can have many timers. Each trigger's
**"Additional information"** field is the `user_event` — put the desired state
there:

```json
{ "count": 3 }
{ "az": "ap-southeast-4c", "count": 1 }
{ "az": "ap-southeast-4c", "flavor": "s7n.2xlarge.2", "count": 2 }
```

Example schedule (one function, four timers; times in **UTC**):

| Trigger | Cron (UTC) | `user_event` |
|---|---|---|
| morning-az-a | `0 0 * * ?` | `{"az":"ap-southeast-4a","count":2}` |
| morning-az-c | `0 0 * * ?` | `{"az":"ap-southeast-4c","count":1}` |
| night-az-a | `0 16 * * ?` | `{"az":"ap-southeast-4a","count":0}` |
| night-az-b | `0 16 * * ?` | `{"az":"ap-southeast-4b","count":0}` |

**Test it** from the console with a simulated timer event (note `user_event` is a
JSON **string**):

```json
{ "trigger_type": "TIMER", "user_event": "{\"az\":\"ap-southeast-4c\",\"count\":1}" }
```

Check the logs — siesta logs `provider=… nodePool=… previous=… desired=… changed=…`.

## IAM permissions

Attach the system-defined **`CCE FullAccess`**, or this least-privilege custom
policy (classic format, `"Version": "1.1"`):

```json
{
  "Version": "1.1",
  "Statement": [
    { "Effect": "Allow", "Action": [
        "cce:nodepool:list",
        "cce:nodepool:get",
        "cce:nodepool:update",
        "cce:nodepool:scale",
        "cce:cluster:get"
    ] }
  ]
}
```

- `cce:nodepool:update` → whole-pool scaling
- `cce:nodepool:scale` → per-zone/group scaling
- `cce:nodepool:list` → name lookup and `describe`

Node provisioning itself is done by CCE's own service agency, so the caller needs
no ECS/VPC permissions. In FunctionGraph mode, grant these to the function's
**agency** rather than to a user.

## Behavior & caveats

- **Idempotent** — if the target is already at the requested count, siesta makes
  no API call and reports `changed=false`.
- **Fire-and-return** — siesta returns once the API accepts the change; nodes
  provision/drain asynchronously over the next few minutes.
- **Cluster Autoscaler is left untouched.** siesta only sets node counts. If a
  pool/group has the autoscaler enabled with a minimum above your target, it may
  scale back up — keep the autoscaler **off** on anything siesta manages.
- **Scaling to 0 evicts the pods** on those nodes (the point of a nightly
  shutdown).
- **Multi-AZ groups** (shown as `random` in the AZ column) can't be targeted by a
  single `--az`; scale them by the whole pool, by exact `--scale-group`, or split
  them into per-zone groups.
- Scaling **down** a group removes nodes only from that group; other groups are
  untouched.

## Development

```bash
make test     # unit tests with coverage (no cloud needed)
make build    # local binary -> bin/siesta
go vet ./...
```

Contributions welcome. To add a cloud provider, implement the small `Scaler`
interface in `internal/scaler` and register it — see `internal/scaler/huaweicce`
for a reference.

## License

MIT — see [LICENSE](LICENSE).

## Credits

- FunctionGraph Go runtime: [rahadiangg/huaweicloud-go-runtime](https://github.com/rahadiangg/huaweicloud-go-runtime)
- Huawei Cloud SDK: [huaweicloud/huaweicloud-sdk-go-v3](https://github.com/huaweicloud/huaweicloud-sdk-go-v3)
