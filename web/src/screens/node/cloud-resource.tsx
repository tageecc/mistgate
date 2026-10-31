import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import type { CloudAccount, CloudResource } from "@/gen/mistgate/admin/v1/cloud_usage_pb";
import { useT } from "@/i18n";
import { cloudUsage } from "@/lib/api";
import { cloudAccountsQuery, cloudError, cloudUsageQuery } from "@/lib/cloud-usage";
import { useEdition } from "@/lib/edition";
import type { Plain } from "@/lib/plain";
import { useIsOwner } from "@/lib/updates";

export function CloudResourceCard({ nodeId }: { nodeId: string }) {
  const owner = useIsOwner();
  const unavailable = useEdition().off("cloud_usage");
  const a = useQuery({ ...cloudAccountsQuery(), enabled: owner && !unavailable });
  const q = useQuery({ ...cloudUsageQuery(nodeId), enabled: owner && !unavailable, refetchInterval: false });
  if (!owner || unavailable || !a.data || !q.data) return null;
  return <ResourceForm key={`${nodeId}/${q.data.resource?.accountId ?? ""}/${q.data.resource?.instanceId ?? ""}`} nodeId={nodeId} resource={q.data.resource} accounts={a.data} />;
}

function ResourceForm({ nodeId, resource, accounts }: { nodeId: string; resource?: Plain<CloudResource>; accounts: Plain<CloudAccount>[] }) {
  const t = useT();
  const guard = useStepUp();
  const toast = useToast();
  const qc = useQueryClient();
  const [account, setAccount] = useState(resource?.accountId || "none");
  const [region, setRegion] = useState(resource?.region || "");
  const [instance, setInstance] = useState(resource?.instanceId || "");
  const [threshold, setThreshold] = useState(resource?.thresholdBytes ? String(resource.thresholdBytes / 1e9) : "");
  const [amount, setAmount] = useState(resource?.thresholdAmount || "");
  const [currency, setCurrency] = useState(resource?.thresholdCurrency || "CNY");
  const [error, setError] = useState("");
  const lightsail = accounts.find((a) => a.id === account)?.provider === "aws_lightsail";
  const mutation = useMutation({
    mutationFn: () => guard(() => cloudUsage.bindCloudResource({ nodeId, resource: account === "none" ? undefined : { accountId: account, region: region.trim(), instanceId: instance.trim(), thresholdBytes: BigInt(Math.round(Number(threshold || 0) * 1e9)), thresholdAmount: lightsail ? "" : amount, thresholdCurrency: lightsail ? "" : currency } })),
    onSuccess: () => { void qc.invalidateQueries({ queryKey: ["cloud-usage", nodeId] }); toast(t("cloud.saved")); },
    onError: (e) => { if (!isStepUpCancelled(e)) setError(cloudError(e, t)); },
  });
  function submit(e: FormEvent) { e.preventDefault(); setError(""); mutation.mutate(); }
  const changed = account !== (resource?.accountId || "none") || region.trim() !== (resource?.region || "") || instance.trim() !== (resource?.instanceId || "") || Number(threshold || 0) * 1e9 !== (resource?.thresholdBytes || 0) || amount !== (resource?.thresholdAmount || "") || currency !== (resource?.thresholdCurrency || "CNY");
  return (
    <form onSubmit={submit} className="flex flex-col gap-4 rounded-card-lg border border-line bg-surface p-4">
      <SectionLabel as="h2" icon="server" tone="sky">{t("cloud.resource")}</SectionLabel>
      <Select aria-label={t("cloud.account")} value={account} onValueChange={setAccount} options={[{ value: "none", label: t("cloud.none") }, ...accounts.map((a) => ({ value: a.id, label: a.name, disabled: !a.connected && a.id !== resource?.accountId }))]} />
      {account !== "none" && <>
        <div className="grid gap-3 md:grid-cols-2">
          <TextField label={t("cloud.region")} value={region} onChange={(e) => setRegion(e.target.value)} placeholder="ap-southeast-1" required mono />
          <TextField label={t(lightsail ? "cloud.instanceName" : "cloud.instance")} value={instance} onChange={(e) => setInstance(e.target.value)} placeholder={lightsail ? "my-instance" : "i-…"} required mono />
        </div>
        <details className="border-t border-line pt-3">
          <summary className="cursor-pointer text-xs font-semibold text-muted">{t("cloud.threshold")}</summary>
          <div className="mt-3 flex flex-col gap-3">
            <TextField label={t(lightsail ? "cloud.thresholdTotal" : "cloud.thresholdTraffic")} type="number" min={0} max={1000000} step="any" value={threshold} onChange={(e) => setThreshold(e.target.value)} placeholder={t("cloud.off")} />
            {!lightsail && <div className="grid grid-cols-[minmax(0,1fr)_100px] items-end gap-3">
              <TextField label={t("cloud.thresholdCost")} type="number" min={0} step="0.01" value={amount} onChange={(e) => setAmount(e.target.value)} placeholder={t("cloud.off")} />
              <Select aria-label={t("cloud.currency")} value={currency} onValueChange={setCurrency} options={["CNY", "USD", "JPY"].map((v) => ({ value: v, label: v }))} />
            </div>}
          </div>
        </details>
      </>}
      {error && <p role="alert" className="text-xs text-danger-text">{error}</p>}
      <div className="flex items-center justify-between gap-3 border-t border-line pt-3">
        <Link to="/settings/$section" params={{ section: "cloud" }} className="text-xs font-semibold text-muted hover:text-fg">{t("cloud.accounts")}</Link>
        <Button type="submit" variant="primary" disabled={!changed || mutation.isPending}>{t(mutation.isPending ? "cloud.saving" : "cloud.save")}</Button>
      </div>
    </form>
  );
}
