package cloudusage

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // Alibaba Cloud RPC signature v1, required by these APIs.
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The endpoints are fixed; no cloud credential can be sent to a user-supplied URL.
type aliyun struct{ http *http.Client }

func newAliyun() *aliyun {
	return &aliyun{http: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func rpcEncode(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }

// RPC signing: https://www.alibabacloud.com/help/en/sdk/product-overview/rpc-mechanism
func signRPC(p url.Values, secret string) string {
	canonical := strings.ReplaceAll(p.Encode(), "+", "%20")
	h := hmac.New(sha1.New, []byte(secret+"&"))
	h.Write([]byte("POST&%2F&" + rpcEncode(canonical)))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func (a *aliyun) call(ctx context.Context, c credentials, endpoint, version, action string, p url.Values, out any) error {
	if p == nil {
		p = url.Values{}
	}
	var nonce [16]byte
	rand.Read(nonce[:])
	p.Set("Action", action)
	p.Set("Version", version)
	p.Set("Format", "JSON")
	p.Set("AccessKeyId", c.KeyID)
	p.Set("SignatureMethod", "HMAC-SHA1")
	p.Set("SignatureVersion", "1.0")
	p.Set("SignatureNonce", hex.EncodeToString(nonce[:]))
	p.Set("Timestamp", time.Now().UTC().Format(time.RFC3339))
	p.Set("Signature", signRPC(p, c.Secret))
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+endpoint+"/", strings.NewReader(p.Encode()))
	if err != nil {
		return errCloud
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.http.Do(r)
	if err != nil {
		return errCloud
	} // Never expose request URLs, provider messages or credentials.
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
	if err != nil || len(b) > 8<<20 {
		return errCloud
	}
	var status struct {
		Code    string
		Success *bool
	}
	if json.Unmarshal(b, &status) != nil {
		return errData
	}
	if resp.StatusCode >= 300 || (status.Code != "" && status.Code != "Success" && status.Code != "200") || (status.Success != nil && !*status.Success) {
		switch {
		case strings.Contains(status.Code, "InvalidAccessKeyId"), strings.Contains(status.Code, "SignatureDoesNotMatch"), strings.Contains(status.Code, "InvalidSecurityToken"), strings.Contains(status.Code, "Forbidden.RAMUser.Disabled"):
			return errCredentials
		case resp.StatusCode == 403, strings.HasPrefix(status.Code, "Forbidden"), strings.Contains(status.Code, "NoPermission"), strings.Contains(status.Code, "Unauthorized"):
			return errPermission
		default:
			return errCloud
		}
	}
	if err := json.Unmarshal(b, out); err != nil {
		return errData
	}
	return nil
}

func (a *aliyun) Identity(ctx context.Context, c credentials) (string, error) {
	var v struct {
		AccountID string `json:"AccountId"`
	}
	if err := a.call(ctx, c, "sts.aliyuncs.com", "2015-04-01", "GetCallerIdentity", nil, &v); err != nil {
		return "", err
	}
	if !regexp.MustCompile(`^[0-9]{6,24}$`).MatchString(v.AccountID) {
		return "", errData
	}
	return v.AccountID, nil
}

var regionPattern = regexp.MustCompile(`^[a-z]{2}-[a-z]+(?:-[a-z0-9]+)*$`)
var instancePattern = regexp.MustCompile(`^i-[a-zA-Z0-9]{5,40}$`)

func (a *aliyun) Instance(ctx context.Context, c credentials, region, id string) (instance, error) {
	var v struct {
		Instances struct {
			Instance []struct {
				InstanceID   string `json:"InstanceId"`
				CreationTime string
				PublicIP     struct {
					IP []string `json:"IpAddress"`
				} `json:"PublicIpAddress"`
				EIP struct {
					IP string `json:"IpAddress"`
				} `json:"EipAddress"`
			}
		}
	}
	ids, _ := json.Marshal([]string{id})
	err := a.call(ctx, c, "ecs."+region+".aliyuncs.com", "2014-05-26", "DescribeInstances", url.Values{"RegionId": {region}, "InstanceIds": {string(ids)}}, &v)
	if err != nil {
		return instance{}, err
	}
	if len(v.Instances.Instance) != 1 || v.Instances.Instance[0].InstanceID != id {
		return instance{}, errData
	}
	i := v.Instances.Instance[0]
	// EIP monitoring and billing belong to an EIP resource, not this ECS public-IP integration.
	if i.EIP.IP != "" || len(i.PublicIP.IP) == 0 {
		return instance{}, errData
	}
	created, err := time.Parse(time.RFC3339, i.CreationTime)
	if err != nil {
		created, err = time.Parse("2006-01-02T15:04Z", i.CreationTime)
	}
	if err != nil {
		return instance{}, errData
	}
	return instance{CreatedAt: created}, nil
}

func (a *aliyun) Usage(ctx context.Context, c credentials, region, id string, start, end time.Time) ([]sample, error) {
	var v struct {
		MonitorData *struct {
			Items []struct {
				InstanceID string `json:"InstanceId"`
				Timestamp  string `json:"TimeStamp"`
				InternetTX *int64
			} `json:"InstanceMonitorData"`
		}
	}
	err := a.call(ctx, c, "ecs."+region+".aliyuncs.com", "2014-05-26", "DescribeInstanceMonitorData", url.Values{
		"RegionId": {region}, "InstanceId": {id}, "Period": {strconv.FormatInt(samplePeriod, 10)},
		"StartTime": {start.UTC().Format(time.RFC3339)}, "EndTime": {end.UTC().Format(time.RFC3339)},
	}, &v)
	if err != nil {
		return nil, err
	}
	if v.MonitorData == nil || v.MonitorData.Items == nil || len(v.MonitorData.Items) > 400 {
		return nil, errData
	}
	var out []sample
	for _, p := range v.MonitorData.Items {
		t, err := time.Parse(time.RFC3339, p.Timestamp)
		if err != nil || p.InstanceID != id || p.InternetTX == nil || *p.InternetTX < 0 || *p.InternetTX > math.MaxInt64/125 {
			return nil, errData
		}
		// InternetTX is an interval total in kbits (not kbit/s). Store bytes once, never multiply by Period.
		if t.Unix()%samplePeriod != 0 {
			return nil, errData
		}
		if t.After(start) && !t.After(end) {
			out = append(out, sample{End: t.Unix(), Bytes: uint64(*p.InternetTX) * 125})
		}
	}
	return out, nil
}

func (a *aliyun) Bill(ctx context.Context, c credentials, accountID, month string) ([]billEntry, error) {
	var out []billEntry
	next := ""
	seen := map[string]bool{}
	for page := 0; page < 1000; page++ {
		var v struct {
			Data struct {
				AccountID    string
				BillingCycle string
				NextToken    string
				Items        []struct {
					InstanceID, OwnerID, BillAccountID, Region, PipCode, CommodityCode, BillingItemCode, Currency string
					PretaxAmount                                                                                  json.Number
				}
			}
		}
		err := a.call(ctx, c, "business.aliyuncs.com", "2017-12-14", "DescribeInstanceBill", url.Values{
			"BillingCycle": {month}, "Granularity": {"MONTHLY"}, "ProductCode": {"cdt"}, "PipCode": {"cdt"}, "IsBillingItem": {"true"}, "IsHideZeroCharge": {"false"}, "MaxResults": {"300"}, "NextToken": {next},
		}, &v)
		if err != nil {
			return nil, err
		}
		if v.Data.AccountID != accountID || v.Data.BillingCycle != month || v.Data.Items == nil {
			return nil, errData
		}
		for _, i := range v.Data.Items {
			if i.OwnerID != "" && i.OwnerID != accountID {
				continue
			}
			if i.PipCode != "cdt" {
				return nil, errData
			}
			parts := strings.Split(i.InstanceID, ";")
			id := parts[0]
			region := ""
			if len(parts) > 1 {
				region = parts[1]
			}
			if !instancePattern.MatchString(id) {
				continue
			}
			// Refuse an unfamiliar ECS billing layout instead of publishing a partial total.
			if i.BillingItemCode != "ECS_Out_Bytes" && i.BillingItemCode != "internet_traffic" {
				return nil, errData
			}
			if _, ok := decimal(string(i.PretaxAmount)); !ok || !regexp.MustCompile(`^[A-Z]{3}$`).MatchString(i.Currency) {
				return nil, errData
			}
			out = append(out, billEntry{InstanceID: id, Region: region, Amount: string(i.PretaxAmount), Currency: i.Currency})
		}
		next = v.Data.NextToken
		if next == "" {
			return out, nil
		}
		if seen[next] {
			return nil, errData
		}
		seen[next] = true
	}
	return nil, errData
}
