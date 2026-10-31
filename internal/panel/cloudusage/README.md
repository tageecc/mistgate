# Cloud usage

Optional VPS panel integration. The owner connects a cloud account in Settings → Cloud accounts, then associates
an instance in Node → Server → Cloud resource. Alibaba Cloud ECS fixed public IP and CDT internet traffic are
supported, along with AWS Lightsail instance traffic and the current plan allocation. EIP and other products
require their own monitoring adapters.

The integration reads cloud APIs only. It never updates node desired state, user quotas or cloud resources.
Reminder thresholds are local display settings. They do not stop a node or represent a vendor's free allowance.

## Access

Enter one AccessKey ID and Secret in **Settings → Cloud accounts**, then associate the instance in
**Node → Server → Cloud resource**. Traffic collection starts after binding; bill collection starts when enabled.

The key needs `ecs:DescribeInstances` and `ecs:DescribeInstanceMonitorData` on the selected ECS resources.
Reading bills also needs `bssapi:DescribeInstanceBill` with `bssapi:ProductCode=cdt`; this permission is
account/product-scoped. STS GetCallerIdentity pins the root account ID when connecting, so reconnecting cannot
silently switch resource ownership.

For AWS Lightsail, select **AWS Lightsail** and enter one AccessKey ID and Secret. The key needs
`lightsail:GetInstance` and `lightsail:GetInstanceMetricData`. These two AWS actions require `Resource: "*"`;
restrict them to the target region with the `aws:RequestedRegion` condition. STS
GetCallerIdentity verifies the AWS account ID. Bind by region and **instance name**, rather than an EC2 ID.
No write, Cost Explorer or billing permissions are used.

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
- Lightsail uses UTC calendar months. Read `NetworkIn` and `NetworkOut` with `Sum`, `Bytes` and 300-second
  periods. AWS timestamps start an interval. Store a ten-minute pair only when both five-minute observations
  exist in both directions; a missing direction remains a gap. The card and reminder threshold use in + out.
  Backfill the last 30 days and re-read 48 hours; these are collection windows, not a claim of API retention.
- `GetInstance.networking.monthlyTransfer.gbPerMonthAllocated` supplies the plan's monthly allowance in AWS GB.
  It is not remaining allowance: Lightsail pools allowance across the same bundle and region, and billable
  excess depends on traffic destination/direction. Do not derive an invoice or remaining quota from these
  monitoring counters. The AWS card shows the plan allocation instead of a billed amount; paid billing API
  polling and Lightsail for Research's forecast API are not used.

The fork's tables and migration journal use the `cloud_usage_` namespace in the existing database. Upstream core
migrations remain unchanged. Existing database snapshots include the extension; restoring also requires the
same master key. A newer extension schema disables this module while leaving the core panel available.

References: [ECS monitoring](https://help.aliyun.com/zh/ecs/developer-reference/api-ecs-2014-05-26-describeinstancemonitordata),
[instance bills](https://help.aliyun.com/zh/user-center/developer-reference/api-bssopenapi-2017-12-14-describeinstancebill),
[CDT bill formats](https://help.aliyun.com/zh/cdt/query-bills),
[Lightsail metrics](https://docs.aws.amazon.com/lightsail/2016-11-28/api-reference/API_GetInstanceMetricData.html),
[Lightsail allocation](https://docs.aws.amazon.com/lightsail/2016-11-28/api-reference/API_GetInstance.html),
[Lightsail allowance rules](https://docs.aws.amazon.com/lightsail/latest/userguide/amazon-lightsail-faq-data-transfer-allowance.html).
