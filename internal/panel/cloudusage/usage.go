package cloudusage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

func (s *Service) GetCloudUsage(ctx context.Context, req *connect.Request[adminv1.GetCloudUsageRequest]) (*connect.Response[adminv1.GetCloudUsageResponse], error) {
	if err := s.guard(ctx, false); err != nil {
		return nil, err
	}
	start, end, err := monthBounds(req.Msg.Month, s.cfg.Now())
	if err != nil {
		return nil, invalid()
	}
	out := &adminv1.GetCloudUsageResponse{Month: start.Format("2006-01")}
	var id string
	var created int64
	r := &adminv1.CloudResource{}
	err = s.cfg.Store.R.QueryRowContext(ctx, `SELECT r.id,r.account_id,r.region,r.instance_id,r.created_at,b.threshold_bytes,b.threshold_amount,b.threshold_currency,a.name FROM cloud_usage_binding b JOIN cloud_usage_resource r ON r.id=b.resource_id JOIN cloud_usage_account a ON a.id=r.account_id WHERE b.node_id=?`, req.Msg.NodeId).Scan(&id, &r.AccountId, &r.Region, &r.InstanceId, &created, &r.ThresholdBytes, &r.ThresholdAmount, &r.ThresholdCurrency, &out.AccountName)
	if errors.Is(err, sql.ErrNoRows) {
		return connect.NewResponse(out), nil
	}
	if err != nil {
		return nil, failure(err)
	}
	out.Resource = r
	rows, err := s.cfg.Store.R.QueryContext(ctx, `SELECT interval_end,outbound_bytes FROM cloud_usage_sample WHERE resource_id=? AND interval_end>? AND interval_end<=? ORDER BY interval_end`, id, start.Unix(), end.Unix())
	if err != nil {
		return nil, failure(err)
	}
	counts := map[string]int64{}
	days := map[string]*adminv1.CloudUsageDay{}
	var count int64
	for rows.Next() {
		var t int64
		var bytes uint64
		if err = rows.Scan(&t, &bytes); err != nil {
			break
		}
		date := time.Unix(t-1, 0).In(billingZone).Format("2006-01-02")
		if days[date] == nil {
			days[date] = &adminv1.CloudUsageDay{Date: date, HasUsage: true}
		}
		days[date].OutboundBytes += bytes
		counts[date]++
		count++
		out.OutboundBytes += bytes
		out.HasUsage = true
		if out.ObservedFromUnix == 0 {
			out.ObservedFromUnix = t - samplePeriod
		}
		out.ObservedThroughUnix = t
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, failure(err)
	}
	expectedStart := max(start.Unix(), created/samplePeriod*samplePeriod)
	expectedEnd := min(end.Unix(), (s.cfg.Now().Unix()/samplePeriod-1)*samplePeriod)
	if out.HasUsage {
		out.Partial = count != max(int64(0), (expectedEnd-expectedStart)/samplePeriod)
	}
	for d := start; d.Before(end) && d.Before(s.cfg.Now()); d = d.AddDate(0, 0, 1) {
		date := d.Format("2006-01-02")
		point := days[date]
		if point == nil {
			point = &adminv1.CloudUsageDay{Date: date}
		}
		until := min(d.AddDate(0, 0, 1).Unix(), expectedEnd)
		expected := max(int64(0), (until-max(d.Unix(), expectedStart))/samplePeriod)
		point.Partial = point.HasUsage && counts[date] != expected
		out.Days = append(out.Days, point)
	}
	out.ThresholdReached = r.ThresholdBytes > 0 && out.OutboundBytes >= r.ThresholdBytes
	var bills string
	err = s.cfg.Store.R.QueryRowContext(ctx, `SELECT entries,fetched_at FROM cloud_usage_bill WHERE account_id=? AND month=?`, r.AccountId, out.Month).Scan(&bills, &out.BilledThroughUnix)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, failure(err)
	}
	if err == nil {
		var entries []billEntry
		if json.Unmarshal([]byte(bills), &entries) != nil {
			return nil, failure(errData)
		}
		total := new(big.Rat)
		for _, e := range entries {
			if e.InstanceID != r.InstanceId || (e.Region != "" && e.Region != r.Region) {
				continue
			}
			v, ok := decimal(e.Amount)
			if !ok {
				return nil, failure(errData)
			}
			if out.Currency != "" && out.Currency != e.Currency {
				return nil, failure(errData)
			}
			out.Currency = e.Currency
			out.HasCost = true
			total.Add(total, v)
		}
		if out.HasCost {
			if threshold, ok := decimal(r.ThresholdAmount); ok && threshold.Sign() > 0 && r.ThresholdCurrency == out.Currency && total.Cmp(threshold) >= 0 {
				out.ThresholdReached = true
			}
			out.BilledAmount = strings.TrimRight(strings.TrimRight(total.FloatString(40), "0"), ".")
			if out.BilledAmount == "-0" {
				out.BilledAmount = "0"
			}
		}
	}
	return connect.NewResponse(out), nil
}
