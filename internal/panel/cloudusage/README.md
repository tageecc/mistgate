# Cloud usage

Optional VPS panel integration. The owner connects a cloud account in Settings → Cloud accounts, then associates
an instance in Node → Server → Cloud resource. Alibaba Cloud ECS fixed public IP and CDT internet traffic are
supported. EIP and other providers require their own monitoring adapters.

The integration reads cloud APIs only. It never updates node desired state, user quotas or cloud resources.
Reminder thresholds are local display settings. They do not stop a node or represent a vendor's free allowance.

## Access

Use a dedicated RAM AccessKey. Grant `ecs:DescribeInstances` and `ecs:DescribeInstanceMonitorData` on the selected
ECS resources. If bill collection is enabled, also grant `bssapi:DescribeInstanceBill` with `bssapi:ProductCode=cdt`.
Bill access is account/product-scoped, not an instance-only permission. STS GetCallerIdentity pins the root account
ID when connecting; reconnecting cannot silently switch resource ownership.

Credentials are write-only, sealed by the panel vault with account-specific associated data. All RPCs require an
owner session, writes require step-up, and API tokens/MCP have no access. Disconnect cancels collection, clears
the stored credentials and increments their revision. In-flight responses cannot commit under an old revision.

## Measurements

- ECS InternetTX is the total kbits sent during each 600-second interval: multiply by 125 to store decimal bytes.
  The timestamp closes the interval. Fetch at most 399 intervals per request, backfill within the API's 30-day
  retention, and re-read the preceding 48 hours for delayed or corrected samples. Upsert by resource and interval.
- Calendar months and days use UTC+8, matching Alibaba billing. Missing samples remain gaps; a real zero is kept.
  Never estimate a complete month from a partial window. Raw samples are retained for one year.
- Monthly CDT bills are refreshed every six hours, for this month and the preceding month. Every page must
  succeed before replacing a snapshot. Match ECS instance identity, sum PretaxAmount using exact decimals, and
  preserve the currency. Unknown ECS billing layouts fail closed; they are not silently omitted from the sum.
- A bill is an already-billed amount, not a real-time estimate. Its refresh time is separate from the latest
  observed traffic interval. Missing bills are not zero. Free allowance usage is not inferred from node counters:
  the allowance is shared by the entire root account and needs a separate verified data source.

The fork's tables and migration journal use the `cloud_usage_` namespace in the existing database. Upstream core
migrations remain unchanged. Existing database snapshots include the extension; restoring also requires the
same master key. A newer extension schema disables this module while leaving the core panel available.

References: [ECS monitoring](https://help.aliyun.com/zh/ecs/developer-reference/api-ecs-2014-05-26-describeinstancemonitordata),
[instance bills](https://help.aliyun.com/zh/user-center/developer-reference/api-bssopenapi-2017-12-14-describeinstancebill),
[CDT bill formats](https://help.aliyun.com/zh/cdt/query-bills).
