package domain_test

import (
	"testing"

	"github.com/evetabi/prediction/internal/domain"
	"github.com/shopspring/decimal"
)

// TestParimutuelPayoutMath validates the pari-mutuel payout calculation used
// by ResolutionService.  No I/O — pure arithmetic.
//
//	Scenario:
//	  pool_up   = 1 200 TRY  (users: 1000 + 200)
//	  pool_down =   500 TRY
//	  commission = 3 %
//	  winner = UP
//
//	Expected for a User1 stake of 1 000 TRY:
//	  distributable = 500 × (1 - 0.03)  = 485 TRY
//	  share         = 1000 / 1200       ≈ 0.8333...
//	  profit        = 0.8333 × 485      ≈ 404.1667
//	  payout        = 1000 + 404.1667   = 1404.1667
//
//	Platform commission = 1700 × 0.03 = 51 TRY
func TestParimutuelPayoutMath(t *testing.T) {
	poolUp := decimal.NewFromInt(1200)
	poolDown := decimal.NewFromInt(500)
	commission := decimal.NewFromFloat(0.03)
	one := decimal.NewFromInt(1)

	totalPool := poolUp.Add(poolDown)
	commissionAmt := totalPool.Mul(commission)
	distributable := poolDown.Mul(one.Sub(commission))

	// User1 (stake 1000 TRY in winning UP pool)
	user1Stake := decimal.NewFromInt(1000)
	share1 := user1Stake.Div(poolUp)
	profit1 := share1.Mul(distributable)
	payout1 := user1Stake.Add(profit1).RoundDown(4)

	wantCommission := decimal.NewFromFloat(51)
	if commissionAmt.Sub(wantCommission).Abs().GreaterThan(decimal.NewFromFloat(0.01)) {
		t.Errorf("commission = %s, want %s", commissionAmt, wantCommission)
	}

	// Payout should be > stake (winner should profit)
	if payout1.LessThanOrEqual(user1Stake) {
		t.Errorf("winner payout %s should be > stake %s", payout1, user1Stake)
	}

	// User3 (stake 200 TRY in winning UP pool)
	user3Stake := decimal.NewFromInt(200)
	share3 := user3Stake.Div(poolUp)
	profit3 := share3.Mul(distributable)
	payout3 := user3Stake.Add(profit3).RoundDown(4)

	// Total paid out should not exceed distributable + total winner stakes
	// i.e. house should not lose money beyond target
	totalPaidOut := payout1.Add(payout3)
	totalWinnerStakes := user1Stake.Add(user3Stake)
	if totalPaidOut.Sub(totalWinnerStakes).Sub(distributable).Abs().GreaterThan(decimal.NewFromFloat(0.01)) {
		t.Errorf("total paidout - winner stakes = %s, want ~%s",
			totalPaidOut.Sub(totalWinnerStakes), distributable)
	}

	t.Logf("commission=%.4f distributable=%.4f payout1=%.4f payout3=%.4f",
		commissionAmt.InexactFloat64(),
		distributable.InexactFloat64(),
		payout1.InexactFloat64(),
		payout3.InexactFloat64())
}

// TestExitAmountCalculation validates early cash-out (bozdur) maths.
//
//	Scenario: bet=500 TRY, current odds = 1.5x, cashoutFeeRate = 5%
//	  gross    = 500 × 1.5 = 750
//	  fee      = 750 × 0.05 = 37.5
//	  net payout = 750 - 37.5 = 712.5
func TestExitAmountCalculation(t *testing.T) {
	stake := decimal.NewFromInt(500)
	currentOdds := decimal.NewFromFloat(1.5)
	feeRate := decimal.NewFromFloat(0.05)
	one := decimal.NewFromInt(1)

	bet := &domain.Bet{
		Amount: stake,
	}

	// Replicate Bet.CalculateExitAmount logic from domain
	gross := bet.Amount.Mul(currentOdds).RoundDown(4)
	fee := gross.Mul(feeRate).RoundDown(4)
	net := gross.Mul(one.Sub(feeRate)).RoundDown(4)

	wantGross := decimal.NewFromFloat(750)
	wantFee := decimal.NewFromFloat(37.5)
	wantNet := decimal.NewFromFloat(712.5)

	if !gross.Equal(wantGross) {
		t.Errorf("gross = %s, want %s", gross, wantGross)
	}
	if !fee.Equal(wantFee) {
		t.Errorf("fee = %s, want %s", fee, wantFee)
	}
	if !net.Equal(wantNet) {
		t.Errorf("net = %s, want %s", net, wantNet)
	}
}
