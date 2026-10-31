# Cloud usage

Optional VPS panel integration. The owner connects a cloud account in Settings → Cloud accounts, then associates
an instance in Node → Server → Cloud resource. Alibaba Cloud ECS fixed public IP and CDT internet traffic are
supported. EIP and other providers require their own monitoring adapters.

The integration reads cloud APIs only. It never updates node desired state, user quotas or cloud resources.
Reminder thresholds are local display settings. They do not stop a node or represent a vendor's free allowance.

## Access

Use a dedicated RAM **user**, for example `mistgate-usage`, with programmatic access. An AccessKey inherits its
user's effective permissions: another key for an existing operations user is not a separate read-only identity.

Attach [the read-only policy](policies/aliyun-readonly.json) to this dedicated user, replacing `ACCOUNT_ID`,
`REGION_ID` and `INSTANCE_ID`. Add explicit ECS resource ARNs for additional instances. The policy grants
`ecs:DescribeInstances` and `ecs:DescribeInstanceMonitorData` on those resources, plus
`bssapi:DescribeInstanceBill` restricted to `bssapi:ProductCode=cdt`. Bill access is account/product-scoped, not an
instance-only permission. Remove the bill statement and leave **Read bills** off if that scope is not approved.
STS GetCallerIdentity pins the root account ID when connecting; reconnecting cannot silently switch ownership.

The account administrator can create the key, or attach [the key-creator policy](policies/aliyun-key-creator.json)
to a human operator's RAM user. Replace `ACCOUNT_ID` and, if different, the dedicated user's name. This delegates
only key creation and listing for that user, not user creation or permission management. The operator can call
[CreateAccessKey](https://help.aliyun.com/zh/ram/developer-reference/api-ims-2019-08-15-createaccesskey)
in OpenAPI Explorer with the dedicated user's full `UserPrincipalName`; broad `ram:ListUsers` access is not required
by that API. If the operator also maintains rotation, the administrator can add `ram:UpdateAccessKey` and
`ram:DeleteAccessKey` on the same user. Do not attach these key-management permissions to the collector itself.

The owner enters the resulting key in **Settings → Cloud accounts**, then associates the instance in
**Node → Server → Cloud resource**. Traffic collection starts after binding; bill collection starts when enabled.
The panel does not need ECS write permissions, RAM administration or a cloud monitoring agent on the node.

See Alibaba's [AccessKey permission guidance](https://help.aliyun.com/zh/ram/user-guide/create-an-accesskey-pair)
and [per-user self-management example](https://help.aliyun.com/zh/ram/authorize-a-ram-user-to-manage-accesskey-pairs).

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
