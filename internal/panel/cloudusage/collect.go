package cloudusage

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type resource struct {
	ID, AccountID, Region, InstanceID string
	CreatedAt, ScannedUntil           int64
}

func (s *Service) Run(ctx context.Context) {
	if s.initErr != nil {
		return
	}
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		s.collect(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

func (s *Service) collect(ctx context.Context) {
	all, err := s.accounts(ctx)
	if err != nil {
		s.cfg.Log.Warn("cloud usage accounts unavailable")
		return
	}
	for _, a := range all {
		if len(a.Sealed) == 0 {
			continue
		}
		round, cancel := context.WithTimeout(ctx, 3*time.Minute)
		s.mu.Lock()
		s.active[a.ID] = cancel
		s.mu.Unlock()
		err := s.collectAccount(round, a)
		cancel()
		s.mu.Lock()
		delete(s.active, a.ID)
		s.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			// Error values contain only local codes, never cloud response bodies.
			s.cfg.Log.Warn("cloud usage sync incomplete", "account", a.ID, "code", safeCode(err))
			if errors.Is(err, errCredentials) || errors.Is(err, errPermission) {
				s.cfg.Store.W.ExecContext(ctx, `UPDATE cloud_usage_account SET reconnect=1 WHERE id=? AND revision=?`, a.ID, a.Revision)
			}
		} else if err == nil && a.Reconnect && (!a.Billing || s.cfg.Now().Unix()-a.BillAttempt >= int64(6*time.Hour/time.Second)) {
			s.cfg.Store.W.ExecContext(ctx, `UPDATE cloud_usage_account SET reconnect=0 WHERE id=? AND revision=?`, a.ID, a.Revision)
		}
	}
	cut := s.cfg.Now().AddDate(-1, 0, 0).Unix()
	s.cfg.Store.W.ExecContext(ctx, `DELETE FROM cloud_usage_sample WHERE interval_end<?`, cut)
	s.cfg.Store.W.ExecContext(ctx, `DELETE FROM cloud_usage_bill WHERE month<?`, s.cfg.Now().In(billingZone).AddDate(-1, 0, 0).Format("2006-01"))
}

func safeCode(err error) string {
	for _, e := range []error{errCredentials, errPermission, errCloud, errData} {
		if errors.Is(err, e) {
			return e.Error()
		}
	}
	return "cloud_sync"
}

func (s *Service) resources(ctx context.Context, accountID string) ([]resource, error) {
	rows, err := s.cfg.Store.R.QueryContext(ctx, `SELECT r.id,r.account_id,r.region,r.instance_id,r.created_at,r.scanned_until FROM cloud_usage_resource r WHERE account_id=? AND EXISTS(SELECT 1 FROM cloud_usage_binding b WHERE b.resource_id=r.id) ORDER BY r.id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []resource
	for rows.Next() {
		var r resource
		if err = rows.Scan(&r.ID, &r.AccountID, &r.Region, &r.InstanceID, &r.CreatedAt, &r.ScannedUntil); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) current(ctx context.Context, a account) bool {
	var n int
	err := s.cfg.Store.R.QueryRowContext(ctx, `SELECT count(*) FROM cloud_usage_account WHERE id=? AND revision=? AND credentials IS NOT NULL`, a.ID, a.Revision).Scan(&n)
	return err == nil && n == 1
}

func (s *Service) collectAccount(ctx context.Context, a account) error {
	p, ok := s.providers[a.Provider]
	if !ok {
		return errData
	}
	if !s.current(ctx, a) {
		return nil
	}
	c, err := s.open(a)
	if err != nil {
		return err
	}
	resources, err := s.resources(ctx, a.ID)
	if err != nil {
		return err
	}
	var failed error
	for _, r := range resources {
		if err = s.collectResource(ctx, a, c, r); err != nil {
			failed = errors.Join(failed, err)
			if errors.Is(err, errCredentials) || ctx.Err() != nil {
				return err
			}
		}
	}
	if a.Billing && s.cfg.Now().Unix()-a.BillAttempt >= int64(6*time.Hour/time.Second) {
		// Advance the attempt even after refusal; repeated page loads never hammer the bill API.
		s.cfg.Store.W.ExecContext(ctx, `UPDATE cloud_usage_account SET bill_attempt=? WHERE id=? AND revision=?`, s.cfg.Now().Unix(), a.ID, a.Revision)
		month, _, _ := monthBounds("", s.cfg.Now(), providerZone(a.Provider))
		for _, m := range []string{month.Format("2006-01"), month.AddDate(0, -1, 0).Format("2006-01")} {
			if !s.current(ctx, a) {
				return nil
			}
			entries, e := p.Bill(ctx, c, a.AccountID, m)
			if e != nil {
				failed = errors.Join(failed, e)
				if errors.Is(e, errCredentials) {
					return failed
				}
				continue
			}
			b, _ := json.Marshal(entries)
			_, e = s.cfg.Store.W.ExecContext(ctx, `INSERT INTO cloud_usage_bill(account_id,month,fetched_at,entries) SELECT ?,?,?,? WHERE EXISTS(SELECT 1 FROM cloud_usage_account WHERE id=? AND revision=? AND credentials IS NOT NULL) ON CONFLICT(account_id,month) DO UPDATE SET fetched_at=excluded.fetched_at,entries=excluded.entries`, a.ID, m, s.cfg.Now().Unix(), string(b), a.ID, a.Revision)
			if e != nil {
				failed = errors.Join(failed, e)
			}
		}
	}
	return failed
}

func (s *Service) collectResource(ctx context.Context, a account, c credentials, r resource) error {
	p, ok := s.providers[a.Provider]
	if !ok {
		return errData
	}
	now := s.cfg.Now()
	// Leave one interval for the provider to finish. All windows use the same 10-minute grid.
	end := (now.Unix()/samplePeriod - 1) * samplePeriod
	floor := (now.Add(-30*24*time.Hour).Unix()/samplePeriod + 1) * samplePeriod
	start := max(floor, r.CreatedAt/samplePeriod*samplePeriod)
	if r.ScannedUntil > 0 {
		start = max(start, min(r.ScannedUntil, end-48*3600))
	}
	for start < end {
		if !s.current(ctx, a) {
			return nil
		}
		until := min(start+399*samplePeriod, end)
		points, err := p.Usage(ctx, c, r.Region, r.InstanceID, time.Unix(start, 0), time.Unix(until, 0))
		if err != nil {
			return err
		}
		// The remote call is outside the short database transaction.
		tx, err := s.cfg.Store.W.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		var valid int
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM cloud_usage_account WHERE id=? AND revision=? AND credentials IS NOT NULL`, a.ID, a.Revision).Scan(&valid)
		if err != nil || valid == 0 {
			tx.Rollback()
			return err
		}
		for _, p := range points {
			_, err = tx.ExecContext(ctx, `INSERT INTO cloud_usage_sample(resource_id,interval_end,outbound_bytes,inbound_bytes) VALUES(?,?,?,?) ON CONFLICT(resource_id,interval_end) DO UPDATE SET outbound_bytes=excluded.outbound_bytes,inbound_bytes=excluded.inbound_bytes`, r.ID, p.End, p.Bytes, p.Inbound)
			if err != nil {
				break
			}
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE cloud_usage_resource SET scanned_until=max(scanned_until,?) WHERE id=?`, until, r.ID)
		}
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
		if err != nil {
			return err
		}
		start = until
	}
	return nil
}
