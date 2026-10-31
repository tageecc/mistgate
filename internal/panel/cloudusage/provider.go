package cloudusage

import (
	"context"
	"errors"
	"math/big"
	"time"
)

const samplePeriod = int64(600)

var billingZone = time.FixedZone("UTC+8", 8*3600)

type credentials struct {
	KeyID  string
	Secret string
}
type instance struct{ CreatedAt time.Time }
type sample struct {
	End   int64
	Bytes uint64
}
type billEntry struct {
	InstanceID string `json:"instance_id"`
	Region     string `json:"region"`
	Amount     string `json:"amount"`
	Currency   string `json:"currency"`
}

// provider only performs reads. It cannot change cloud resources or node configuration.
type provider interface {
	Identity(context.Context, credentials) (string, error)
	Instance(context.Context, credentials, string, string) (instance, error)
	Usage(context.Context, credentials, string, string, time.Time, time.Time) ([]sample, error)
	Bill(context.Context, credentials, string, string) ([]billEntry, error)
}

var errCredentials = errors.New("cloud_credentials")
var errPermission = errors.New("cloud_permission")
var errCloud = errors.New("cloud_unavailable")
var errData = errors.New("cloud_data")

// Money stays decimal end to end. This also rejects exponents, huge values and NaN without silently rounding them.
func decimal(s string) (*big.Rat, bool) {
	if len(s) == 0 || len(s) > 40 {
		return nil, false
	}
	for i, c := range s {
		if c == '-' && i == 0 {
			continue
		}
		if (c < '0' || c > '9') && c != '.' {
			return nil, false
		}
	}
	v, ok := new(big.Rat).SetString(s)
	return v, ok
}

func monthBounds(month string, now time.Time) (time.Time, time.Time, error) {
	if month == "" {
		month = now.In(billingZone).Format("2006-01")
	}
	start, err := time.ParseInLocation("2006-01", month, billingZone)
	if err != nil || start.After(now) || start.Before(now.In(billingZone).AddDate(-1, -1, 0)) {
		return time.Time{}, time.Time{}, errors.New("month")
	}
	return start, start.AddDate(0, 1, 0), nil
}
