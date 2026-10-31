//go:build !js

package cloudusage

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// Exercise the real signed provider, collector and SQLite store together. The fake cloud accepts only the
// documented read scope, including the product condition used by the limited RAM billing policy.
func TestAliyunReadOnlyCollectionAndBillingPermissionRecovery(t *testing.T) {
	e := newEnv(t)
	e.now = e.f.created.Add(30 * time.Minute)
	created := e.f.created
	const accountID = "1234567890123456"
	const instanceID = "i-example123"
	const region = "ap-southeast-1"
	calls := map[string]int{}
	billingDenied := false
	e.s.provider = fakeAliyun(t, func(p url.Values) (string, int) {
		action := p.Get("Action")
		calls[action]++
		switch action {
		case "GetCallerIdentity":
			return `{"IdentityType":"RAMUser","AccountId":"` + accountID + `"}`, 200
		case "DescribeInstances":
			if p.Get("RegionId") != region || p.Get("InstanceIds") != `["`+instanceID+`"]` {
				t.Fatal("instance lookup escaped the permitted resource")
			}
			return `{"Instances":{"Instance":[{"InstanceId":"` + instanceID + `","CreationTime":"` + created.UTC().Format(time.RFC3339) + `","PublicIpAddress":{"IpAddress":["192.0.2.1"]}}]}}`, 200
		case "DescribeInstanceMonitorData":
			if p.Get("RegionId") != region || p.Get("InstanceId") != instanceID || p.Get("Period") != "600" {
				t.Fatal("monitor request escaped the permitted resource or interval")
			}
			start, err := time.Parse(time.RFC3339, p.Get("StartTime"))
			if err != nil {
				t.Fatal(err)
			}
			end, err := time.Parse(time.RFC3339, p.Get("EndTime"))
			if err != nil {
				t.Fatal(err)
			}
			points := []map[string]any{}
			for at := start.Add(10 * time.Minute); !at.After(end); at = at.Add(10 * time.Minute) {
				tx := 0
				if at.Equal(created.Add(10 * time.Minute)) {
					tx = 343
				}
				points = append(points, map[string]any{"InstanceId": instanceID, "TimeStamp": at.UTC().Format(time.RFC3339), "InternetTX": tx})
			}
			body, err := json.Marshal(map[string]any{"MonitorData": map[string]any{"InstanceMonitorData": points}})
			if err != nil {
				t.Fatal(err)
			}
			return string(body), 200
		case "DescribeInstanceBill":
			if p.Get("ProductCode") != "cdt" || billingDenied {
				return `{"Code":"Forbidden.RAM","Message":"Bill read is not authorized"}`, 403
			}
			month := p.Get("BillingCycle")
			if month == "2026-09" {
				return billJSON(accountID, month, ""), 200
			}
			if month != "2026-10" {
				t.Fatalf("unexpected billing month %q", month)
			}
			return billJSON(accountID, month, "",
				billItem(instanceID+";"+region+";BGP;ecs", "internet_traffic", "0.10"),
				billItem(instanceID, "internet_traffic", "0.20"),
				billItem("i-other123", "internet_traffic", "9.00")), 200
		default:
			t.Fatalf("collector requested an undeclared cloud operation %q", action)
			return "", 403
		}
	})
	saved, err := e.s.SaveCloudAccount(e.ctx, connect.NewRequest(&adminv1.SaveCloudAccountRequest{
		Name: "Example", Provider: "aliyun", AccessKeyId: "key", AccessKeySecret: "secret", BillingEnabled: true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	e.bind(t, saved.Msg.Account)
	e.s.collect(e.ctx)
	first := e.get(t, "")
	if !first.HasUsage || first.OutboundBytes != 42875 || first.Partial || !first.HasCost || first.BilledAmount != "0.3" || first.Currency != "CNY" {
		t.Fatalf("first collection: %+v", first)
	}
	if calls["GetCallerIdentity"] != 1 || calls["DescribeInstances"] != 1 || calls["DescribeInstanceMonitorData"] != 1 || calls["DescribeInstanceBill"] != 2 {
		t.Fatalf("unexpected API requests: %v", calls)
	}
	e.s.collect(e.ctx)
	if got := e.get(t, ""); got.OutboundBytes != first.OutboundBytes || calls["DescribeInstanceBill"] != 2 {
		t.Fatal("repeated collection double counted traffic or bypassed the bill refresh interval")
	}

	billingDenied = true
	e.now = e.now.Add(6 * time.Hour)
	e.s.collect(e.ctx)
	denied := e.get(t, "")
	if denied.Partial || denied.ObservedThroughUnix <= first.ObservedThroughUnix || denied.OutboundBytes != first.OutboundBytes {
		t.Fatal("bill refusal interrupted monitoring")
	}
	if denied.BilledAmount != first.BilledAmount || denied.BilledThroughUnix != first.BilledThroughUnix || !e.mustAccount(t).Reconnect {
		t.Fatal("bill refusal lost the last complete snapshot or the reconnect state")
	}

	billingDenied = false
	e.now = e.now.Add(6 * time.Hour)
	e.s.collect(e.ctx)
	if got := e.get(t, ""); got.BilledThroughUnix <= first.BilledThroughUnix || e.mustAccount(t).Reconnect {
		t.Fatal("billing did not recover after permissions were restored")
	}
}
