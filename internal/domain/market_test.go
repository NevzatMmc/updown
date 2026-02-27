package domain_test

import (
	"testing"
	"time"

	"github.com/evetabi/prediction/internal/domain"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ── Market pool math ──────────────────────────────────────────────────────────

func TestMarket_TotalPool(t *testing.T) {
	m := &domain.Market{
		PoolUp:   decimal.NewFromInt(1000),
		PoolDown: decimal.NewFromInt(500),
	}
	want := decimal.NewFromInt(1500)
	if !m.TotalPool().Equal(want) {
		t.Errorf("TotalPool() = %s, want %s", m.TotalPool(), want)
	}
}

func TestMarket_UpPercent_DownPercent(t *testing.T) {
	m := &domain.Market{
		PoolUp:   decimal.NewFromInt(1000),
		PoolDown: decimal.NewFromInt(500),
	}
	up := m.UpPercent()
	down := m.DownPercent()

	hunderd := decimal.NewFromInt(100)
	if up.Add(down).Round(4).Equal(hunderd) == false {
		t.Errorf("UpPercent + DownPercent should sum to 100, got %s + %s = %s",
			up, down, up.Add(down))
	}
	// UP should be ~66.67%
	wantUp := decimal.NewFromFloat(66.67)
	if up.Sub(wantUp).Abs().GreaterThan(decimal.NewFromFloat(0.01)) {
		t.Errorf("UpPercent() = %s, want ~%s", up, wantUp)
	}
}

func TestMarket_EmptyPool_UpPercent(t *testing.T) {
	m := &domain.Market{
		PoolUp:   decimal.Zero,
		PoolDown: decimal.Zero,
	}
	// Should not panic / divide by zero
	up := m.UpPercent()
	down := m.DownPercent()
	if up.IsNegative() || down.IsNegative() {
		t.Errorf("empty pool percents should not be negative: up=%s down=%s", up, down)
	}
}

func TestMarket_OddsFor(t *testing.T) {
	m := &domain.Market{
		PoolUp:   decimal.NewFromInt(1000),
		PoolDown: decimal.NewFromInt(500),
	}
	// effectivePool = (1000+500) × (1 - 0.03) = 1455
	// UP odds   = 1455 / 1000 = 1.455
	// DOWN odds = 1455 / 500  = 2.91
	commission := decimal.NewFromFloat(0.03)
	one := decimal.NewFromInt(1)
	effective := m.TotalPool().Mul(one.Sub(commission))

	wantUp := effective.Div(decimal.NewFromInt(1000))
	wantDown := effective.Div(decimal.NewFromInt(500))

	upOdds := m.OddsFor(domain.OutcomeUp)
	if upOdds.Sub(wantUp).Abs().GreaterThan(decimal.NewFromFloat(0.001)) {
		t.Errorf("OddsFor(UP) = %s, want %s", upOdds, wantUp)
	}
	downOdds := m.OddsFor(domain.OutcomeDown)
	if downOdds.Sub(wantDown).Abs().GreaterThan(decimal.NewFromFloat(0.001)) {
		t.Errorf("OddsFor(DOWN) = %s, want %s", downOdds, wantDown)
	}
	// Winners always get odds > 1
	if upOdds.LessThanOrEqual(decimal.NewFromInt(1)) {
		t.Errorf("UP odds should be > 1.0, got %s", upOdds)
	}
}

func TestMarket_IsOpen(t *testing.T) {
	now := time.Now().UTC()
	m := &domain.Market{
		Status:   domain.StatusOpen,
		OpensAt:  now.Add(-2 * time.Minute),
		ClosesAt: now.Add(3 * time.Minute),
	}
	if !m.IsOpen() {
		t.Error("expected market to be open")
	}
	m.Status = domain.StatusResolved
	if m.IsOpen() {
		t.Error("resolved market should not be open")
	}
}

func TestMarket_TimeLeft(t *testing.T) {
	now := time.Now().UTC()
	m := &domain.Market{
		ClosesAt: now.Add(2 * time.Minute),
	}
	tl := m.TimeLeft()
	if tl <= 0 || tl > 2*time.Minute+time.Second {
		t.Errorf("TimeLeft() = %v, expected ~2m0s", tl)
	}
}

// ── Outcome validity ──────────────────────────────────────────────────────────

func TestOutcome_IsValid(t *testing.T) {
	if !domain.OutcomeUp.IsValid() {
		t.Error("OutcomeUp should be valid")
	}
	if !domain.OutcomeDown.IsValid() {
		t.Error("OutcomeDown should be valid")
	}
	if domain.Outcome("SIDEWAYS").IsValid() {
		t.Error("SIDEWAYS should not be valid")
	}
}

// ── Bet helpers ───────────────────────────────────────────────────────────────

func TestBet_IsActive(t *testing.T) {
	b := &domain.Bet{
		ID:     uuid.New(),
		Status: domain.BetStatusActive,
	}
	if !b.IsActive() {
		t.Error("bet with BetStatusActive should be active")
	}
	b.Status = domain.BetStatusWon
	if b.IsActive() {
		t.Error("won bet should not be active")
	}
}

// ── Wallet available ──────────────────────────────────────────────────────────

func TestWallet_Available(t *testing.T) {
	w := &domain.Wallet{
		Balance: decimal.NewFromInt(1000),
		Locked:  decimal.NewFromInt(300),
	}
	want := decimal.NewFromInt(700)
	if !w.Available().Equal(want) {
		t.Errorf("Available() = %s, want %s", w.Available(), want)
	}
}
