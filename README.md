# siesta

**Scheduled scale-down for your Kubernetes node pools.** Run your cluster at full
size during the day, shrink it to (almost) nothing at night — and stop paying for
idle nodes.

Siesta is a tiny Go tool that sets a node pool (or a single availability zone
within it) to an exact node count. It does **not** run a scheduler itself — you
trigger it from whatever cron you already have (a Huawei FunctionGraph timer, AWS
EventBridge, a Kubernetes CronJob, plain `crontab`…). Each run just applies the
size you ask for.

Today it supports **Huawei Cloud CCE**. The internals are provider-agnostic, so
other clouds (AWS EKS, DigitalOcean, …) can be added later.

```bash
# 3 nodes now
siesta --cluster <id> --nodepool prod --count 3 --region ap-southeast-4

# 0 nodes (e.g. overnight)
siesta --cluster <id> --nodepool prod --count 0 --region ap-southeast-4
```

It's **idempotent** (no change if already at the target), supports **`--dry-run`**,
and exits non-zero on failure so your cron notices.

---

## Install

Download a binary from the [releases page](https://github.com/rahadiangg/siesta/releases)
(Linux / macOS / Windows, amd64 / arm64), or build from source:

```bash
go install github.com/rahadiangg/siesta/cmd/siesta@latest
# or
git clone https://github.com/rahadiangg/siesta && cd siesta && make build
```

## Credentials

Set these in the environment:

| Variable | What |
|---|---|
| `HUAWEICLOUD_SDK_AK` | access key |
| `HUAWEICLOUD_SDK_SK` | secret key |
| `HUAWEICLOUD_PROJECT_ID` | project ID (per region) |

Get AK/SK from the Huawei console (*My Credentials → Access Keys*) and the project
ID from *My Credentials → API Credentials*. The IAM user needs the
[permissions below](#iam-permissions).

## Usage

```bash
# preview a change without applying it
siesta --cluster <id> --nodepool prod --count 0 --dry-run

# JSON output (for scripting)
siesta --cluster <id> --nodepool prod --count 3 --json

# everything
siesta --help
```

Flags can also come from the environment: `SIESTA_REGION`, `SIESTA_CLUSTER`,
`SIESTA_NODEPOOL`.

### Scaling one availability zone

A CCE node pool can span several AZs, each its own *scale group*. If a workload is
pinned to a zone — e.g. it mounts a **zonal disk (EVS/PV)** and can only run where
that disk lives — scale that zone directly instead of the whole pool.

List the groups:

```bash
siesta describe --cluster <id> --nodepool prod --region ap-southeast-4
```
```
node pool prod (np-xxxx) — total 3
GROUP    AZ               FLAVOR        DESIRED  EXISTING  AUTOSCALING
default  ap-southeast-4a  s7n.xlarge.2  2        2         false
g-az3    ap-southeast-4c  s7n.xlarge.2  1        1         false
```

Then target a zone (siesta figures out the group for you):

```bash
# keep exactly 1 node in AZ-c; other zones untouched
siesta --cluster <id> --nodepool prod --az ap-southeast-4c --count 1

# scale that zone to 0
siesta --cluster <id> --nodepool prod --az ap-southeast-4c --count 0
```

Use `--flavor` to disambiguate if a zone has more than one machine type, or
`--scale-group <name>` to target by exact name.

## Scheduling

Siesta runs once and exits, so point any scheduler at it.

**Plain cron** (UTC; adjust for your timezone):
```cron
0 0  * * *  siesta --cluster <id> --nodepool prod --count 3   # 07:00 UTC+7
0 16 * * *  siesta --cluster <id> --nodepool prod --count 0   # 23:00 UTC+7
```

**Huawei FunctionGraph** (runs the same binary as a timer-triggered function):

Grab the ready-made `siesta_<version>_functiongraph_amd64.zip` from the
[releases page](https://github.com/rahadiangg/siesta/releases) (use `arm64` for
Kunpeng runtimes), or build it yourself:

```bash
make package-fg          # builds function.zip (a Linux bootstrap binary)
```

Upload the zip (Go runtime), give the function an **agency** with the CCE
permissions below (no AK/SK needed — siesta reads temporary credentials from the
agency), set `SIESTA_REGION` / `SIESTA_CLUSTER` / `SIESTA_NODEPOOL`, and add timer
triggers. Each trigger's **`user_event`** carries the desired state:

```json
{ "count": 3 }
{ "az": "ap-southeast-4c", "count": 1 }
```

## IAM permissions

Attach the system-defined **`CCE FullAccess`**, or this least-privilege custom
policy:

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

`cce:nodepool:update` is used for whole-pool scaling, `cce:nodepool:scale` for
per-zone scaling. Node provisioning is done by CCE's own service agency, so no
ECS/VPC permissions are needed on the caller.

## Good to know

- **Cluster Autoscaler is left untouched.** Siesta only sets node counts. If a
  pool has the autoscaler enabled with a minimum above your target, it may scale
  back up — keep the autoscaler off on pools you manage with siesta.
- **Scaling to 0 evicts the pods** on those nodes (the point of a nightly shutdown).
- **Multi-AZ default groups** (shown as `random` in `describe`) can't be targeted
  by a single `--az`; scale them by the whole pool or split them per zone.

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
