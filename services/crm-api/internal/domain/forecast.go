package domain

import (
	"context"
	"errors"
	"fmt"
)

// Weighted pipeline forecast (B2 — docs/customer-os/PLAN.md). Nothing here is
// persisted: PipelineForecast is a pure read-and-compute rollup, run fresh
// against the store on every call.
//
// "Owner" is the Opportunity's Account's owner (Account.OwnerID):
// Opportunity carries no owner field of its own in this schema/proto, and
// this task is scoped to Opportunity's own business rules, not to adding a
// field to Account or to the wire contract. Reading Account.OwnerID through
// the same account_id referential-integrity link Create/Update already
// enforce (transitions.go) is a read, not a change to Account's rules.

// StageProbability maps an open Opportunity.Stage to the weight
// PipelineForecast applies to its Amount. This is a documented, configurable
// DEFAULT, not invented business policy: a deployment with a real forecasting
// methodology supplies its own map via ForecastOptions.Probabilities instead
// of editing DefaultStageProbabilities.
type StageProbability map[string]float64

// DefaultStageProbabilities is the default weighting for open stages. Closed
// stages are intentionally absent — PipelineForecast handles closed_won
// (full amount) and closed_lost (zero) as special cases, not via this map.
var DefaultStageProbabilities = StageProbability{
	StageProspecting:   0.10,
	StageQualification: 0.25,
	StageProposal:      0.50,
	StageNegotiation:   0.75,
}

// ForecastOptions configures one PipelineForecast call.
type ForecastOptions struct {
	// Probabilities overrides DefaultStageProbabilities when non-nil. A stage
	// missing from this map weights at zero, same as an unrecognized stage.
	Probabilities StageProbability
}

// ForecastBucket is one (owner, stage) cell of the pipeline rollup.
type ForecastBucket struct {
	OwnerID  string
	Stage    string
	Count    int
	Amount   float64 // raw, unweighted sum — for reference alongside Weighted.
	Weighted float64
}

// Forecast is the full weighted-pipeline rollup for one tenant.
type Forecast struct {
	ByOwner map[string]float64
	ByStage map[string]float64
	Total   float64
	Buckets []*ForecastBucket
}

// forecastReader is what PipelineForecast needs from a Store: every
// Opportunity in the tenant, and the owner behind each one's Account.
type forecastReader interface {
	ListOpportunities(ctx context.Context, scope Scope, page Page) ([]*Opportunity, string, error)
	GetAccount(ctx context.Context, scope Scope, id string) (*Account, error)
}

// weightFor returns the value one Opportunity contributes to the forecast:
//
//   - closed_won counts at full Amount — the deal is won, not a probability.
//   - closed_lost counts at zero — it will never close, but still appears in
//     the rollup (at zero) so a bucket does not silently disappear the moment
//     a deal is lost.
//   - every open stage counts at Amount * probs[stage]; a stage missing from
//     probs (including any the caller has not configured) weights at zero
//     rather than panicking or guessing.
func weightFor(o *Opportunity, probs StageProbability) float64 {
	switch o.Stage {
	case StageClosedWon:
		return o.Amount
	case StageClosedLost:
		return 0
	default:
		return o.Amount * probs[o.Stage]
	}
}

// PipelineForecast rolls up every Opportunity in scope's tenant into a
// weighted forecast, grouped by owner and by stage. It is computed fresh on
// every call from Store reads — nothing about the result is persisted.
func PipelineForecast(ctx context.Context, store forecastReader, scope Scope, opts ForecastOptions) (*Forecast, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	probs := opts.Probabilities
	if probs == nil {
		probs = DefaultStageProbabilities
	}

	opps, err := allOpportunitiesForForecast(ctx, store, scope)
	if err != nil {
		return nil, err
	}

	f := &Forecast{ByOwner: map[string]float64{}, ByStage: map[string]float64{}}
	buckets := map[[2]string]*ForecastBucket{}
	ownerCache := map[string]string{}

	for _, o := range opps {
		owner, err := ownerOfAccount(ctx, store, scope, ownerCache, o.AccountID)
		if err != nil {
			return nil, err
		}

		weighted := weightFor(o, probs)

		key := [2]string{owner, o.Stage}
		b, ok := buckets[key]
		if !ok {
			b = &ForecastBucket{OwnerID: owner, Stage: o.Stage}
			buckets[key] = b
			f.Buckets = append(f.Buckets, b)
		}
		b.Count++
		b.Amount += o.Amount
		b.Weighted += weighted

		f.ByOwner[owner] += weighted
		f.ByStage[o.Stage] += weighted
		f.Total += weighted
	}

	return f, nil
}

// ownerOfAccount resolves the OwnerID of the Account an Opportunity names,
// memoized across one PipelineForecast call so an owner with many
// opportunities against the same account triggers one lookup, not N. A
// dangling account_id (should not exist thanks to the referential-integrity
// check on write, but the forecast does not assume the store's history was
// always written under today's rules) resolves to an empty owner rather than
// failing the whole forecast.
func ownerOfAccount(
	ctx context.Context, store accountReader, scope Scope, cache map[string]string, accountID string,
) (string, error) {
	if owner, ok := cache[accountID]; ok {
		return owner, nil
	}
	acct, err := store.GetAccount(ctx, scope, accountID)
	if errors.Is(err, ErrNotFound) {
		cache[accountID] = ""
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve owner for account %q: %w", accountID, err)
	}
	cache[accountID] = acct.OwnerID
	return acct.OwnerID, nil
}

// allOpportunitiesForForecast pages through every Opportunity in the tenant.
// Forecast is a whole-tenant rollup, so it reads every page rather than
// exposing pagination to the caller.
func allOpportunitiesForForecast(ctx context.Context, store forecastReader, scope Scope) ([]*Opportunity, error) {
	var out []*Opportunity
	token := ""
	for {
		page, next, err := store.ListOpportunities(ctx, scope, Page{Size: maxPageSize, Token: token})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if next == "" {
			return out, nil
		}
		token = next
	}
}
