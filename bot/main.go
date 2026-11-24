// Command lamplighter-bot runs due jobs on a Lamplighter registry and collects
// their rewards.
//
// Every pass it asks the registry which jobs are due, simulates running each
// one, and broadcasts the run only if the simulation says it pays: the reward
// the chain would actually transfer, minus the gas fee, minus the storage
// deposit the transaction is allowed to lock.
//
// A job's callback is somebody else's code. The bot assumes it may be hostile:
// see decide for how a run is bounded, and ledger for how a job that misbehaves
// is left alone.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	"github.com/gnolang/gno/gno.land/pkg/sdk/vm"
	"github.com/gnolang/gno/tm2/pkg/amino"
	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	"github.com/gnolang/gno/tm2/pkg/crypto"
	"github.com/gnolang/gno/tm2/pkg/crypto/keys"
	"github.com/gnolang/gno/tm2/pkg/std"
)

type config struct {
	remote       string
	chainID      string
	registry     string
	home         string
	key          string
	passwordFile string
	interval     time.Duration
	timeout      time.Duration
	once         bool
	dryRun       bool
	maxJobs      int
	maxLoss      int64
	lim          limits
}

func main() {
	var cfg config
	flag.StringVar(&cfg.remote, "remote", "https://rpc.onyx.testnets.gno.land:443", "RPC endpoint of the chain")
	flag.StringVar(&cfg.chainID, "chain-id", "onyx-1", "chain id to sign for; must be the one the endpoint serves")
	flag.StringVar(&cfg.registry, "registry", "", "package path of the Lamplighter registry (required)")
	flag.StringVar(&cfg.home, "home", "", "gnokey home directory holding the key (required)")
	flag.StringVar(&cfg.key, "key", "", "name of the key to sign with (required)")
	flag.StringVar(&cfg.passwordFile, "password-file", "", "file holding only the key's password; otherwise LAMPLIGHTER_PASSWORD is used")
	flag.DurationVar(&cfg.interval, "interval", 15*time.Second, "time between passes")
	flag.DurationVar(&cfg.timeout, "timeout", 20*time.Second, "time allowed for one request to the endpoint")
	flag.BoolVar(&cfg.once, "once", false, "make one pass and exit; the exit status is 1 if the pass could not be made")
	flag.BoolVar(&cfg.dryRun, "dry-run", false, "simulate and report, never broadcast")
	flag.IntVar(&cfg.maxJobs, "max-jobs", 20, "most jobs to simulate in one pass")
	flag.Int64Var(&cfg.maxLoss, "max-loss", 100000, "ugnot of losses after which the bot stops broadcasting")
	flag.Int64Var(&cfg.lim.minProfit, "min-profit", 1000, "smallest worst-case net gain, in ugnot, worth running a job for")
	flag.Int64Var(&cfg.lim.maxDeposit, "max-deposit", 50000, "most ugnot a single run may lock as storage deposit")
	flag.Int64Var(&cfg.lim.depositSlack, "deposit-slack", 2000, "ugnot of storage deposit a run may lock beyond what its simulation did")
	flag.Int64Var(&cfg.lim.gasCap, "gas-cap", 30000000, "most gas a single run may use")
	flag.Int64Var(&cfg.lim.gasMargin, "gas-margin", 25, "percent of gas added on top of what the simulation used")
	flag.Parse()

	if err := cfg.validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		flag.Usage()
		os.Exit(2)
	}

	log.SetFlags(log.LstdFlags | log.LUTC)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, cfg)
	stop()
	if err != nil {
		log.Printf("error: %v", err)
		os.Exit(1)
	}
}

func (cfg config) validate() error {
	switch {
	case cfg.registry == "" || cfg.home == "" || cfg.key == "":
		return errors.New("-registry, -home and -key are required")
	case cfg.interval < time.Second:
		return errors.New("-interval must be at least 1s")
	case cfg.timeout < time.Second:
		return errors.New("-timeout must be at least 1s")
	case cfg.maxJobs < 1:
		return errors.New("-max-jobs must be at least 1")
	case cfg.maxLoss < 0:
		return errors.New("-max-loss must not be negative")
	case cfg.lim.maxDeposit < 1:
		// A transaction with no deposit limit falls back to the chain's own,
		// far larger one, which is the opposite of what a zero here asks for.
		return errors.New("-max-deposit must be at least 1")
	case cfg.lim.depositSlack < 0:
		return errors.New("-deposit-slack must not be negative")
	case cfg.lim.gasCap < 1 || cfg.lim.gasCap > 1e10:
		return errors.New("-gas-cap must be between 1 and 10000000000")
	case cfg.lim.gasMargin < 0 || cfg.lim.gasMargin > 1000:
		return errors.New("-gas-margin must be between 0 and 1000")
	}
	return nil
}

func run(ctx context.Context, cfg config) error {
	if info, err := os.Stat(cfg.home); err != nil || !info.IsDir() {
		return fmt.Errorf("-home %s is not a directory", cfg.home)
	}
	password, err := readPassword(cfg.passwordFile)
	if err != nil {
		return err
	}
	kb, err := keys.NewKeyBaseFromDir(cfg.home)
	if err != nil {
		return fmt.Errorf("open keybase: %w", err)
	}
	signer := gnoclient.SignerFromKeybase{Keybase: kb, Account: cfg.key, Password: password, ChainID: cfg.chainID}
	if err := signer.Validate(); err != nil {
		return fmt.Errorf("key %q: %w", cfg.key, err)
	}
	info, err := signer.Info()
	if err != nil {
		return fmt.Errorf("key %q: %w", cfg.key, err)
	}
	rpc, err := rpcclient.NewHTTPClient(cfg.remote, rpcclient.WithRequestTimeout(cfg.timeout))
	if err != nil {
		return fmt.Errorf("connect to %s: %w", cfg.remote, err)
	}
	defer rpc.Close()

	// A wrong chain id is invisible to simulation, which skips the signature
	// check, and would surface only as every broadcast being refused.
	status, err := rpc.Status(ctx, nil)
	if err != nil {
		return fmt.Errorf("ask %s for its status: %w", cfg.remote, err)
	}
	if status.NodeInfo.Network != cfg.chainID {
		return fmt.Errorf("%s serves chain %q, not -chain-id %q", cfg.remote, status.NodeInfo.Network, cfg.chainID)
	}

	b := &bot{
		cfg:    cfg,
		client: gnoclient.Client{Signer: signer, RPCClient: rpc},
		addr:   info.GetAddress(),
		ledger: newLedger(time.Now),
	}

	mode := "live"
	if cfg.dryRun {
		mode = "dry run"
	}
	log.Printf("lamplighter-bot (%s): %s on %s, running jobs of %s", mode, b.addr, cfg.chainID, cfg.registry)

	for {
		err := b.pass(ctx)
		if cfg.once {
			return err
		}
		if err != nil {
			log.Printf("pass abandoned: %v", err)
		}
		select {
		case <-ctx.Done():
			log.Printf("stopping")
			return nil
		case <-time.After(cfg.interval):
		}
	}
}

type bot struct {
	cfg    config
	client gnoclient.Client
	addr   crypto.Address
	ledger *ledger
	halted bool // the loss budget is spent: simulate, never broadcast
}

// pass looks for due jobs once and tries each of them. It returns an error
// only when the pass could not be made at all; one job going wrong is logged
// and the pass moves on.
func (b *bot) pass(ctx context.Context) error {
	price, err := b.gasPrice()
	if err != nil {
		return fmt.Errorf("gas price: %w", err)
	}
	account, _, err := b.client.QueryAccount(b.addr)
	if err != nil {
		return fmt.Errorf("account %s: %w", b.addr, err)
	}
	probeFee, ok := feeFor(b.cfg.lim.gasCap, price.Price.Amount, price.Gas)
	if !ok {
		return errors.New("the chain's gas price is unusable")
	}
	if have := account.Coins.AmountOf(denom); have < probeFee {
		// Even a simulation is refused when the account could not pay its fee.
		return fmt.Errorf("%s holds %d %s, less than the %d a run at -gas-cap may cost", b.addr, have, denom, probeFee)
	}

	jobs, err := b.candidates()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if ctx.Err() != nil {
			return nil
		}
		sent, err := b.try(ctx, j, price, account)
		if err != nil {
			log.Printf("job %s: %v", j.id, err)
		}
		if sent {
			// The sequence has moved, and so has the balance.
			if account, _, err = b.client.QueryAccount(b.addr); err != nil {
				return fmt.Errorf("account %s: %w", b.addr, err)
			}
		}
	}
	return nil
}

// candidates pages through the due jobs and returns up to -max-jobs of them
// that are worth simulating. Jobs that are resting, and jobs whose reward
// could not cover -min-profit even with a free transaction, are passed over
// without costing a request each, so that a flood of worthless jobs cannot
// crowd out a real one.
func (b *bot) candidates() ([]dueJob, error) {
	const pageSize, maxPages = 100, 10

	var picked []dueJob
	afterDue, afterID := int64(0), "0"
	for page := 0; page < maxPages && len(picked) < b.cfg.maxJobs; page++ {
		expr := fmt.Sprintf("DueJobs(%d, %s, %d)", afterDue, afterID, pageSize)
		answer, _, err := b.client.QEval(b.cfg.registry, expr)
		if err != nil {
			return nil, fmt.Errorf("ask for due jobs: %s", clean(firstLine(err), 200))
		}
		due, err := parseDueJobs(answer)
		if err != nil {
			return nil, err
		}
		for _, j := range due {
			if j.reward <= b.cfg.lim.minProfit || b.ledger.resting(j.id) {
				continue
			}
			if picked = append(picked, j); len(picked) == b.cfg.maxJobs {
				break
			}
		}
		if len(due) < pageSize {
			break
		}
		last := due[len(due)-1]
		afterDue, afterID = last.due, last.id
	}
	return picked, nil
}

// try simulates one job and broadcasts it if it pays. It reports whether a
// transaction was broadcast.
func (b *bot) try(ctx context.Context, j dueJob, price std.GasPrice, account *std.BaseAccount) (bool, error) {
	lim := b.cfg.lim
	me := b.addr.String()

	// First signature: the operator's widest limits, only to simulate.
	probeFee, _ := feeFor(lim.gasCap, price.Price.Amount, price.Gas)
	probe, err := b.signed(j.id, lim.gasCap, probeFee, lim.maxDeposit, account)
	if err != nil {
		return false, err
	}
	sim, err := b.client.SimulateResult(probe)
	if err != nil {
		return false, fmt.Errorf("simulate: %s", clean(firstLine(err), 200))
	}
	if sim.Error != nil {
		// Its callback failed, another executor got there first, or it wants
		// more gas or deposit than the limits allow.
		wait := b.ledger.skipped(j.id)
		return false, fmt.Errorf("would fail, skipped for %s: %s", wait, reason(sim.Error, sim.Log))
	}

	p := decide(readOutcome(sim.Events, me), sim.GasUsed, price.Price.Amount, price.Gas, lim)
	if !p.run {
		wait := b.ledger.skipped(j.id)
		log.Printf("job %s: %s; skipped for %s", j.id, p.why, wait)
		return false, nil
	}
	if b.cfg.dryRun || b.halted {
		log.Printf("job %s: would run: fee %d, deposit limit %d, net %d expected and %d at worst", j.id, p.fee, p.maxDeposit, p.expected, p.worst)
		return false, nil
	}
	if ctx.Err() != nil {
		return false, nil
	}

	// Second signature: the gas this run needs, the fee that goes with it,
	// and a deposit limit no wider than what the simulation showed.
	tx, err := b.signed(j.id, p.gas, p.fee, p.maxDeposit, account)
	if err != nil {
		return false, err
	}
	res, err := b.client.BroadcastTxCommit(tx)
	switch {
	case res == nil:
		// The node did not say what happened. The transaction may still be
		// included, so assume the fee is gone and leave the job alone.
		b.lose(j.id, p.fee)
		return true, fmt.Errorf("outcome unknown, assuming the fee of %d is lost: %s", p.fee, clean(firstLine(err), 200))
	case res.CheckTx.IsErr():
		// Refused before it reached a block: nothing was charged.
		b.ledger.skipped(j.id)
		return true, fmt.Errorf("not accepted, nothing lost: %s", reason(res.CheckTx.Error, res.CheckTx.Log))
	case res.DeliverTx.IsErr():
		// Included and failed: the fee is charged all the same. Either
		// another executor ran the job first, or the job behaves differently
		// in a real block than in simulation.
		b.lose(j.id, p.fee)
		return true, fmt.Errorf("failed at height %d and cost the fee of %d: %s",
			res.Height, p.fee, reason(res.DeliverTx.Error, res.DeliverTx.Log))
	}

	actual := readOutcome(res.DeliverTx.Events, me)
	net := actual.net(p.fee)
	log.Printf("job %s: ran at height %d: %s, fee %d, net %d ugnot", j.id, res.Height, actual, p.fee, net)
	if net < p.expected {
		b.lose(j.id, p.expected-net)
		log.Printf("job %s: paid %d less than its simulation promised", j.id, p.expected-net)
	} else {
		b.ledger.paid(j.id)
	}
	return true, nil
}

// lose books a loss against a job and against the bot's budget.
func (b *bot) lose(id string, amount int64) {
	wait, banned := b.ledger.lostOn(id, amount)
	if banned {
		log.Printf("job %s: lost money %d times, leaving it alone for good", id, maxLosses)
	} else {
		log.Printf("job %s: leaving it alone for %s", id, wait)
	}
	if !b.halted && b.ledger.lost >= b.cfg.maxLoss {
		b.halted = true
		log.Printf("losses have reached %d ugnot (-max-loss %d): no more transactions will be sent", b.ledger.lost, b.cfg.maxLoss)
	}
}

// signed builds and signs the transaction that runs one job.
func (b *bot) signed(id string, gasWanted, fee, maxDeposit int64, account *std.BaseAccount) (*std.Tx, error) {
	msg := vm.MsgCall{
		Caller:     b.addr,
		PkgPath:    b.cfg.registry,
		Func:       "Exec",
		Args:       []string{id},
		MaxDeposit: std.Coins{std.NewCoin(denom, maxDeposit)},
	}
	unsigned, err := gnoclient.NewCallTx(gnoclient.BaseTxCfg{
		GasFee:    fmt.Sprintf("%d%s", fee, denom),
		GasWanted: gasWanted,
	}, msg)
	if err != nil {
		return nil, fmt.Errorf("build transaction: %w", err)
	}
	tx, err := b.client.SignTx(*unsigned, account.AccountNumber, account.Sequence)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	return tx, nil
}

// gasPrice asks the chain for its current gas price.
func (b *bot) gasPrice() (std.GasPrice, error) {
	res, err := b.client.Query(gnoclient.QueryCfg{Path: "auth/gasprice"})
	if err != nil {
		return std.GasPrice{}, errors.New(clean(firstLine(err), 200))
	}
	var price std.GasPrice
	if err := amino.UnmarshalJSON(res.Response.Data, &price); err != nil {
		return std.GasPrice{}, err
	}
	if price.Gas <= 0 || price.Price.Denom != denom || price.Price.Amount < 0 {
		return std.GasPrice{}, fmt.Errorf("unexpected answer %s", clean(string(res.Response.Data), 80))
	}
	return price, nil
}

// readPassword returns the key's password without it ever being a command
// line argument: the first line of -password-file if one is given, otherwise
// the LAMPLIGHTER_PASSWORD environment variable.
func readPassword(file string) (string, error) {
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read -password-file: %w", err)
		}
		line, _, _ := strings.Cut(string(data), "\n")
		return strings.TrimRight(line, "\r"), nil
	}
	if v, ok := os.LookupEnv("LAMPLIGHTER_PASSWORD"); ok {
		return v, nil
	}
	return "", errors.New("pass -password-file or set LAMPLIGHTER_PASSWORD")
}

func firstLine(err error) string {
	line, _, _ := strings.Cut(err.Error(), "\n")
	return line
}
