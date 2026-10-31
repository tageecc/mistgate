import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { ChartCard } from "@/components/chart";
import { Select } from "@/components/ui/select";
import { useT } from "@/i18n";
import { cloudMonths, cloudMoney, cloudUsageQuery, type CloudUsage } from "@/lib/cloud-usage";
import { useEdition } from "@/lib/edition";
import { scaleBytes, useFmt } from "@/lib/format";
import { niceCeil } from "@/lib/series";
import { useIsOwner } from "@/lib/updates";

export function NodeCloudUsage({ nodeId }: { nodeId: string }) {
  const owner = useIsOwner();
  const unavailable = useEdition().off("cloud_usage");
  const [month, setMonth] = useState("");
  const q = useQuery({ ...cloudUsageQuery(nodeId, month), enabled: owner && !unavailable,
    placeholderData: (previous) => previous ? { ...previous, month, days: [], hasUsage: false, hasCost: false, partial: false, observedThroughUnix: 0, billedThroughUnix: 0, thresholdReached: false } : undefined,
  });
  if (!owner || unavailable || !q.data?.resource) return null;
  return <CloudUsageCard data={q.data} month={month} onMonthChange={setMonth} />;
}

/** Shared by the live screen and the development fixtures; no synthetic data in production. */
export function CloudUsageCard({ data, month = "", onMonthChange }: { data: CloudUsage; month?: string; onMonthChange: (month: string) => void }) {
  const t = useT();
  const fmt = useFmt();
  const days = data.days;
  const lightsail = data.provider === "aws_lightsail";
  const traffic = (d: { outboundBytes: number; inboundBytes: number }) => d.outboundBytes + (lightsail ? d.inboundBytes : 0);
  const peak = Math.max(1, ...days.map(traffic));
  const unit = scaleBytes(peak).unit;
  const divisor = 1000 ** unit;
  const step = niceCeil(peak / divisor / 2);
  const value = data.hasUsage ? fmt.bytes(traffic(data)) : "—";
  const monthOptions = cloudMonths(data.provider).map((v) => ({ value: v, label: v }));
  const label = `${t(lightsail ? "cloud.total" : "cloud.outbound")}${data.partial ? " · " + t("cloud.partial") : ""}`;
  return (
    <ChartCard
      title={t("cloud.title")}
      icon="traffic" iconTone="sky"
      control={<div className="w-[120px]" title={lightsail ? "UTC" : undefined}><Select compact aria-label={t("cloud.month")} value={month || data.month} onValueChange={onMonthChange} options={monthOptions} /></div>}
      summary={<div className="flex flex-col gap-0.5" title={lightsail ? t("cloud.allowanceHint") : data.billedThroughUnix > 0 ? t("cloud.billUpdated", { time: fmt.stamp(data.billedThroughUnix) }) : undefined}><span className="text-xs text-muted">{t(lightsail ? "cloud.allowance" : "cloud.billed")}</span><span className="font-mono text-xl leading-[1.1] font-bold tracking-[-0.04em]">{lightsail ? (data.allowanceGb > 0 ? `${fmt.num(data.allowanceGb)} GB` : "—") : data.hasCost ? cloudMoney(data.billedAmount, data.currency, fmt.locale) : "—"}</span></div>}
      lines={[{ key: "traffic", values: days.map((d) => traffic(d) / divisor), defined: days.map((d) => d.hasUsage && !d.partial), tone: "accent", width: 2 }]}
      max={step * 2} height={110}
      yLabels={[`${fmt.num(step * 2, 1)} ${t(`unit.${(["B", "KB", "MB", "GB", "TB"] as const)[unit]}`)}`, fmt.num(step, 1), "0"]}
      xLabels={days.length ? [days[0]!.date.slice(5).replace("-", "/"), days.at(-1)!.date.slice(5).replace("-", "/")] : []}
      idle={{ when: label, main: value }}
      at={(i) => ({ when: days[i]!.date, main: days[i]!.hasUsage ? `${fmt.bytes(traffic(days[i]!))}${days[i]!.partial ? " · " + t("cloud.partial") : ""}` : "—" })}
      legend={() => []} label={`${t("cloud.title")} · ${value}`}
      footer={<div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted">
        {lightsail && data.hasUsage && <span>{t("cloud.directions", { received: fmt.bytes(data.inboundBytes), sent: fmt.bytes(data.outboundBytes) })}</span>}

        {data.observedThroughUnix > 0 && <span>{t("cloud.through", { time: fmt.stamp(data.observedThroughUnix) })}</span>}
        {data.thresholdReached && <span className="font-semibold text-warn-text">{t("cloud.reached")}</span>}
      </div>}
    />
  );
}
