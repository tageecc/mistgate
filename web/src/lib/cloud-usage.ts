import { ConnectError } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import type { GetCloudUsageResponse } from "@/gen/mistgate/admin/v1/cloud_usage_pb";
import type { T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cloudUsage } from "./api";
import { plain, type Plain } from "./plain";

export type CloudUsage = Plain<GetCloudUsageResponse>;
export const cloudAccountsQuery = () => queryOptions({
  queryKey: ["cloud-accounts"],
  queryFn: async ({ signal }) => (await cloudUsage.listCloudAccounts({}, { signal })).accounts.map(plain),
  staleTime: 60_000,
});
export const cloudUsageQuery = (nodeId: string, month = "") => queryOptions({
  queryKey: ["cloud-usage", nodeId, month],
  queryFn: async ({ signal }) => plain(await cloudUsage.getCloudUsage({ nodeId, month }, { signal })),
  staleTime: 60_000,
  refetchInterval: 60_000,
});

// Background reads stay quiet. These short messages appear only beside an explicit edit.
export function cloudError(error: unknown, t: T) {
  const keys: Record<string, MessageKey> = {
    cloud_credentials: "cloud.credentials", cloud_permission: "cloud.permission", cloud_invalid: "cloud.invalid",
    cloud_exists: "cloud.exists", cloud_resource_bound: "cloud.bound",
  };
  return t(keys[ConnectError.from(error).rawMessage] ?? "cloud.failed");
}

export function cloudMonths(provider = "aliyun", now = new Date()) {
  const parts = new Intl.DateTimeFormat("en-CA", { timeZone: provider === "aws_lightsail" ? "UTC" : "Asia/Shanghai", year: "numeric", month: "2-digit" }).formatToParts(now);
  const year = Number(parts.find((p) => p.type === "year")!.value);
  const month = Number(parts.find((p) => p.type === "month")!.value);
  return Array.from({ length: 12 }, (_, i) => new Date(Date.UTC(year, month - 1 - i, 1)).toISOString().slice(0, 7));
}

export function cloudMoney(amount: string, currency: string, locale: string) {
  const value = Number(amount);
  if (!Number.isFinite(value) || !/^[A-Z]{3}$/.test(currency)) return "—";
  return new Intl.NumberFormat(locale, { style: "currency", currency, maximumFractionDigits: 2 }).format(value);
}
