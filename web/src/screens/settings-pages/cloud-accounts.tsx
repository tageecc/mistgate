import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Switch } from "@/components/ui/switch";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import type { CloudAccount } from "@/gen/mistgate/admin/v1/cloud_usage_pb";
import { useT } from "@/i18n";
import { cloudUsage } from "@/lib/api";
import { cloudAccountsQuery, cloudError } from "@/lib/cloud-usage";
import { useEdition } from "@/lib/edition";
import type { Plain } from "@/lib/plain";
import { useIsOwner } from "@/lib/updates";

export function CloudAccountsPage() {
  const t = useT();
  const owner = useIsOwner();
  const unavailable = useEdition().off("cloud_usage");
  const q = useQuery({ ...cloudAccountsQuery(), enabled: owner && !unavailable });
  const [editing, setEditing] = useState<Plain<CloudAccount> | "new" | null>(null);
  if (!owner || unavailable) return null;
  return (
    <section className="overflow-hidden rounded-card-lg border border-line bg-surface">
      <div className="flex items-center justify-between gap-3 p-4">
        <SectionLabel as="h2" icon="server" tone="sky">{t("cloud.accounts")}</SectionLabel>
        <Button size="sm" onClick={() => setEditing("new")}>{t("cloud.add")}</Button>
      </div>
      <div className="divide-y divide-line border-t border-line">
        {q.data?.map((a) => (
          <div key={a.id} className="flex items-center gap-3 px-4 py-3.5">
            <div className="flex min-w-0 flex-1 flex-col gap-1">
              <b className="break-words text-sm">{a.name}</b>
              <span className="text-xs text-muted">{t("cloud.aliyun")} · <span className="font-mono">{a.accountId}</span></span>
              {!a.connected && <span className="text-xs text-muted">{t("cloud.disconnected")}</span>}
            </div>
            <Button size="sm" variant="ghost" onClick={() => setEditing(a)}>{t(a.reconnect || !a.connected ? "cloud.reconnect" : "cloud.edit")}</Button>
          </div>
        ))}
        {(!q.data || q.data.length === 0) && <p className="px-4 py-6 text-center text-[13px] text-muted">{q.data ? t("cloud.empty") : "—"}</p>}
      </div>
      {editing && <CloudAccountForm account={editing === "new" ? undefined : editing} onClose={() => setEditing(null)} />}
    </section>
  );
}

function CloudAccountForm({ account, onClose }: { account?: Plain<CloudAccount>; onClose: () => void }) {
  const t = useT();
  const guard = useStepUp();
  const qc = useQueryClient();
  const toast = useToast();
  const [name, setName] = useState(account?.name ?? "");
  const [keyId, setKeyId] = useState("");
  const [secret, setSecret] = useState("");
  const [billing, setBilling] = useState(account?.billingEnabled ?? false);
  const [confirm, setConfirm] = useState(false);
  const [error, setError] = useState("");
  const saved = () => { void qc.invalidateQueries({ queryKey: ["cloud-accounts"] }); void qc.invalidateQueries({ queryKey: ["cloud-usage"] }); toast(t("cloud.saved")); onClose(); };
  const failed = (e: unknown) => { if (!isStepUpCancelled(e)) setError(cloudError(e, t)); };
  const save = useMutation({
    mutationFn: () => guard(() => cloudUsage.saveCloudAccount({ id: account?.id ?? "", name: name.trim(), provider: "aliyun", accessKeyId: keyId.trim(), accessKeySecret: secret.trim(), billingEnabled: billing })),
    onSuccess: saved, onError: failed,
  });
  const disconnect = useMutation({
    mutationFn: () => guard(() => cloudUsage.disconnectCloudAccount({ id: account!.id })),
    onSuccess: saved, onError: failed,
  });
  const pending = save.isPending || disconnect.isPending;
  const needKey = !account?.connected || account.reconnect;
  function submit(e: FormEvent) { e.preventDefault(); setError(""); save.mutate(); }
  return (
    <Modal open onOpenChange={(v) => { if (!v && !pending) onClose(); }} title={t(account ? "cloud.edit" : "cloud.add")} closeLabel={t("cloud.cancel")}>
      <form onSubmit={submit} className="flex flex-col gap-4">
        <TextField label={t("cloud.name")} value={name} onChange={(e) => setName(e.target.value)} required maxLength={120} />
        <div className="flex items-center justify-between text-[13px]"><span className="text-muted">{t("cloud.provider")}</span><span>{t("cloud.aliyun")}</span></div>
        <TextField label={t("cloud.keyId")} value={keyId} onChange={(e) => setKeyId(e.target.value)} required={needKey || !!secret} placeholder={account?.connected ? "••••••••" : ""} autoComplete="off" maxLength={256} mono />
        <TextField label={t("cloud.keySecret")} type="password" value={secret} onChange={(e) => setSecret(e.target.value)} required={needKey || !!keyId} placeholder={account?.connected ? "••••••••" : ""} autoComplete="new-password" maxLength={256} />
        <label className="flex items-center justify-between gap-3 text-[13px]"><span>{t("cloud.billing")}</span><Switch checked={billing} onCheckedChange={setBilling} /></label>
        {error && <p role="alert" className="text-xs text-danger-text">{error}</p>}
        <div className="flex flex-wrap items-center gap-2 border-t border-line pt-3">
          {account?.connected && <Button type="button" variant="ghostDanger" size="sm" disabled={pending} onClick={() => setConfirm(true)}>{t("cloud.disconnect")}</Button>}
          <Button className="ml-auto" type="submit" variant="primary" disabled={pending || !name.trim()}>{t(pending ? "cloud.saving" : "cloud.save")}</Button>
        </div>
      </form>
      <Modal open={confirm} onOpenChange={setConfirm} title={t("cloud.disconnectConfirm", { name: account?.name ?? "" })} description={t("cloud.disconnectHint")} footer={<><Button onClick={() => setConfirm(false)}>{t("cloud.cancel")}</Button><Button variant="danger" disabled={pending} onClick={() => disconnect.mutate()}>{t("cloud.disconnect")}</Button></>} />
    </Modal>
  );
}
