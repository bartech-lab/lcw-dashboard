package lcw

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Each method here costs exactly one credit, except Status which costs none.
// The doc comments state the cost because callers budget against it.

// Status checks that the API is up. It needs no key and costs no credit, which
// makes it the right probe for a circuit breaker: retry storms stay free.
func (c *Client) Status(ctx context.Context) error {
	return c.post(ctx, "/status", false, nil, nil)
}

// Credits reports the remaining daily allowance. Costs 1 credit — checking the
// budget spends from it, so do not poll this tightly.
func (c *Client) Credits(ctx context.Context) (Credits, error) {
	var out Credits
	err := c.post(ctx, "/credits", true, nil, &out)
	return out, err
}

// Overview returns market-wide aggregates. Costs 1 credit.
func (c *Client) Overview(ctx context.Context, currency string) (Overview, error) {
	var out Overview
	err := c.post(ctx, "/overview", true, overviewRequest{Currency: currency}, &out)
	return out, err
}

// OverviewHistory returns market-wide aggregates over a time span. Costs 1 credit.
func (c *Client) OverviewHistory(ctx context.Context, currency string, start, end time.Time) ([]OverviewPoint, error) {
	var out []OverviewPoint
	err := c.post(ctx, "/overview/history", true, overviewHistoryRequest{
		Currency: currency,
		Start:    start.UnixMilli(),
		End:      end.UnixMilli(),
	}, &out)
	return out, err
}

// CoinsListParams describes a /coins/list request.
type CoinsListParams struct {
	Currency string
	Sort     SortField
	Order    SortOrder
	Offset   int
	Limit    int
	Meta     bool
}

// CoinsList returns a ranked page of coins. Costs 1 credit regardless of limit,
// which is why the dashboard always asks for a full page of 100.
//
// Limit above MaxListLimit is rejected here rather than silently truncated by
// the API, so a bad config surfaces as an error instead of a short table.
func (c *Client) CoinsList(ctx context.Context, p CoinsListParams) ([]Coin, error) {
	if p.Limit < 1 || p.Limit > MaxListLimit {
		return nil, fmt.Errorf("coins/list limit must be 1..%d, got %d", MaxListLimit, p.Limit)
	}
	if !p.Sort.Valid() {
		return nil, fmt.Errorf("coins/list: invalid sort %q", p.Sort)
	}
	if !p.Order.Valid() {
		return nil, fmt.Errorf("coins/list: invalid order %q", p.Order)
	}
	var out []Coin
	err := c.post(ctx, "/coins/list", true, coinsListRequest{
		Currency: p.Currency,
		Sort:     p.Sort,
		Order:    p.Order,
		Offset:   p.Offset,
		Limit:    p.Limit,
		Meta:     p.Meta,
	}, &out)
	return out, err
}

// CoinsMapParams describes a /coins/map request.
type CoinsMapParams struct {
	Codes    []string
	Currency string
	Sort     SortField
	Order    SortOrder
	Meta     bool
}

// CoinsMap returns exactly the requested coins, ignoring rank entirely. Costs
// 1 credit.
//
// This is how the watchlist works: a coin ranked #731 is as cheap to fetch as
// Bitcoin, and one request covers the whole list. Codes the API does not
// recognise are omitted from the response rather than reported, so callers
// should diff requested against returned to notice a bad code.
//
// Limit is sent as 0, which the API documents as "default to len(codes)".
func (c *Client) CoinsMap(ctx context.Context, p CoinsMapParams) ([]Coin, error) {
	if len(p.Codes) == 0 {
		return nil, fmt.Errorf("coins/map: no codes requested")
	}
	if len(p.Codes) > MaxListLimit {
		return nil, fmt.Errorf("coins/map: %d codes exceeds the %d maximum; chunk the request",
			len(p.Codes), MaxListLimit)
	}
	if !p.Sort.Valid() {
		return nil, fmt.Errorf("coins/map: invalid sort %q", p.Sort)
	}
	if !p.Order.Valid() {
		return nil, fmt.Errorf("coins/map: invalid order %q", p.Order)
	}
	var out []Coin
	err := c.post(ctx, "/coins/map", true, coinsMapRequest{
		Codes:    p.Codes,
		Currency: p.Currency,
		Sort:     p.Sort,
		Order:    p.Order,
		Offset:   0,
		Limit:    0,
		Meta:     p.Meta,
	}, &out)
	return out, err
}

// CoinsSingle returns one coin in detail. Costs 1 credit.
func (c *Client) CoinsSingle(ctx context.Context, currency, code string, meta bool) (Coin, error) {
	var out Coin
	if code == "" {
		return out, fmt.Errorf("coins/single: empty code")
	}
	err := c.post(ctx, "/coins/single", true, coinsSingleRequest{
		Currency: currency,
		Code:     code,
		Meta:     meta,
	}, &out)
	// /coins/single omits the code from its response body, so fill it in to keep
	// the returned value self-describing.
	if err == nil && out.Code == "" {
		out.Code = code
	}
	return out, err
}

// CoinsSingleHistory returns price history for one coin. Costs 1 credit.
//
// There is no bulk equivalent. Reproducing a sparkline column for 100 coins
// across three ranges would cost 300 credits per refresh, which is why the
// dashboard records its own history from the poll loop instead.
func (c *Client) CoinsSingleHistory(ctx context.Context, currency, code string, start, end time.Time, meta bool) (CoinHistory, error) {
	var out CoinHistory
	if code == "" {
		return out, fmt.Errorf("coins/single/history: empty code")
	}
	if !end.After(start) {
		return out, fmt.Errorf("coins/single/history: end %s is not after start %s",
			end.Format(time.RFC3339), start.Format(time.RFC3339))
	}
	err := c.post(ctx, "/coins/single/history", true, coinsHistoryRequest{
		Currency: currency,
		Code:     code,
		Start:    start.UnixMilli(),
		End:      end.UnixMilli(),
		Meta:     meta,
	}, &out)
	if err == nil && out.Code == "" {
		out.Code = code
	}
	return out, err
}

// FiatsAll lists every supported fiat currency. Costs 1 credit, and the result
// changes rarely, so it is cached to disk for weeks.
func (c *Client) FiatsAll(ctx context.Context) ([]Fiat, error) {
	var out []Fiat
	err := c.post(ctx, "/fiats/all", true, nil, &out)
	return out, err
}

// NormalizeCode upper-cases and trims a coin code. The API is case-sensitive
// and expects upper case; a lower-case code returns no data rather than an
// error, which is a confusing way to see an empty watchlist.
func NormalizeCode(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }
