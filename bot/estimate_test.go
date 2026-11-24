package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gnolang/gno/gnovm/stdlibs/chain"
	abci "github.com/gnolang/gno/tm2/pkg/bft/abci/types"
	"github.com/gnolang/gno/tm2/pkg/sdk/bank"
	"github.com/gnolang/gno/tm2/pkg/std"
)

const (
	me       = "g1me"
	registry = "g1registry"
)

func ugnot(n int64) std.Coins { return std.Coins{std.NewCoin(denom, n)} }

func TestReadOutcome(t *testing.T) {
	events := []abci.Event{
		bank.TransferEvent{From: registry, To: me, Coins: ugnot(50000)},
		// Someone else's transfer in the same transaction is not ours.
		bank.TransferEvent{From: registry, To: "g1other", Coins: ugnot(777)},
		// Another denomination is not counted as reward.
		bank.TransferEvent{From: registry, To: me, Coins: std.Coins{std.NewCoin("other", 9)}},
		chain.Event{Type: "JobExecuted", PkgPath: "gno.land/r/x/registry"},
		// The sender pays deposits in every realm the transaction touched.
		chain.StorageDepositEvent{BytesDelta: 20, FeeDelta: std.NewCoin(denom, 2000), PkgPath: "gno.land/r/x/registry"},
		chain.StorageDepositEvent{BytesDelta: 5, FeeDelta: std.NewCoin(denom, 500), PkgPath: "gno.land/r/x/taproom"},
		chain.StorageUnlockEvent{BytesDelta: -3, FeeRefund: std.NewCoin(denom, 300)},
		// A refund the chain withheld never reaches us.
		chain.StorageUnlockEvent{BytesDelta: -9, FeeRefund: std.NewCoin(denom, 900), RefundWithheld: true},
	}

	got := readOutcome(events, me)
	want := outcome{received: 50000, deposit: 2500, refund: 300}
	if got != want {
		t.Fatalf("readOutcome = %+v, want %+v", got, want)
	}
	if net := got.net(10000); net != 37800 {
		t.Fatalf("net = %d, want 37800", net)
	}
}

func TestReadOutcomeCountsWhatWeSend(t *testing.T) {
	events := []abci.Event{
		bank.TransferEvent{From: me, To: registry, Coins: ugnot(1000)},
		bank.TransferEvent{From: registry, To: me, Coins: ugnot(400)},
	}
	if net := readOutcome(events, me).net(100); net != -700 {
		t.Fatalf("net = %d, want -700", net)
	}
}

var testLimits = limits{gasCap: 30000000, gasMargin: 25, maxDeposit: 50000, depositSlack: 2000, minProfit: 1000}

func TestDecideRunsAProfitableJob(t *testing.T) {
	// The measured taproom run: 50000 reward, about 6.6M gas, no deposit.
	p := decide(outcome{received: 50000}, 6610000, 1, 1000, testLimits)
	if !p.run {
		t.Fatalf("not run: %s", p.why)
	}
	if p.gas != 8262500 || p.fee != 8263 {
		t.Errorf("gas %d fee %d, want 8262500 and 8263", p.gas, p.fee)
	}
	if p.expected != 41737 {
		t.Errorf("expected net %d, want 41737", p.expected)
	}
}

// The real transaction must never carry the operator's whole -max-deposit: a
// job that locked nothing in simulation gets only the slack, so growing its
// state in the real block fails the transaction instead of taking the coins.
func TestDecideBoundsTheDepositToTheSimulation(t *testing.T) {
	p := decide(outcome{received: 50000}, 6610000, 1, 1000, testLimits)
	if p.maxDeposit != 2000 {
		t.Errorf("deposit limit %d, want the 2000 slack", p.maxDeposit)
	}
	if p.worst != p.expected-2000 {
		t.Errorf("worst case %d, want %d", p.worst, p.expected-2000)
	}

	// A refund is welcome but never relied on: the last run of a one-shot
	// job really does release its deposit, yet a callback could show a
	// refund in simulation and withhold it in the real block.
	p = decide(outcome{received: 50000, refund: 490000}, 6610000, 1, 1000, testLimits)
	if p.expected != 41737+490000 || p.worst != 41737-2000 {
		t.Errorf("with a refund: expected %d, worst %d", p.expected, p.worst)
	}

	p = decide(outcome{received: 50000, deposit: 3000}, 6610000, 1, 1000, testLimits)
	if p.maxDeposit != 5000 {
		t.Errorf("deposit limit %d, want simulated 3000 + slack 2000", p.maxDeposit)
	}

	// Never wider than the operator allows, never zero.
	p = decide(outcome{received: 500000, deposit: 49500}, 6610000, 1, 1000, testLimits)
	if p.maxDeposit != 50000 {
		t.Errorf("deposit limit %d, want the 50000 cap", p.maxDeposit)
	}
	noSlack := testLimits
	noSlack.depositSlack = 0
	p = decide(outcome{received: 50000}, 6610000, 1, 1000, noSlack)
	if p.maxDeposit != 1 {
		t.Errorf("deposit limit %d, want 1: zero would mean the chain's own limit", p.maxDeposit)
	}
}

func TestDecideSkipsWhatDoesNotPay(t *testing.T) {
	cases := map[string]outcome{
		"reward below the fee":                  {received: 5000},
		"reward eaten by the deposit":           {received: 50000, deposit: 45000},
		"pays only if the slack is not used":    {received: 11000},
		"sends more than it receives":           {received: 50000, sent: 60000},
		"a refund the callback may not deliver": {received: 5000, refund: 500000},
	}
	for name, sim := range cases {
		if p := decide(sim, 6610000, 1, 1000, testLimits); p.run {
			t.Errorf("%s: would run with worst-case net %d", name, p.worst)
		} else if !strings.Contains(p.why, "not worth it") {
			t.Errorf("%s: reason %q", name, p.why)
		}
	}
}

func TestDecideRespectsTheGasCap(t *testing.T) {
	lim := testLimits
	lim.gasCap = 7000000
	p := decide(outcome{received: 50000}, 6610000, 1, 1000, lim)
	if p.gas != 7000000 {
		t.Errorf("gas %d, want it clamped to the 7000000 cap", p.gas)
	}
	if p := decide(outcome{received: 50000}, 6610000, 1, 0, testLimits); p.run {
		t.Error("ran with an unusable gas price")
	}
}

func TestFeeFor(t *testing.T) {
	cases := []struct {
		gas, amount, per, want int64
		ok                     bool
	}{
		{7000000, 1, 1000, 7000, true},
		{7000001, 1, 1000, 7001, true}, // rounds up: a fee one ugnot short is refused
		{999, 1, 1000, 1, true},
		{0, 1, 1000, 0, true},
		{5000, 3, 1000, 15, true},
		{5000, 0, 1000, 0, true},
		{5000, 1, 0, 0, false},
		{-1, 1, 1000, 0, false},
		{3000000000, 4000000000, 1000, 0, false}, // would overflow int64
	}
	for _, c := range cases {
		got, ok := feeFor(c.gas, c.amount, c.per)
		if got != c.want || ok != c.ok {
			t.Errorf("feeFor(%d, %d, %d) = %d, %v; want %d, %v", c.gas, c.amount, c.per, got, ok, c.want, c.ok)
		}
	}
}

func TestWithMargin(t *testing.T) {
	for _, c := range []struct{ gas, percent, want int64 }{
		{7000000, 25, 8750000},
		{7000000, 0, 7000000},
		{6610000, 25, 8262500},
		{199, 50, 298},
	} {
		if got := withMargin(c.gas, c.percent); got != c.want {
			t.Errorf("withMargin(%d, %d) = %d, want %d", c.gas, c.percent, got, c.want)
		}
	}
}

func TestParseDueJobs(t *testing.T) {
	ok := []struct {
		in   string
		want []dueJob
	}{
		{`("" string)`, nil},
		{`("1:50000:1700000000" string)`, []dueJob{{"1", 50000, 1700000000}}},
		{`("1:50000:10,22:1000:20,333:7:30" string)` + "\n", []dueJob{{"1", 50000, 10}, {"22", 1000, 20}, {"333", 7, 30}}},
	}
	for _, c := range ok {
		got, err := parseDueJobs(c.in)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseDueJobs(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}

	bad := []string{
		``, `(1 int)`, `("1" string)`, `("1:5" string)`, `("1:5:" string)`, `(":5:9" string)`, `("1:5:9:9" string)`,
		`("1:5:9,,2:5:9" string)`, `(",1:5:9" string)`, `("1:5:9," string)`, `("1:5:9;2:5:9" string)`,
		`("x:5:9" string)`, `("1:-5:9" string)`, `("1:5:-9" string)`, `("0:5:9" string)`,
		`("99999999999999999999999999:5:9" string)`, `("1:5:9 --send" string)`,
	}
	for _, in := range bad {
		if got, err := parseDueJobs(in); err == nil {
			t.Errorf("parseDueJobs(%q) = %v, want an error", in, got)
		}
	}

	// A leading zero is read as the number it is, not passed through.
	got, err := parseDueJobs(`("007:5:9" string)`)
	if err != nil || got[0].id != "7" {
		t.Errorf("parseDueJobs with a leading zero = %v, %v", got, err)
	}
}

func TestReason(t *testing.T) {
	// The three shapes a node gives a failure, as the bot receives them.
	vmPanic := "msg:0,success:false,log:--= Error =--\nData: errors.FmtError{format:\"job failed: tap is \\\"jammed\\\"\", args:[]interface {}(nil)}\nMsg Traces:\n    0  /gno/tm2/pkg/errors/errors.go:103 - VM panic: job failed\n--= /Error =--\n"
	if got := reason(errors.New("x"), vmPanic); got != `job failed: tap is "jammed"` {
		t.Errorf("realm panic: %q", got)
	}
	if got := reason(abci.StringError("storage deposit exceeds max deposit"), ""); got != "storage deposit exceeds max deposit" {
		t.Errorf("string error: %q", got)
	}
	typed := "--= Error =--\nData: std.InsufficientFundsError{abciError:std.abciError{}}\nMsg Traces:\n    0  /gno/tm2/pkg/sdk/auth/ante.go:99 - insufficient funds to pay for fees; 5ugnot < 30000ugnot\n--= /Error =--\n"
	if got := reason(errors.New("x"), typed); got != "insufficient funds to pay for fees; 5ugnot < 30000ugnot" {
		t.Errorf("typed error: %q", got)
	}
	if got := reason(nil, ""); got != "unknown error" {
		t.Errorf("nothing to go on: %q", got)
	}
}

// A job's callback chooses its own error text. It must not be able to draw
// on the operator's terminal or fake a log line.
func TestReasonCannotForgeLogLines(t *testing.T) {
	hostile := "Data: errors.FmtError{format:\"oops\\x1b[2K\\r2026/11/24 job 1: ran at height 99\\nnext\", args:nil}\n"
	got := reason(errors.New("x"), hostile)
	if strings.ContainsAny(got, "\x1b\r\n") {
		t.Errorf("control characters survived: %q", got)
	}
	if long := clean(strings.Repeat("a", 500), 40); len(long) > 50 {
		t.Errorf("not truncated: %d bytes", len(long))
	}
}

func TestLedgerBacksOffAndForgives(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLedger(func() time.Time { return now })

	if l.resting("1") {
		t.Fatal("an unknown job is resting")
	}
	if wait := l.skipped("1"); wait != time.Minute {
		t.Errorf("first skip waits %s", wait)
	}
	if !l.resting("1") {
		t.Error("a job just skipped is not resting")
	}
	now = now.Add(time.Minute)
	if l.resting("1") {
		t.Error("still resting after the wait")
	}
	if wait := l.skipped("1"); wait != 2*time.Minute {
		t.Errorf("second skip waits %s", wait)
	}
	for i := 0; i < 20; i++ {
		if wait := l.skipped("1"); wait > skipMax {
			t.Fatalf("skip %d waits %s, more than the cap", i, wait)
		}
	}
	if l.lost != 0 {
		t.Errorf("skips cost %d", l.lost)
	}

	l.paid("1")
	if l.resting("1") {
		t.Error("a job that paid is still resting")
	}
}

func TestLedgerBansAfterRepeatedLosses(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLedger(func() time.Time { return now })

	for i := 1; i <= maxLosses; i++ {
		wait, banned := l.lostOn("7", 8000)
		if banned != (i == maxLosses) {
			t.Errorf("loss %d: banned = %v", i, banned)
		}
		if want := lossFirst << (i - 1); wait != want {
			t.Errorf("loss %d waits %s, want %s", i, wait, want)
		}
	}
	if l.lost != 8000*maxLosses {
		t.Errorf("lost %d", l.lost)
	}
	now = now.Add(1000 * time.Hour)
	if !l.resting("7") {
		t.Error("a banned job came back")
	}
	if l.resting("8") {
		t.Error("another job is affected")
	}
}

func TestLedgerStaysBounded(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLedger(func() time.Time { return now })
	for i := 0; i < maxRecords+500; i++ {
		l.skipped(string(rune('a'+i%26)) + time.Duration(i).String())
	}
	if len(l.records) > maxRecords {
		t.Errorf("%d records, more than %d", len(l.records), maxRecords)
	}
}

func TestReadPassword(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "pw")
	if err := os.WriteFile(file, []byte("s3cret\r\nsecond line\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("LAMPLIGHTER_PASSWORD", "from-env")
	if got, err := readPassword(file); err != nil || got != "s3cret" {
		t.Errorf("file given: %q, %v; the file must win over the environment", got, err)
	}
	if got, err := readPassword(""); err != nil || got != "from-env" {
		t.Errorf("no file: %q, %v", got, err)
	}
	if _, err := readPassword(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing file is not an error")
	}

	// An empty password is a real one (throwaway dev keys), not "unset".
	t.Setenv("LAMPLIGHTER_PASSWORD", "")
	if got, err := readPassword(""); err != nil || got != "" {
		t.Errorf("empty password: %q, %v", got, err)
	}
	os.Unsetenv("LAMPLIGHTER_PASSWORD")
	if _, err := readPassword(""); err == nil {
		t.Error("no password source is not an error")
	}
}

func TestConfigValidate(t *testing.T) {
	good := config{
		registry: "gno.land/r/x/registry", home: "/tmp", key: "k",
		interval: 15 * time.Second, timeout: 20 * time.Second, maxJobs: 20, maxLoss: 100000,
		lim: testLimits,
	}
	if err := good.validate(); err != nil {
		t.Fatalf("good config refused: %v", err)
	}
	bad := map[string]func(*config){
		"no registry":        func(c *config) { c.registry = "" },
		"hot loop":           func(c *config) { c.interval = 0 },
		"no timeout":         func(c *config) { c.timeout = 0 },
		"no jobs":            func(c *config) { c.maxJobs = 0 },
		"negative loss":      func(c *config) { c.maxLoss = -1 },
		"no deposit limit":   func(c *config) { c.lim.maxDeposit = 0 },
		"negative slack":     func(c *config) { c.lim.depositSlack = -1 },
		"no gas":             func(c *config) { c.lim.gasCap = 0 },
		"absurd gas":         func(c *config) { c.lim.gasCap = 1e12 },
		"negative margin":    func(c *config) { c.lim.gasMargin = -10 },
		"overflowing margin": func(c *config) { c.lim.gasMargin = 1e9 },
	}
	for name, breakIt := range bad {
		c := good
		breakIt(&c)
		if c.validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
