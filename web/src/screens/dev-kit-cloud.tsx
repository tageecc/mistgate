import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import { useState } from "react";
import { ThemeToggle, LangToggle } from "@/components/prefs";
import { AccentPicker } from "@/components/accent-picker";
import { Button } from "@/components/ui/button";
import { MeResponseSchema, Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { cloudAccountsQuery, cloudMonths, cloudUsageQuery, type CloudUsage } from "@/lib/cloud-usage";
import { meQuery } from "@/lib/session";
import { CloudUsageCard } from "./node/cloud-usage";
import { CloudResourceCard } from "./node/cloud-resource";
import { CloudAccountsPage } from "./settings-pages/cloud-accounts";

// Development-only fixtures. No cloud credentials, live resources or synthetic production records.
export function CloudUsageKit() {
  const [month, setMonth] = useState(cloudMonths()[0]!);
  const [state, setState] = useState("normal");
  const [narrow, setNarrow] = useState(false);
  const data: CloudUsage = {
    accountName: "Personal", month, hasUsage: state !== "empty", outboundBytes: state === "zero" ? 0 : 126.4e9,
    days: Array.from({ length: 21 }, (_, i) => ({ date: `${month}-${String(i + 1).padStart(2, "0")}`, outboundBytes: state === "zero" ? 0 : 2e9 + ((i * 7) % 11) * 6e8, hasUsage: state !== "empty" && !(state === "partial" && i > 8 && i < 12), partial: false })),
    observedThroughUnix: state === "empty" ? 0 : Date.parse(`${month}-21T08:00:00+08:00`) / 1000, observedFromUnix: 0,
    partial: state === "partial", hasCost: state !== "empty", billedAmount: "0", currency: "CNY", billedThroughUnix: state === "empty" ? 0 : Date.parse(`${month}-21T00:00:00+08:00`) / 1000,
    thresholdReached: state === "threshold", resource: { accountId: "cld_demo", region: "ap-southeast-1", instanceId: "i-example123", thresholdBytes: 200e9, thresholdAmount: "", thresholdCurrency: "CNY" },
  };
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } });
    c.setQueryData(meQuery.queryKey, create(MeResponseSchema, { admin: { role: Role.OWNER }, edition: "vps", unavailable: [] }));
    c.setQueryData(cloudAccountsQuery().queryKey, [{ id: "cld_demo", name: "Personal", provider: "aliyun", accountId: "1234567890123456", connected: true, billingEnabled: true, reconnect: false }]);
    c.setQueryData(cloudUsageQuery("nod_demo").queryKey, data);
    return c;
  });
  return <QueryClientProvider client={qc}>
    <div className="flex flex-wrap items-center gap-2"><LangToggle /><ThemeToggle /><AccentPicker /><Button onClick={() => setNarrow(!narrow)}>{narrow ? "Desktop" : "390 px"}</Button></div>
    <div className="flex flex-wrap gap-2">{["normal", "zero", "partial", "threshold", "empty"].map((v) => <Button key={v} onClick={() => setState(v)} variant={state === v ? "primary" : "ghost"}>{v}</Button>)}</div>
    <div className="mx-auto flex w-full flex-col gap-4" style={{ maxWidth: narrow ? 390 : 960 }}>
      <CloudUsageCard data={data} month={month} onMonthChange={setMonth} />
      <CloudAccountsPage />
      <CloudResourceCard nodeId="nod_demo" />
    </div>
  </QueryClientProvider>;
}
