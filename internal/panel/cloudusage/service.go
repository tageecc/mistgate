// Package cloudusage reads cloud monitoring and bills without changing the proxy fleet.
package cloudusage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

type Config struct {
	Store  *store.Store
	Vault  *vault.Vault
	StepUp func(context.Context) error
	Log    *slog.Logger
	Now    func() time.Time
	Edge   bool
}

type Service struct {
	cfg      Config
	provider provider
	initErr  error
	kick     chan struct{}
	mu       sync.Mutex
	active   map[string]context.CancelFunc
}

func New(c Config) *Service {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	s := &Service{cfg: c, provider: newAliyun(), kick: make(chan struct{}, 1), active: map[string]context.CancelFunc{}}
	if c.Edge {
		s.initErr = errors.New("VPS edition required")
	} else {
		s.initErr = migrate(context.Background(), c.Store)
		if s.initErr != nil {
			c.Log.Error("cloud usage initialization failed", "err", s.initErr)
		}
	}
	return s
}

func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewCloudUsageServiceHandler(s, append([]connect.HandlerOption{connect.WithReadMaxBytes(16 << 10)}, opts...)...)
}

func (s *Service) guard(ctx context.Context, write bool) error {
	a, ok := auth.AdminFrom(ctx)
	if !ok || a.Role != store.RoleOwner {
		return connect.NewError(connect.CodePermissionDenied, errors.New("owner required"))
	}
	if s.initErr != nil {
		return connect.NewError(connect.CodeUnavailable, errors.New("cloud_unavailable"))
	}
	if write {
		return s.cfg.StepUp(ctx)
	}
	return nil
}

func failure(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return connect.NewError(connect.CodeNotFound, errors.New("cloud_not_found"))
	}
	if errors.Is(err, errCredentials) {
		return connect.NewError(connect.CodeFailedPrecondition, errCredentials)
	}
	if errors.Is(err, errPermission) {
		return connect.NewError(connect.CodePermissionDenied, errPermission)
	}
	return connect.NewError(connect.CodeUnavailable, errCloud)
}
func invalid() error {
	return connect.NewError(connect.CodeInvalidArgument, errors.New("cloud_invalid"))
}

type account struct {
	ID, Name, Provider, AccountID string
	Sealed                        []byte
	Revision                      int64
	Billing, Reconnect            bool
	BillAttempt                   int64
}

func (a account) proto() *adminv1.CloudAccount {
	return &adminv1.CloudAccount{Id: a.ID, Name: a.Name, Provider: a.Provider, AccountId: a.AccountID, Connected: len(a.Sealed) > 0, BillingEnabled: a.Billing, Reconnect: a.Reconnect}
}
func (s *Service) accounts(ctx context.Context) ([]account, error) {
	r, err := s.cfg.Store.R.QueryContext(ctx, `SELECT id,name,provider,account_id,credentials,revision,billing_enabled,reconnect,bill_attempt FROM cloud_usage_account ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var out []account
	for r.Next() {
		var a account
		if err := r.Scan(&a.ID, &a.Name, &a.Provider, &a.AccountID, &a.Sealed, &a.Revision, &a.Billing, &a.Reconnect, &a.BillAttempt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, r.Err()
}
func (s *Service) account(ctx context.Context, id string) (account, error) {
	all, err := s.accounts(ctx)
	if err != nil {
		return account{}, err
	}
	for _, a := range all {
		if a.ID == id {
			return a, nil
		}
	}
	return account{}, sql.ErrNoRows
}
func (s *Service) open(a account) (credentials, error) {
	b, err := s.cfg.Vault.Open(a.Sealed, "cloud-usage/"+a.ID)
	if err != nil {
		return credentials{}, errCredentials
	}
	var c credentials
	if json.Unmarshal(b, &c) != nil {
		return c, errCredentials
	}
	return c, nil
}
func (s *Service) wake() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}
func (s *Service) cancel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.active[id]; f != nil {
		f()
	}
}
func (s *Service) audit(ctx context.Context, action, id string) {
	a, _ := auth.AdminFrom(ctx)
	p, _ := json.Marshal(map[string]string{"id": id})
	if err := s.cfg.Store.Audit(ctx, s.cfg.Now(), store.AuditEntry{Actor: a.ID, Action: action, Params: string(p), Result: "ok"}); err != nil {
		s.cfg.Log.Warn("cloud usage audit failed")
	}
}

func (s *Service) ListCloudAccounts(ctx context.Context, _ *connect.Request[adminv1.ListCloudAccountsRequest]) (*connect.Response[adminv1.ListCloudAccountsResponse], error) {
	if err := s.guard(ctx, false); err != nil {
		return nil, err
	}
	all, err := s.accounts(ctx)
	if err != nil {
		return nil, failure(err)
	}
	out := &adminv1.ListCloudAccountsResponse{}
	for _, a := range all {
		out.Accounts = append(out.Accounts, a.proto())
	}
	return connect.NewResponse(out), nil
}

func (s *Service) SaveCloudAccount(ctx context.Context, req *connect.Request[adminv1.SaveCloudAccountRequest]) (*connect.Response[adminv1.SaveCloudAccountResponse], error) {
	if err := s.guard(ctx, true); err != nil {
		return nil, err
	}
	m := req.Msg
	name := strings.TrimSpace(m.Name)
	if name == "" || len(name) > 120 || m.Provider != "aliyun" || len(m.AccessKeyId) > 256 || len(m.AccessKeySecret) > 256 {
		return nil, invalid()
	}
	a := account{ID: m.Id, Name: name, Provider: m.Provider, Billing: m.BillingEnabled}
	if a.ID != "" {
		old, err := s.account(ctx, a.ID)
		if err != nil {
			return nil, failure(err)
		}
		a.AccountID = old.AccountID
		a.Sealed = old.Sealed
		a.Revision = old.Revision
	}
	if m.AccessKeyId != "" || m.AccessKeySecret != "" {
		if strings.TrimSpace(m.AccessKeyId) == "" || strings.TrimSpace(m.AccessKeySecret) == "" {
			return nil, invalid()
		}
		c := credentials{strings.TrimSpace(m.AccessKeyId), strings.TrimSpace(m.AccessKeySecret)}
		ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		id, err := s.provider.Identity(ctx, c)
		if err != nil {
			return nil, failure(err)
		}
		if a.AccountID != "" && a.AccountID != id {
			return nil, invalid()
		}
		a.AccountID = id
		if a.ID == "" {
			a.ID = store.NewID("cld_")
		}
		b, _ := json.Marshal(c)
		a.Sealed = s.cfg.Vault.Seal(b, "cloud-usage/"+a.ID)
	}
	if a.ID == "" || len(a.Sealed) == 0 {
		return nil, invalid()
	}
	s.cancel(a.ID)
	var err error
	if m.Id == "" {
		var n int
		if err := s.cfg.Store.R.QueryRowContext(ctx, `SELECT count(*) FROM cloud_usage_account`).Scan(&n); err != nil {
			return nil, failure(err)
		}
		if n >= 20 {
			return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("cloud_account_limit"))
		}
		_, err = s.cfg.Store.W.ExecContext(ctx, `INSERT INTO cloud_usage_account(id,name,provider,account_id,credentials,billing_enabled) VALUES(?,?,?,?,?,?)`, a.ID, a.Name, a.Provider, a.AccountID, a.Sealed, a.Billing)
	} else {
		var res sql.Result
		res, err = s.cfg.Store.W.ExecContext(ctx, `UPDATE cloud_usage_account SET name=?,credentials=?,billing_enabled=?,revision=revision+1,reconnect=0,bill_attempt=0 WHERE id=? AND revision=?`, a.Name, a.Sealed, a.Billing, a.ID, a.Revision)
		if err == nil {
			n, _ := res.RowsAffected()
			if n != 1 {
				return nil, connect.NewError(connect.CodeAborted, errors.New("cloud_changed"))
			}
		}
	}
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("cloud_exists"))
		}
		return nil, failure(err)
	}
	s.audit(ctx, "cloud_account_saved", a.ID)
	s.wake()
	return connect.NewResponse(&adminv1.SaveCloudAccountResponse{Account: a.proto()}), nil
}

func (s *Service) DisconnectCloudAccount(ctx context.Context, req *connect.Request[adminv1.DisconnectCloudAccountRequest]) (*connect.Response[adminv1.DisconnectCloudAccountResponse], error) {
	if err := s.guard(ctx, true); err != nil {
		return nil, err
	}
	s.cancel(req.Msg.Id)
	_, err := s.cfg.Store.W.ExecContext(ctx, `UPDATE cloud_usage_account SET credentials=NULL,revision=revision+1,reconnect=0 WHERE id=?`, req.Msg.Id)
	if err != nil {
		return nil, failure(err)
	}
	s.audit(ctx, "cloud_account_disconnected", req.Msg.Id)
	return connect.NewResponse(&adminv1.DisconnectCloudAccountResponse{}), nil
}

func (s *Service) BindCloudResource(ctx context.Context, req *connect.Request[adminv1.BindCloudResourceRequest]) (*connect.Response[adminv1.BindCloudResourceResponse], error) {
	if err := s.guard(ctx, true); err != nil {
		return nil, err
	}
	m := req.Msg
	var count int
	if err := s.cfg.Store.R.QueryRowContext(ctx, `SELECT count(*) FROM node WHERE id=?`, m.NodeId).Scan(&count); err != nil {
		return nil, failure(err)
	}
	if count != 1 {
		return nil, invalid()
	}
	if m.Resource == nil {
		_, err := s.cfg.Store.W.ExecContext(ctx, `DELETE FROM cloud_usage_binding WHERE node_id=?`, m.NodeId)
		if err != nil {
			return nil, failure(err)
		}
	} else {
		r := m.Resource
		if !regionPattern.MatchString(r.Region) || !instancePattern.MatchString(r.InstanceId) || r.ThresholdBytes > 1<<50 {
			return nil, invalid()
		}
		if r.ThresholdAmount != "" {
			v, ok := decimal(r.ThresholdAmount)
			if !ok || v.Sign() < 0 || !(r.ThresholdCurrency == "CNY" || r.ThresholdCurrency == "USD" || r.ThresholdCurrency == "JPY") {
				return nil, invalid()
			}
		}
		// Editing local reminder thresholds must work even when cloud access is disconnected.
		result, err := s.cfg.Store.W.ExecContext(ctx, `UPDATE cloud_usage_binding SET threshold_bytes=?,threshold_amount=?,threshold_currency=? WHERE node_id=? AND resource_id IN (SELECT id FROM cloud_usage_resource WHERE account_id=? AND region=? AND instance_id=?)`, r.ThresholdBytes, r.ThresholdAmount, r.ThresholdCurrency, m.NodeId, r.AccountId, r.Region, r.InstanceId)
		if err != nil {
			return nil, failure(err)
		}
		if n, err := result.RowsAffected(); err != nil {
			return nil, failure(err)
		} else if n == 1 {
			s.audit(ctx, "cloud_resource_saved", m.NodeId)
			return connect.NewResponse(&adminv1.BindCloudResourceResponse{}), nil
		}
		a, err := s.account(ctx, r.AccountId)
		if err != nil {
			return nil, failure(err)
		}
		c, err := s.open(a)
		if err != nil {
			return nil, failure(err)
		}
		ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		i, err := s.provider.Instance(ctx, c, r.Region, r.InstanceId)
		if err != nil {
			return nil, failure(err)
		}
		tx, err := s.cfg.Store.W.BeginTx(ctx, nil)
		if err != nil {
			return nil, failure(err)
		}
		defer tx.Rollback()
		var current int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM cloud_usage_account WHERE id=? AND revision=? AND credentials IS NOT NULL`, a.ID, a.Revision).Scan(&current); err != nil {
			return nil, failure(err)
		} else if current != 1 {
			return nil, connect.NewError(connect.CodeAborted, errors.New("cloud_changed"))
		}
		var resourceID string
		err = tx.QueryRowContext(ctx, `INSERT INTO cloud_usage_resource(id,account_id,region,instance_id,created_at) VALUES(?,?,?,?,?) ON CONFLICT(account_id,region,instance_id) DO UPDATE SET created_at=excluded.created_at RETURNING id`, store.NewID("csr_"), a.ID, r.Region, r.InstanceId, i.CreatedAt.Unix()).Scan(&resourceID)
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO cloud_usage_binding(node_id,resource_id,threshold_bytes,threshold_amount,threshold_currency) VALUES(?,?,?,?,?) ON CONFLICT(node_id) DO UPDATE SET resource_id=excluded.resource_id,threshold_bytes=excluded.threshold_bytes,threshold_amount=excluded.threshold_amount,threshold_currency=excluded.threshold_currency`, m.NodeId, resourceID, r.ThresholdBytes, r.ThresholdAmount, r.ThresholdCurrency)
		}
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("cloud_resource_bound"))
			}
			return nil, failure(err)
		}
		if err = tx.Commit(); err != nil {
			return nil, failure(err)
		}
	}
	s.audit(ctx, "cloud_resource_saved", m.NodeId)
	s.wake()
	return connect.NewResponse(&adminv1.BindCloudResourceResponse{}), nil
}
