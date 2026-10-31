//go:build !js

package cloudusage

import (
	"context"
	"errors"
	"math"
	"net/http"
	"regexp"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	"github.com/aws/aws-sdk-go-v2/service/lightsail/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

var awsAccountID = regexp.MustCompile(`^[0-9]{12}$`)

type awsLightsail struct{ http *http.Client }

func newLightsail() *awsLightsail {
	return &awsLightsail{http: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (a *awsLightsail) config(c credentials, region string) aws.Config {
	// Never load ambient process credentials, profiles or user-supplied endpoints.
	return aws.Config{Region: region, Credentials: awscreds.NewStaticCredentialsProvider(c.KeyID, c.Secret, ""), HTTPClient: a.http, RetryMaxAttempts: 2}
}

func awsFailure(err error) error {
	var e smithy.APIError
	if errors.As(err, &e) {
		switch e.ErrorCode() {
		case "InvalidClientTokenId", "InvalidAccessKeyId", "UnrecognizedClientException", "SignatureDoesNotMatch", "InvalidSignatureException", "ExpiredToken":
			return errCredentials
		case "AccessDenied", "AccessDeniedException", "UnauthorizedException":
			return errPermission
		}
	}
	return errCloud
}

func (a *awsLightsail) Identity(ctx context.Context, c credentials) (string, error) {
	v, err := sts.NewFromConfig(a.config(c, "us-east-1")).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", awsFailure(err)
	}
	id := aws.ToString(v.Account)
	if !awsAccountID.MatchString(id) {
		return "", errData
	}
	return id, nil
}

func (a *awsLightsail) Instance(ctx context.Context, c credentials, region, name string) (instance, error) {
	if !regionPattern.MatchString(region) || !lightsailName.MatchString(name) {
		return instance{}, errData
	}
	v, err := lightsail.NewFromConfig(a.config(c, region)).GetInstance(ctx, &lightsail.GetInstanceInput{InstanceName: aws.String(name)})
	if err != nil {
		return instance{}, awsFailure(err)
	}
	i := v.Instance
	if i == nil || aws.ToString(i.Name) != name || i.CreatedAt == nil || i.Location == nil || string(i.Location.RegionName) != region || i.Networking == nil || i.Networking.MonthlyTransfer == nil {
		return instance{}, errData
	}
	gb := aws.ToInt32(i.Networking.MonthlyTransfer.GbPerMonthAllocated)
	if gb <= 0 {
		return instance{}, errData
	}
	return instance{CreatedAt: *i.CreatedAt, AllowanceGB: int64(gb)}, nil
}

func (a *awsLightsail) Usage(ctx context.Context, c credentials, region, name string, start, end time.Time) ([]sample, error) {
	if !regionPattern.MatchString(region) || !lightsailName.MatchString(name) {
		return nil, errData
	}
	client := lightsail.NewFromConfig(a.config(c, region))
	series := make([]map[int64]uint64, 2)
	for n, metric := range []types.InstanceMetricName{types.InstanceMetricNameNetworkOut, types.InstanceMetricNameNetworkIn} {
		v, err := client.GetInstanceMetricData(ctx, &lightsail.GetInstanceMetricDataInput{InstanceName: aws.String(name), MetricName: metric, StartTime: &start, EndTime: &end, Period: aws.Int32(300), Unit: types.MetricUnitBytes, Statistics: []types.MetricStatistic{types.MetricStatisticSum}})
		if err != nil {
			return nil, awsFailure(err)
		}
		if v.MetricName != metric {
			return nil, errData
		}
		series[n] = map[int64]uint64{}
		for _, p := range v.MetricData {
			if p.Timestamp == nil || p.Sum == nil || p.Unit != types.MetricUnitBytes {
				return nil, errData
			}
			ts, b := p.Timestamp.Unix(), *p.Sum
			if ts < start.Unix() || ts >= end.Unix() {
				continue
			}
			if ts%300 != 0 || math.IsNaN(b) || math.IsInf(b, 0) || b < 0 || b > 1<<50 || math.Trunc(b) != b {
				return nil, errData
			}
			if old, ok := series[n][ts]; ok && old != uint64(b) {
				return nil, errData
			}
			series[n][ts] = uint64(b)
		}
	}
	// AWS timestamps start a five-minute interval. Only commit complete pairs in both directions;
	// missing points stay gaps, and adjacent requests never count a boundary twice.
	var out []sample
	for ts, first := range series[0] {
		if ts%samplePeriod != 0 {
			continue
		}
		second, okOut := series[0][ts+300]
		inFirst, okInFirst := series[1][ts]
		inSecond, okInSecond := series[1][ts+300]
		if !okOut || !okInFirst || !okInSecond {
			continue
		}
		inbound := inFirst + inSecond
		out = append(out, sample{End: ts + samplePeriod, Bytes: first + second, Inbound: &inbound})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].End < out[j].End })
	return out, nil
}

func (*awsLightsail) Bill(context.Context, credentials, string, string) ([]billEntry, error) {
	// Lightsail monitoring is not a bill. GetCostEstimate is a forecast API for Lightsail for Research.
	return nil, errData
}
