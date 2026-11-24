package main

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/gnolang/gno/gnovm/stdlibs/chain"
	abci "github.com/gnolang/gno/tm2/pkg/bft/abci/types"
	"github.com/gnolang/gno/tm2/pkg/sdk/bank"
)

const denom = "ugnot"

// outcome is what running one job does to the executor's balance, read from
// the events of a simulated or delivered transaction. All amounts are in ugnot.
type outcome struct {
	received int64 // coins sent to the executor: the job's reward
	sent     int64 // coins the executor sends out
	deposit  int64 // storage deposit locked for state the transaction adds
	refund   int64 // storage deposit released to the executor
}

// net is the change in the executor's balance once the gas fee is paid.
func (o outcome) net(fee int64) int64 {
	return o.received - o.sent - o.deposit + o.refund - fee
}

func (o outcome) String() string {
	return fmt.Sprintf("received %d, sent %d, storage deposit %d, refund %d", o.received, o.sent, o.deposit, o.refund)
}

// readOutcome adds up what a transaction's events mean for the address me.
//
// Nothing here trusts the job: the reward is not what the registry advertises
// but what the chain reports was actually transferred, and the storage deposit
// includes state a job's callback added in its own realm, which the sender of
// the transaction pays for too.
func readOutcome(events []abci.Event, me string) outcome {
	var o outcome
	for _, ev := range events {
		switch e := ev.(type) {
		case bank.TransferEvent:
			amount := e.Coins.AmountOf(denom)
			if e.To == me {
				o.received += amount
			}
			if e.From == me {
				o.sent += amount
			}
		case chain.StorageDepositEvent:
			if e.FeeDelta.Denom == denom {
				o.deposit += e.FeeDelta.Amount
			}
		case chain.StorageUnlockEvent:
			if e.FeeRefund.Denom == denom && !e.RefundWithheld {
				o.refund += e.FeeRefund.Amount
			}
		}
	}
	return o
}

// limits are the operator's bounds on a single run.
type limits struct {
	gasCap       int64 // most gas a run may use
	gasMargin    int64 // percent of gas added on top of what the simulation used
	maxDeposit   int64 // most ugnot a run may lock as storage deposit
	depositSlack int64 // ugnot of deposit allowed beyond what the simulation locked
	minProfit    int64 // smallest worst-case gain worth running a job for
}

// plan is what the bot will sign for one job, or why it will not.
type plan struct {
	run        bool
	why        string // set when run is false
	gas        int64
	fee        int64
	maxDeposit int64 // the deposit limit the real transaction carries
	expected   int64 // net gain if the real run matches the simulation
	worst      int64 // net gain if the run locks its whole deposit limit
}

// decide turns a successful simulation into a plan.
//
// The simulation ran against the last committed block. The real transaction
// lands in a later one, and a job's callback is free to behave differently
// there. So the real transaction is not signed with the operator's full
// -max-deposit: it carries only what the simulation locked plus a small slack,
// which turns "the job grew its state at my expense" into a failed
// transaction that costs the fee and nothing else. The decision is made on
// the worst case, with that whole limit spent.
func decide(sim outcome, gasUsed, priceAmount, priceGas int64, lim limits) plan {
	p := plan{gas: withMargin(gasUsed, lim.gasMargin)}
	if p.gas > lim.gasCap {
		p.gas = lim.gasCap
	}
	fee, ok := feeFor(p.gas, priceAmount, priceGas)
	if !ok {
		p.why = "the gas price is unusable"
		return p
	}
	p.fee = fee

	p.maxDeposit = sim.deposit + lim.depositSlack
	if p.maxDeposit > lim.maxDeposit {
		p.maxDeposit = lim.maxDeposit
	}
	if p.maxDeposit < 1 {
		// No limit at all would mean the chain's own, far larger one.
		p.maxDeposit = 1
	}

	p.expected = sim.net(fee)
	// A refund comes from state being freed, and a callback decides whether
	// to free its own. The worst case therefore counts none of it.
	p.worst = sim.received - sim.sent - p.maxDeposit - fee
	if p.worst < lim.minProfit {
		p.why = fmt.Sprintf("not worth it: %s, fee %d, net %d expected and %d at worst (min %d)",
			sim, fee, p.expected, p.worst, lim.minProfit)
		return p
	}
	p.run = true
	return p
}

// feeFor returns the smallest fee, in ugnot, that meets a gas price of
// priceAmount ugnot per priceGas gas for gasWanted gas. It reports false for a
// price it cannot use or a fee that does not fit in an int64.
func feeFor(gasWanted, priceAmount, priceGas int64) (int64, bool) {
	if gasWanted < 0 || priceAmount < 0 || priceGas <= 0 {
		return 0, false
	}
	if priceAmount != 0 && gasWanted > math.MaxInt64/priceAmount {
		return 0, false
	}
	total := gasWanted * priceAmount
	fee := total / priceGas
	if total%priceGas != 0 {
		fee++ // a fee one ugnot short is refused
	}
	return fee, true
}

// withMargin raises gas by percent, so a run that needs slightly more gas in
// its real block than it did in simulation still fits.
func withMargin(gas, percent int64) int64 {
	return gas + gas/100*percent + gas%100*percent/100
}

// dueJob is one entry of the registry's DueJobs answer.
type dueJob struct {
	id     string
	reward int64
	due    int64 // unix time the job became due; with id, the cursor for the next page
}

// parseDueJobs reads the registry's DueJobs answer as qeval prints it:
// ("1:50000:1795480000,7:1000:1795480060" string), each entry being
// id:reward:due. An empty list is ("" string). Anything else is refused
// rather than passed on as a transaction argument.
func parseDueJobs(qeval string) ([]dueJob, error) {
	bad := fmt.Errorf("unexpected DueJobs answer: %s", clean(qeval, 120))
	list, ok := strings.CutPrefix(strings.TrimSpace(qeval), `("`)
	if !ok {
		return nil, bad
	}
	if list, ok = strings.CutSuffix(list, `" string)`); !ok {
		return nil, bad
	}
	if list == "" {
		return nil, nil
	}
	var jobs []dueJob
	for _, item := range strings.Split(list, ",") {
		fields := strings.Split(item, ":")
		if len(fields) != 3 {
			return nil, bad
		}
		id, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil || id == 0 {
			return nil, bad
		}
		reward, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || reward < 0 {
			return nil, bad
		}
		due, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || due < 0 {
			return nil, bad
		}
		jobs = append(jobs, dueJob{id: strconv.FormatUint(id, 10), reward: reward, due: due})
	}
	return jobs, nil
}

var (
	panicText = regexp.MustCompile(`format:"((?:[^"\\]|\\.)*)"`)
	traceText = regexp.MustCompile(`(?m)^\s*0\s+\S+ - (.+)$`)
)

// reason picks the readable cause out of a failed simulation or delivery.
//
// The node describes a failure three ways: a realm's panic arrives as a Go
// dump of an error whose format string is the message, a plain string error
// is the message itself, and anything else is named only in the first line of
// the message trace.
func reason(err error, log string) string {
	if m := panicText.FindStringSubmatch(log); m != nil {
		if text, unquoteErr := strconv.Unquote(`"` + m[1] + `"`); unquoteErr == nil {
			return clean(text, 200)
		}
	}
	if s, ok := err.(abci.StringError); ok && s != "" {
		return clean(string(s), 200)
	}
	if m := traceText.FindStringSubmatch(log); m != nil {
		return clean(m[1], 200)
	}
	if err != nil {
		return clean(strings.TrimPrefix(fmt.Sprintf("%T", err), "std."), 200)
	}
	return "unknown error"
}

// clean makes text that came from the chain safe to print: a job's callback
// chooses its own error message, and a terminal would obey escape codes in it.
func clean(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= max {
			b.WriteString("…")
			break
		}
		if r == '\n' || r == '\t' {
			r = ' '
		}
		if !unicode.IsPrint(r) {
			r = '?'
		}
		b.WriteRune(r)
	}
	return b.String()
}
