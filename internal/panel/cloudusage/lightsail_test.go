//go:build !js

package cloudusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func fakeLightsail(t *testing.T, reply func(string, map[string]any) (string, int)) *awsLightsail {
	t.Helper()
	a := newLightsail()
	a.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.Scheme != "https" || r.URL.RawQuery != "" || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=key/") {
			t.Fatal("expected signed HTTPS POST")
		}
		var p map[string]any
		action := r.Header.Get("X-Amz-Target")
		contentType := "application/x-amz-json-1.1"
		if r.URL.Host == "sts.us-east-1.amazonaws.com" {
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			action = r.PostForm.Get("Action")
			contentType = "text/xml"
		} else {
			if r.URL.Host != "lightsail.ap-southeast-1.amazonaws.com" {
				t.Fatalf("unexpected endpoint %s", r.URL.Host)
			}
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				t.Fatal(err)
			}
		}
		body, status := reply(action, p)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": {contentType}}}, nil
	})
	return a
}

func TestLightsailIdentityPlanAndCompleteIntervals(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	a := fakeLightsail(t, func(action string, p map[string]any) (string, int) {
		switch action {
		case "GetCallerIdentity":
			return `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Account>123456789012</Account></GetCallerIdentityResult></GetCallerIdentityResponse>`, 200
		case "Lightsail_20161128.GetInstance":
			return fmt.Sprintf(`{"instance":{"name":"example-node","createdAt":%d,"location":{"regionName":"ap-southeast-1"},"networking":{"monthlyTransfer":{"gbPerMonthAllocated":2048}}}}`, start.Unix()), 200
		case "Lightsail_20161128.GetInstanceMetricData":
			if p["instanceName"] != "example-node" || p["period"] != float64(300) || p["unit"] != "Bytes" {
				t.Fatal(p)
			}
			metric := p["metricName"].(string)
			// Out-of-order data, known zeros, an incomplete ten-minute pair, and an exclusive endpoint.
			points := []map[string]any{}
			for _, offset := range []int64{900, 300, 0, 600, 1200, 1800} {
				if metric == "NetworkIn" && offset == 900 {
					continue
				}
				points = append(points, map[string]any{"timestamp": start.Unix() + offset, "sum": offset, "unit": "Bytes"})
			}
			body, _ := json.Marshal(map[string]any{"metricName": metric, "metricData": points})
			return string(body), 200
		default:
			t.Fatalf("unexpected read %s", action)
		}
		return "", 500
	})
	c := credentials{"key", "secret"}
	if id, err := a.Identity(context.Background(), c); err != nil || id != "123456789012" {
		t.Fatal(id, err)
	}
	i, err := a.Instance(context.Background(), c, "ap-southeast-1", "example-node")
	if err != nil || !i.CreatedAt.Equal(start) || i.AllowanceGB != 2048 {
		t.Fatal(i, err)
	}
	points, err := a.Usage(context.Background(), c, "ap-southeast-1", "example-node", start, start.Add(30*time.Minute))
	if err != nil || len(points) != 1 || points[0].End != start.Unix()+600 || points[0].Bytes != 300 || points[0].Inbound == nil || *points[0].Inbound != 300 {
		t.Fatalf("%+v %v", points, err)
	}
}

func TestLightsailErrorsAreRedacted(t *testing.T) {
	for _, tc := range []struct {
		code string
		want error
	}{{"AccessDeniedException", errPermission}, {"UnrecognizedClientException", errCredentials}} {
		a := fakeLightsail(t, func(string, map[string]any) (string, int) {
			return `{"__type":"` + tc.code + `","message":"secret account data"}`, 400
		})
		_, err := a.Instance(context.Background(), credentials{"key", "secret"}, "ap-southeast-1", "example-node")
		if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "secret") {
			t.Fatal(err)
		}
	}
}

func TestLightsailUTCMonthAndCombinedThreshold(t *testing.T) {
	e := newEnv(t)
	e.now = time.Date(2026, 11, 1, 0, 20, 0, 0, time.UTC)
	start := time.Date(2026, 10, 31, 23, 50, 0, 0, time.UTC)
	e.f.created = start
	e.f.identity = "123456789012"
	e.f.usage = func(_ context.Context, from, until time.Time) ([]sample, error) {
		inbound := uint64(700)
		var points []sample
		for ts := from.Unix() + 600; ts <= until.Unix(); ts += 600 {
			points = append(points, sample{End: ts, Bytes: 300, Inbound: &inbound})
		}
		return points, nil
	}
	e.s.providers["aws_lightsail"] = e.f
	created, err := e.s.SaveCloudAccount(e.ctx, connect.NewRequest(&adminv1.SaveCloudAccountRequest{Name: "AWS", Provider: "aws_lightsail", AccessKeyId: "key", AccessKeySecret: "secret"}))
	if err != nil {
		t.Fatal(err)
	}
	a := created.Msg.Account
	r := &adminv1.CloudResource{AccountId: a.Id, Region: "ap-southeast-1", InstanceId: "example-node", ThresholdBytes: 900}
	if _, err = e.s.BindCloudResource(e.ctx, connect.NewRequest(&adminv1.BindCloudResourceRequest{NodeId: "nod_one", Resource: r})); err != nil {
		t.Fatal(err)
	}
	e.s.collect(e.ctx)
	for _, month := range []string{"2026-10", "2026-11"} {
		got := e.get(t, month)
		if got.Provider != "aws_lightsail" || got.OutboundBytes != 300 || got.InboundBytes != 700 || !got.ThresholdReached || got.Partial || got.HasCost {
			t.Fatalf("%s: %+v", month, got)
		}
	}
	_, err = e.s.SaveCloudAccount(e.ctx, connect.NewRequest(&adminv1.SaveCloudAccountRequest{Id: a.Id, Name: "AWS", Provider: "aliyun"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatal("allowed provider switch", err)
	}
	_, err = e.s.SaveCloudAccount(e.ctx, connect.NewRequest(&adminv1.SaveCloudAccountRequest{Id: a.Id, Name: "AWS", Provider: "aws_lightsail", BillingEnabled: true}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatal("allowed unsupported billing", err)
	}
	month, _, err := monthBounds("", time.Date(2026, 10, 31, 17, 0, 0, 0, time.UTC), providerZone("aws_lightsail"))
	if err != nil || month.Format("2006-01") != "2026-10" {
		t.Fatal(month, err)
	}
}

func TestLightsailMigrationKeepsExistingHistory(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v1, err := migrations.ReadFile("migrations/00001_cloud_usage.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.W.ExecContext(ctx, string(v1)); err != nil {
		t.Fatal(err)
	}
	_, err = st.W.ExecContext(ctx, `CREATE TABLE cloud_usage_schema(id INTEGER PRIMARY KEY AUTOINCREMENT,version_id INTEGER NOT NULL,is_applied INTEGER NOT NULL,tstamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP);
	INSERT INTO cloud_usage_schema(version_id,is_applied) VALUES(0,1),(1,1);
	INSERT INTO cloud_usage_account(id,name,provider,account_id) VALUES('cld_one','Existing','aliyun','1234567890123456');
	INSERT INTO cloud_usage_resource(id,account_id,region,instance_id,created_at) VALUES('csr_one','cld_one','ap-southeast-1','i-example123',1);
	INSERT INTO cloud_usage_sample(resource_id,interval_end,outbound_bytes) VALUES('csr_one',600,123);`)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrate(ctx, st); err != nil {
		t.Fatal(err)
	}
	var outbound uint64
	var inbound *uint64
	if err = st.R.QueryRowContext(ctx, `SELECT outbound_bytes,inbound_bytes FROM cloud_usage_sample`).Scan(&outbound, &inbound); err != nil || outbound != 123 || inbound != nil {
		t.Fatal(outbound, inbound, err)
	}
	if err = migrate(ctx, st); err != nil {
		t.Fatal("not idempotent", err)
	}
}
