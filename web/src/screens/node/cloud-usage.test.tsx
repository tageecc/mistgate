import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { chartRuns } from "@/components/chart";
import { ToastProvider } from "@/components/ui/toast";
import { en } from "@/i18n/en";
import { cloudAccountsQuery, cloudMonths, cloudMoney, cloudUsageQuery, type CloudUsage } from "@/lib/cloud-usage";
import { CloudAccountsPage } from "../settings-pages/cloud-accounts";
import { CloudResourceCard } from "./cloud-resource";
import { CloudUsageCard, NodeCloudUsage } from "./cloud-usage";

const api = vi.hoisted(() => ({ listCloudAccounts: vi.fn(), getCloudUsage: vi.fn(), saveCloudAccount: vi.fn(), disconnectCloudAccount: vi.fn(), bindCloudResource: vi.fn() }));
vi.mock("@/lib/api", async (original) => ({ ...(await original<object>()), cloudUsage: api }));
vi.mock("@tanstack/react-router", () => ({ Link: ({ children }: { children: ReactNode }) => <a>{children}</a> }));
let owner = true;
let edge = false;
vi.mock("@/lib/updates", () => ({ useIsOwner: () => owner }));
vi.mock("@/lib/edition", () => ({ useEdition: () => ({ off: () => edge }) }));

const account = { id: "cld_example", name: "Personal", provider: "aliyun", accountId: "1234567890123456", connected: true, billingEnabled: true, reconnect: false };
function data(over: Partial<CloudUsage> = {}): CloudUsage { return {
  provider: "aliyun", inboundBytes: 0, allowanceGb: 0, accountName: "Personal", month: cloudMonths()[0]!, hasUsage: true, outboundBytes: 0, days: [{ date: "2026-10-01", outboundBytes: 0, hasUsage: true, partial: false, inboundBytes: 0 }], observedThroughUnix: 1790784600, observedFromUnix: 1790784000,
  partial: false, hasCost: true, billedAmount: "0", currency: "CNY", thresholdReached: false, billedThroughUnix: 1790784600,
  resource: { accountId: account.id, region: "ap-southeast-1", instanceId: "i-example123", thresholdBytes: 0, thresholdAmount: "", thresholdCurrency: "CNY" }, ...over,
}; }

let host: HTMLDivElement;
let root: Root;
let qc: QueryClient;
beforeAll(() => { (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true; });
beforeEach(() => {
  owner = true; edge = false; vi.resetAllMocks();
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  api.getCloudUsage.mockResolvedValue(data()); api.listCloudAccounts.mockResolvedValue({ accounts: [account] }); api.bindCloudResource.mockResolvedValue({}); api.saveCloudAccount.mockResolvedValue({ account });
});
afterEach(() => { act(() => root.unmount()); qc.clear(); host.remove(); });
const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });
async function mount(children: ReactNode) { await act(async () => { root.render(<QueryClientProvider client={qc}><ToastProvider>{children}</ToastProvider></QueryClientProvider>); }); for (let i=0;i<3;i++) await settle(); }
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label)!;
async function click(el: Element) { await act(async () => { el.dispatchEvent(new MouseEvent("click", { bubbles: true })); }); await settle(); }
async function type(el: HTMLInputElement, value: string) { await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value); el.dispatchEvent(new Event("input", { bubbles: true })); }); }

describe("cloud usage", () => {
  it("splits gaps without zero fill or connecting across them", () => { expect(chartRuns(7, [true,true,false,true,false,false,true])).toEqual([[0,1],[3],[6]]); expect(chartRuns(0)).toEqual([]); expect(chartRuns(3)).toEqual([[0,1,2]]); });
  it("uses the billing month at the UTC+8 boundary", () => { expect(cloudMonths("aliyun", new Date("2026-10-31T16:01:00Z")).slice(0,2)).toEqual(["2026-11","2026-10"]); expect(cloudMoney("0","CNY","en-US")).not.toBe("—"); });
  it("keeps AWS on UTC and shows the plan separately from combined traffic", async () => {
    expect(cloudMonths("aws_lightsail", new Date("2026-10-31T16:01:00Z"))[0]).toBe("2026-10");
    await mount(<CloudUsageCard data={data({ provider: "aws_lightsail", outboundBytes: 3e9, inboundBytes: 7e9, allowanceGb: 2048, hasCost: false })} onMonthChange={() => {}} />);
    expect(host.textContent).toContain("10 GB"); expect(host.textContent).toContain("In 7 GB · Out 3 GB");
    expect(host.textContent).toContain(en["cloud.allowance"]); expect(host.textContent).not.toContain(en["cloud.billed"]);
    expect(host.textContent).not.toContain("remaining");
  });
  it("uses the Lightsail instance name and only a traffic threshold", async () => {
    const aws = { ...account, provider: "aws_lightsail", billingEnabled: false };
    qc.setQueryData(cloudAccountsQuery().queryKey,[aws]); qc.setQueryData(cloudUsageQuery("n").queryKey,data({ provider: "aws_lightsail" }));
    await mount(<CloudResourceCard nodeId="n"/>);
    expect(host.textContent).toContain(en["cloud.instanceName"]); expect(host.textContent).not.toContain(en["cloud.thresholdCost"]);
    const inputs = host.querySelectorAll<HTMLInputElement>('input[type="number"]'); expect(inputs.length).toBe(1);
    await type(inputs[0]!,"1800");
    await act(async () => { host.querySelector("form")!.dispatchEvent(new Event("submit",{bubbles:true,cancelable:true})); }); await settle();
    expect(api.bindCloudResource).toHaveBeenCalledWith(expect.objectContaining({resource:expect.objectContaining({thresholdBytes:1800000000000n,thresholdAmount:"",thresholdCurrency:""})}));
  });
  it("distinguishes known zero from absent values", async () => {
    await mount(<CloudUsageCard data={data()} onMonthChange={() => {}} />);
    expect(host.textContent).toContain("0 B"); expect(host.textContent).toContain(cloudMoney("0","CNY","en-US"));
    await mount(<CloudUsageCard data={data({ hasUsage: false, hasCost: false, days: [] })} onMonthChange={() => {}} />);
    expect(host.textContent).not.toContain("0 B"); expect(host.textContent?.match(/—/g)?.length).toBe(2);
  });
  it("shows a quiet threshold and keeps the monthly cost during day hover", async () => {
    await mount(<CloudUsageCard data={data({ thresholdReached: true })} onMonthChange={() => {}} />);
    expect(host.textContent).toContain(en["cloud.reached"]); expect(document.querySelector("[role=alert]")).toBeNull();
    const chart = host.querySelector("[role=img]")!;
    await act(async () => { chart.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true })); });
    expect(host.textContent).toContain(en["cloud.billed"]); expect(host.querySelector("[title]")?.getAttribute("title")).toContain("Bill updated");
  });
  it("never fetches cloud data for other roles or edge", async () => {
    owner = false; await mount(<><NodeCloudUsage nodeId="n"/><CloudResourceCard nodeId="n"/><CloudAccountsPage/></>);
    expect(api.getCloudUsage).not.toHaveBeenCalled(); expect(api.listCloudAccounts).not.toHaveBeenCalled(); expect(host.textContent).toBe("");
    owner = true; edge = true; await mount(<NodeCloudUsage nodeId="n"/>); expect(api.getCloudUsage).not.toHaveBeenCalled();
  });
  it("hides unbound nodes and keeps cached measurements on a read failure", async () => {
    api.getCloudUsage.mockResolvedValue(data({ resource: undefined })); await mount(<NodeCloudUsage nodeId="n"/>); expect(host.textContent).toBe("");
    act(() => { qc.setQueryData(cloudUsageQuery("n").queryKey, data()); }); await settle();
    api.getCloudUsage.mockRejectedValue(new ConnectError("offline",Code.Unavailable));
    await act(async () => { await qc.refetchQueries({ queryKey: ["cloud-usage","n"] }); });
    expect(host.textContent).toContain(en["cloud.title"]); expect(host.textContent).toContain("0 B"); expect(host.textContent).not.toContain("offline"); expect(document.querySelector("[role=alert]")).toBeNull();
  });
  it("saves reminder thresholds as bytes and exact money", async () => {
    qc.setQueryData(cloudAccountsQuery().queryKey,[account]); qc.setQueryData(cloudUsageQuery("n").queryKey,data());
    await mount(<CloudResourceCard nodeId="n"/>);
    const inputs = host.querySelectorAll<HTMLInputElement>('input[type="number"]');
    await type(inputs[0]!,"200"); await type(inputs[1]!,"12.34");
    await act(async () => { host.querySelector("form")!.dispatchEvent(new Event("submit",{bubbles:true,cancelable:true})); }); await settle();
    expect(api.bindCloudResource).toHaveBeenCalledWith(expect.objectContaining({nodeId:"n",resource:expect.objectContaining({thresholdBytes:200000000000n,thresholdAmount:"12.34",thresholdCurrency:"CNY"})}));
  });
  it("does not render saved keys and limits action errors to the account form", async () => {
    qc.setQueryData(cloudAccountsQuery().queryKey,[account]); await mount(<CloudAccountsPage/>); await click(button(en["cloud.edit"]));
    const secret = document.querySelector<HTMLInputElement>('input[type="password"]')!; expect(secret.value).toBe("");
    api.saveCloudAccount.mockRejectedValue(new ConnectError("cloud_permission",Code.PermissionDenied));
    await act(async () => { document.querySelector("form")!.dispatchEvent(new Event("submit",{bubbles:true,cancelable:true})); }); await settle();
    expect(document.querySelector("[role=dialog] [role=alert]")?.textContent).toBe(en["cloud.permission"]);
    expect(api.saveCloudAccount).toHaveBeenCalledWith(expect.objectContaining({accessKeyId:"",accessKeySecret:""}));
  });
});
