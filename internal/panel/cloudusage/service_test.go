//go:build !js

package cloudusage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

type fakeProvider struct {
	identity      string
	created       time.Time
	instanceCalls int
	usage         func(context.Context, time.Time, time.Time) ([]sample, error)
	bill          func(string) ([]billEntry, error)
}

func (f *fakeProvider) Identity(context.Context, credentials) (string, error) { return f.identity, nil }
func (f *fakeProvider) Instance(context.Context, credentials, string, string) (instance, error) {
	f.instanceCalls++
	return instance{CreatedAt: f.created}, nil
}
func (f *fakeProvider) Usage(ctx context.Context, _ credentials, _, _ string, start, end time.Time) ([]sample, error) {
	if f.usage == nil {
		return nil, nil
	}
	return f.usage(ctx, start, end)
}
func (f *fakeProvider) Bill(_ context.Context, _ credentials, _, month string) ([]billEntry, error) {
	if f.bill == nil {
		return nil, nil
	}
	return f.bill(month)
}

type testEnv struct {
	s    *Service
	ctx  context.Context
	now  time.Time
	f    *fakeProvider
	logs bytes.Buffer
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	e := &testEnv{now: time.Date(2026, 10, 3, 1, 0, 0, 0, billingZone)}
	e.ctx = auth.WithAdmin(context.Background(), store.Admin{ID: "adm_example", Role: store.RoleOwner})
	st, err := store.Open(e.ctx, filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	v, err := vault.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	e.s = New(Config{Store: st, Vault: v, Now: func() time.Time { return e.now }, StepUp: func(context.Context) error { return nil }, Log: slog.New(slog.NewTextHandler(&e.logs, nil))})
	if e.s.initErr != nil {
		t.Fatal(e.s.initErr)
	}
	e.f = &fakeProvider{identity: "1234567890123456", created: time.Date(2026, 10, 1, 0, 0, 0, 0, billingZone)}
	e.s.providers["aliyun"] = e.f
	e.exec(t, `INSERT INTO node(id,name,address,created_at) VALUES('nod_one','one','192.0.2.1',1),('nod_two','two','192.0.2.2',1)`)
	return e
}
func (e *testEnv) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.s.cfg.Store.W.ExecContext(e.ctx, q, args...); err != nil {
		t.Fatal(err)
	}
}
func (e *testEnv) save(t *testing.T) *adminv1.CloudAccount {
	t.Helper()
	r, err := e.s.SaveCloudAccount(e.ctx, connect.NewRequest(&adminv1.SaveCloudAccountRequest{Name: "Example", Provider: "aliyun", AccessKeyId: "test-access-id", AccessKeySecret: "test-access-secret", BillingEnabled: true}))
	if err != nil {
		t.Fatal(err)
	}
	return r.Msg.Account
}
func (e *testEnv) bind(t *testing.T, a *adminv1.CloudAccount) *adminv1.CloudResource {
	t.Helper()
	r := &adminv1.CloudResource{AccountId: a.Id, Region: "ap-southeast-1", InstanceId: "i-example123"}
	if _, err := e.s.BindCloudResource(e.ctx, connect.NewRequest(&adminv1.BindCloudResourceRequest{NodeId: "nod_one", Resource: r})); err != nil {
		t.Fatal(err)
	}
	return r
}
func (e *testEnv) get(t *testing.T, month string) *adminv1.GetCloudUsageResponse {
	t.Helper()
	r, err := e.s.GetCloudUsage(e.ctx, connect.NewRequest(&adminv1.GetCloudUsageRequest{NodeId: "nod_one", Month: month}))
	if err != nil {
		t.Fatal(err)
	}
	return r.Msg
}
func (e *testEnv) points(t *testing.T, ps ...sample) {
	t.Helper()
	rs, err := e.s.resources(e.ctx, e.mustAccount(t).ID)
	if err != nil || len(rs) != 1 {
		t.Fatalf("resources: %v %v", rs, err)
	}
	for _, p := range ps {
		e.exec(t, `INSERT INTO cloud_usage_sample(resource_id,interval_end,outbound_bytes) VALUES(?,?,?) ON CONFLICT(resource_id,interval_end) DO UPDATE SET outbound_bytes=excluded.outbound_bytes`, rs[0].ID, p.End, p.Bytes)
	}
}
func (e *testEnv) mustAccount(t *testing.T) account {
	t.Helper()
	all, err := e.s.accounts(e.ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("accounts: %v", err)
	}
	return all[0]
}

func TestOwnerSessionOnlyAndStepUp(t *testing.T) {
	e := newEnv(t)
	paths := []string{adminv1connect.CloudUsageServiceListCloudAccountsProcedure, adminv1connect.CloudUsageServiceGetCloudUsageProcedure, adminv1connect.CloudUsageServiceSaveCloudAccountProcedure, adminv1connect.CloudUsageServiceDisconnectCloudAccountProcedure, adminv1connect.CloudUsageServiceBindCloudResourceProcedure}
	for i, p := range paths {
		if auth.ProcedureRole(p) != store.RoleOwner {
			t.Errorf("owner policy missing %s", p)
		}
		if _, ok := auth.TokenAccess(p); ok {
			t.Errorf("token reached %s", p)
		}
		if auth.NeedsStepUp(p) != (i >= 2) {
			t.Errorf("step-up policy %s", p)
		}
	}
	for _, role := range []string{"", store.RoleHelper, store.RoleReadonly} {
		ctx := auth.WithAdmin(context.Background(), store.Admin{Role: role})
		if err := e.s.guard(ctx, false); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("role %s: %v", role, err)
		}
	}
	e.s.cfg.StepUp = func(context.Context) error {
		return connect.NewError(connect.CodePermissionDenied, errors.New("step up"))
	}
	if _, err := e.s.SaveCloudAccount(e.ctx, connect.NewRequest(&adminv1.SaveCloudAccountRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatal(err)
	}
	if e.s.cfg.Edge = true; e.s.cfg.Edge {
		e.s.initErr = errors.New("unsupported")
		if err := e.s.guard(e.ctx, false); connect.CodeOf(err) != connect.CodeUnavailable {
			t.Fatal(err)
		}
	}
}

func TestCredentialsAreSealedIdentityPinnedAndDisconnected(t *testing.T) {
	e := newEnv(t)
	a := e.save(t)
	e.bind(t, a)
	stored := e.mustAccount(t)
	if bytes.Contains(stored.Sealed, []byte("test-access")) {
		t.Fatal("plaintext credentials in database")
	}
	if _, err := e.s.cfg.Vault.Open(stored.Sealed, "cloud-usage/other"); err == nil {
		t.Fatal("AAD not bound to account")
	}
	out, err := e.s.ListCloudAccounts(e.ctx, connect.NewRequest(&adminv1.ListCloudAccountsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out.Msg)
	if strings.Contains(string(b), "test-access") {
		t.Fatal("credential leaked to response")
	}
	e.f.identity = "9876543210123456"
	_, err = e.s.SaveCloudAccount(e.ctx, connect.NewRequest(&adminv1.SaveCloudAccountRequest{Id: a.Id, Name: "wrong owner", Provider: "aliyun", AccessKeyId: "another", AccessKeySecret: "another"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("rebind identity: %v", err)
	}
	if e.mustAccount(t).AccountID != a.AccountId {
		t.Fatal("cloud identity changed")
	}
	_, err = e.s.DisconnectCloudAccount(e.ctx, connect.NewRequest(&adminv1.DisconnectCloudAccountRequest{Id: a.Id}))
	if err != nil {
		t.Fatal(err)
	}
	if len(e.mustAccount(t).Sealed) != 0 || e.s.current(e.ctx, stored) {
		t.Fatal("disconnected credentials still current")
	}
	if e.get(t, "").Resource == nil {
		t.Fatal("disconnect deleted binding")
	}
	var audit string
	e.s.cfg.Store.R.QueryRowContext(e.ctx, `SELECT group_concat(params) FROM audit`).Scan(&audit)
	if strings.Contains(audit+e.logs.String(), "test-access") {
		t.Fatal("credential leaked to audit/log")
	}
}

func TestBindingUniqueThresholdsOfflineAndHistoryRetained(t *testing.T) {
	e := newEnv(t)
	a := e.save(t)
	r := e.bind(t, a)
	if _, err := e.s.BindCloudResource(e.ctx, connect.NewRequest(&adminv1.BindCloudResourceRequest{NodeId: "nod_two", Resource: r})); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("duplicate resource: %v", err)
	}
	e.points(t, sample{End: e.f.created.Add(10 * time.Minute).Unix(), Bytes: 200})
	e.s.DisconnectCloudAccount(e.ctx, connect.NewRequest(&adminv1.DisconnectCloudAccountRequest{Id: a.Id}))
	calls := e.f.instanceCalls
	r.ThresholdBytes = 100
	r.ThresholdAmount = "2.50"
	r.ThresholdCurrency = "CNY"
	if _, err := e.s.BindCloudResource(e.ctx, connect.NewRequest(&adminv1.BindCloudResourceRequest{NodeId: "nod_one", Resource: r})); err != nil {
		t.Fatal(err)
	}
	if e.f.instanceCalls != calls || !e.get(t, "").ThresholdReached {
		t.Fatal("offline threshold edit failed")
	}
	if _, err := e.s.BindCloudResource(e.ctx, connect.NewRequest(&adminv1.BindCloudResourceRequest{NodeId: "nod_one"})); err != nil {
		t.Fatal(err)
	}
	if e.get(t, "").Resource != nil {
		t.Fatal("unlink retained binding")
	}
	var n int
	e.s.cfg.Store.R.QueryRowContext(e.ctx, `SELECT count(*) FROM cloud_usage_sample`).Scan(&n)
	if n != 1 {
		t.Fatal("unlink removed history")
	}
}

func TestCollectorIdempotentCorrectedAndWindowBounded(t *testing.T) {
	e := newEnv(t)
	a := e.save(t)
	e.bind(t, a)
	e.f.created = e.now.Add(-40 * 24 * time.Hour)
	e.exec(t, `UPDATE cloud_usage_resource SET created_at=?`, e.f.created.Unix())
	var requests int
	var corrected uint64 = 10
	e.f.usage = func(_ context.Context, start, end time.Time) ([]sample, error) {
		requests++
		if end.Sub(start) > 399*600*time.Second || start.Before(e.now.Add(-30*24*time.Hour)) {
			t.Fatal("monitor API range exceeded")
		}
		var out []sample
		for ts := start.Unix() + 600; ts <= end.Unix(); ts += 600 {
			out = append(out, sample{End: ts, Bytes: corrected})
		}
		return out, nil
	}
	e.s.collect(e.ctx)
	first := e.get(t, "")
	if !first.HasUsage || first.Partial {
		t.Fatalf("first collection: %+v", first)
	}
	e.s.collect(e.ctx)
	again := e.get(t, "")
	if first.OutboundBytes != again.OutboundBytes {
		t.Fatal("repeated collection double counted")
	}
	corrected = 20
	e.s.collect(e.ctx)
	if e.get(t, "").OutboundBytes <= again.OutboundBytes {
		t.Fatal("provider correction ignored")
	}
	if requests < 10 {
		t.Fatal("backfill did not paginate")
	}
	var rev int64
	e.s.cfg.Store.R.QueryRowContext(e.ctx, `SELECT desired_revision FROM node WHERE id='nod_one'`).Scan(&rev)
	if rev != 0 {
		t.Fatal("metering changed desired state")
	}
}

func TestDisconnectDuringFetchCannotCommit(t *testing.T) {
	e := newEnv(t)
	a := e.save(t)
	e.bind(t, a)
	started := make(chan struct{})
	resume := make(chan struct{})
	e.f.usage = func(ctx context.Context, start, end time.Time) ([]sample, error) {
		close(started)
		<-resume
		return []sample{{End: end.Unix(), Bytes: 999}}, nil
	}
	done := make(chan struct{})
	go func() { e.s.collect(e.ctx); close(done) }()
	<-started
	if _, err := e.s.DisconnectCloudAccount(e.ctx, connect.NewRequest(&adminv1.DisconnectCloudAccountRequest{Id: a.Id})); err != nil {
		t.Fatal(err)
	}
	close(resume)
	<-done
	if e.get(t, "").HasUsage {
		t.Fatal("stale request wrote after disconnect")
	}
}

func TestUsageMonthBoundaryZeroMissingAndTrailingGap(t *testing.T) {
	e := newEnv(t)
	a := e.save(t)
	e.bind(t, a)
	boundary := e.f.created.Unix()
	e.points(t, sample{End: boundary, Bytes: 999}, sample{End: boundary + 600, Bytes: 0}, sample{End: boundary + 1200, Bytes: 250})
	got := e.get(t, "2026-10")
	if !got.HasUsage || got.OutboundBytes != 250 || !got.Partial || !got.Days[0].Partial || got.Days[1].HasUsage {
		t.Fatalf("gap/boundary handling: %+v", got)
	}
	e.now = time.Unix(boundary+1800, 0)
	got = e.get(t, "")
	if got.Partial || got.Days[0].Partial {
		t.Fatalf("complete through collection cutoff: %+v", got)
	}
	e.exec(t, `DELETE FROM cloud_usage_sample WHERE interval_end=?`, boundary+1200)
	e.now = time.Unix(boundary+1200, 0)
	got = e.get(t, "")
	if !got.HasUsage || got.OutboundBytes != 0 || got.Partial {
		t.Fatal("known zero became unknown")
	}
	e.exec(t, `DELETE FROM cloud_usage_sample`)
	got = e.get(t, "")
	if got.HasUsage {
		t.Fatal("missing observation became zero")
	}
}

func TestBillSnapshotsExactAmountsAndFailedFetchKeepsPrevious(t *testing.T) {
	e := newEnv(t)
	a := e.save(t)
	r := e.bind(t, a)
	r.ThresholdAmount = "0.3"
	r.ThresholdCurrency = "CNY"
	if _, err := e.s.BindCloudResource(e.ctx, connect.NewRequest(&adminv1.BindCloudResourceRequest{NodeId: "nod_one", Resource: r})); err != nil {
		t.Fatal(err)
	}
	entries := []billEntry{{r.InstanceId, r.Region, "0.1", "CNY"}, {r.InstanceId, "", "0.2", "CNY"}, {"i-other123", "", "90", "CNY"}}
	e.f.bill = func(string) ([]billEntry, error) { return entries, nil }
	e.s.collect(e.ctx)
	got := e.get(t, "")
	if !got.HasCost || got.BilledAmount != "0.3" || !got.ThresholdReached || got.BilledThroughUnix != e.now.Unix() {
		t.Fatalf("bill: %+v", got)
	}
	oldFetched := got.BilledThroughUnix
	e.now = e.now.Add(6 * time.Hour)
	e.f.bill = func(string) ([]billEntry, error) { return entries, errCloud }
	e.s.collect(e.ctx)
	if got = e.get(t, ""); got.BilledAmount != "0.3" || got.BilledThroughUnix != oldFetched {
		t.Fatal("partial bill overwrote good snapshot")
	}
	e.now = e.now.Add(6 * time.Hour)
	e.f.bill = func(string) ([]billEntry, error) { return []billEntry{{r.InstanceId, r.Region, "0", "CNY"}}, nil }
	e.s.collect(e.ctx)
	if got = e.get(t, ""); !got.HasCost || got.BilledAmount != "0" || got.ThresholdReached {
		t.Fatal("zero bill misrepresented")
	}
	e.now = e.now.Add(6 * time.Hour)
	e.f.bill = func(string) ([]billEntry, error) { return nil, nil }
	e.s.collect(e.ctx)
	if e.get(t, "").HasCost {
		t.Fatal("absent bill misrepresented as zero")
	}
}

func TestSnapshotContainsExtensionAndReopensWithoutExtensionMigration(t *testing.T) {
	e := newEnv(t)
	a := e.save(t)
	e.bind(t, a)
	e.points(t, sample{End: e.f.created.Unix() + 600, Bytes: 12})
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if err := e.s.cfg.Store.SnapshotDatabase(e.ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Open(e.ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var n int
	if err = restored.R.QueryRowContext(e.ctx, `SELECT count(*) FROM cloud_usage_sample`).Scan(&n); err != nil || n != 1 {
		t.Fatal("snapshot missing cloud history", err)
	}
	s := New(Config{Store: restored, Vault: e.s.cfg.Vault})
	if s.initErr != nil {
		t.Fatal(s.initErr)
	}
	restoredAccount, err := s.account(e.ctx, a.Id)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := s.open(restoredAccount); err != nil || c.Secret != "test-access-secret" {
		t.Fatal("restored credential unreadable")
	}
	e.exec(t, `INSERT INTO cloud_usage_schema(version_id,is_applied) VALUES(3,1)`)
	if err := migrate(e.ctx, e.s.cfg.Store); err == nil {
		t.Fatal("accepted future extension schema")
	}
	var core int
	if err := e.s.cfg.Store.R.QueryRowContext(e.ctx, `SELECT count(*) FROM node`).Scan(&core); err != nil || core != 2 {
		t.Fatal("extension refusal broke core")
	}
}
