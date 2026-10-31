package cloudusage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fakeAliyun(t *testing.T, response func(url.Values) (string, int)) *aliyun {
	t.Helper()
	a := newAliyun()
	a.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.Scheme != "https" || r.URL.RawQuery != "" || r.URL.Path != "/" {
			t.Fatal("credentials must use HTTPS form body")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		p := r.PostForm
		signature := p.Get("Signature")
		p.Del("Signature")
		if p.Get("AccessKeyId") != "key" || signature != signRPC(p, "secret") {
			t.Fatal("bad signature")
		}
		body, status := response(p)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	return a
}

func TestRPCEncodingAndResponseRedaction(t *testing.T) {
	if got := rpcEncode("a b~*/&"); got != "a%20b~%2A%2F%26" {
		t.Fatal(got)
	}
	// Fixed canonical string, signed independently from the HTTP request builder.
	if got := signRPC(url.Values{"Action": {"DescribeInstances"}, "Literal": {"a b~*/&"}, "Version": {"2014-05-26"}}, "example-secret"); got != "t7ct6vhhT4I6sha9I+6asi6G6Ks=" {
		t.Fatal(got)
	}
	for _, tc := range []struct {
		body   string
		status int
		want   error
	}{
		{`{"Code":"InvalidAccessKeyId.NotFound","Message":"secret key leaked by upstream"}`, 400, errCredentials},
		{`{"Code":"Forbidden.RAM","Message":"secret"}`, 403, errPermission},
		{`{"Code":"InternalError","Message":"secret"}`, 500, errCloud},
		{`<html>secret</html>`, 200, errData},
	} {
		a := fakeAliyun(t, func(url.Values) (string, int) { return tc.body, tc.status })
		_, err := a.Identity(context.Background(), credentials{"key", "secret"})
		if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe error %v", err)
		}
	}
}

func TestMonitorConvertsIntervalTotalOnceAndKeepsZero(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(20 * time.Minute)
	a := fakeAliyun(t, func(p url.Values) (string, int) {
		if p.Get("Period") != "600" || p.Get("InstanceId") != "i-example123" {
			t.Fatal(p.Get("Period"))
		}
		return `{"MonitorData":{"InstanceMonitorData":[{"InstanceId":"i-example123","TimeStamp":"2026-10-01T00:00:00Z","InternetTX":500},{"InstanceId":"i-example123","TimeStamp":"2026-10-01T00:10:00Z","InternetTX":343},{"InstanceId":"i-example123","TimeStamp":"2026-10-01T00:20:00Z","InternetTX":0}]}}`, 200
	})
	points, err := a.Usage(context.Background(), credentials{"key", "secret"}, "ap-southeast-1", "i-example123", start, end)
	if err != nil || len(points) != 2 || points[0].Bytes != 42875 || points[1].Bytes != 0 {
		t.Fatalf("interval total: %+v %v", points, err)
	}
	for _, body := range []string{`{}`, `{"MonitorData":{}}`, `{"MonitorData":{"InstanceMonitorData":[{"InstanceId":"i-example123","TimeStamp":"2026-10-01T00:10:00Z"}]}}`, `{"MonitorData":{"InstanceMonitorData":[{"InstanceId":"i-example123","TimeStamp":"2026-10-01T00:10:01Z","InternetTX":4}]}}`, `{"MonitorData":{"InstanceMonitorData":[{"InstanceId":"i-other","TimeStamp":"2026-10-01T00:10:00Z","InternetTX":4}]}}`} {
		a = fakeAliyun(t, func(url.Values) (string, int) { return body, 200 })
		if _, err = a.Usage(context.Background(), credentials{"key", "secret"}, "ap-southeast-1", "i-example123", start, end); !errors.Is(err, errData) {
			t.Fatalf("accepted malformed observation %s", body)
		}
	}
}

func billJSON(account, month, next string, items ...map[string]any) string {
	if items == nil {
		items = []map[string]any{}
	}
	b, _ := json.Marshal(map[string]any{"Success": true, "Code": "Success", "Data": map[string]any{"AccountID": account, "BillingCycle": month, "NextToken": next, "Items": items}})
	return string(b)
}
func billItem(id, code, amount string) map[string]any {
	return map[string]any{"InstanceID": id, "OwnerID": "1234567890123456", "PipCode": "cdt", "BillingItemCode": code, "Currency": "CNY", "PretaxAmount": json.Number(amount)}
}

func TestBillPaginatesCDTLayoutsAndRejectsIncompleteSnapshots(t *testing.T) {
	const accountID = "1234567890123456"
	const month = "2026-10"
	var requests int
	a := fakeAliyun(t, func(p url.Values) (string, int) {
		requests++
		if p.Get("IsHideZeroCharge") != "false" || p.Get("IsBillingItem") != "true" || p.Get("PipCode") != "cdt" || p.Get("ProductCode") != "cdt" || p.Get("Granularity") != "MONTHLY" {
			t.Fatal("missing bill scope")
		}
		if p.Get("NextToken") == "" {
			return billJSON(accountID, month, "second", billItem("i-example123;ap-southeast-1;BGP;0", "ECS_Out_Bytes", "0.10")), 200
		}
		return billJSON(accountID, month, "", billItem("i-example123", "internet_traffic", "0.20")), 200
	})
	entries, err := a.Bill(context.Background(), credentials{"key", "secret"}, accountID, month)
	if err != nil || requests != 2 || len(entries) != 2 || entries[0].Region != "ap-southeast-1" || entries[1].Amount != "0.20" {
		t.Fatalf("bill parse: %+v %v", entries, err)
	}
	for _, tc := range []struct {
		name     string
		response func(url.Values) (string, int)
	}{
		{"later page failed", func(p url.Values) (string, int) {
			if p.Get("NextToken") == "" {
				return billJSON(accountID, month, "second", billItem("i-example123", "internet_traffic", "1")), 200
			}
			return `{"Code":"InternalError"}`, 500
		}},
		{"cycle", func(url.Values) (string, int) { return billJSON(accountID, month, "repeat"), 200 }},
		{"wrong account", func(url.Values) (string, int) { return billJSON("9876543210123456", month, ""), 200 }},
		{"wrong month", func(url.Values) (string, int) { return billJSON(accountID, "2026-09", ""), 200 }},
		{"unknown item", func(url.Values) (string, int) {
			return billJSON(accountID, month, "", billItem("i-example123", "unknown_egress", "1")), 200
		}},
		{"missing amount", func(url.Values) (string, int) {
			item := billItem("i-example123", "internet_traffic", "0")
			delete(item, "PretaxAmount")
			return billJSON(accountID, month, "", item), 200
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := fakeAliyun(t, tc.response)
			got, err := a.Bill(context.Background(), credentials{"key", "secret"}, accountID, month)
			if err == nil || got != nil {
				t.Fatalf("partial snapshot returned: %+v %v", got, err)
			}
		})
	}
}

func TestDecimalAndBillingTimezone(t *testing.T) {
	for _, v := range []string{"NaN", "1e3", "1/3", "--1", "1.2.3", "", "+1"} {
		if _, ok := decimal(v); ok {
			t.Errorf("accepted %s", v)
		}
	}
	now := time.Date(2026, 10, 31, 16, 1, 0, 0, time.UTC)
	start, end, err := monthBounds("", now)
	if err != nil || start.Format("2006-01") != "2026-11" || start.UTC().Hour() != 16 || end.Sub(start) != 30*24*time.Hour {
		t.Fatal("billing month not UTC+8")
	}
}
