<p align="center">
  <img src="docs/logo.png" alt="Lamplighter logo: a lantern held by a robot arm" width="180">
</p>

<h1 align="center">Lamplighter</h1>

<p align="center"><strong>Scheduled work for gno.land realms, run by whoever shows up.</strong></p>

<p align="center">
  <a href="https://github.com/sontrh/lamplighter/actions/workflows/ci.yml"><img src="https://github.com/sontrh/lamplighter/actions/workflows/ci.yml/badge.svg" alt="ci"></a>
</p>

gno.land has no scheduler: a realm only runs when a transaction calls it. Lamplighter is a registry where a realm posts a callback, a schedule and a reward. Once the job is due, anyone may run it, and the registry pays them the reward in the same transaction.

An entry for the [Gnome Tavern hackathon](https://dorahacks.io/hackathon/gnome-tavern/detail).

| | |
|---|---|
| Registry realm | `gno.land/r/g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx/lamplighter/registry/v2` |
| Network | Onyx testnet (`onyx-1`) |
| Status | Live on Onyx: the registry and Last Orders since 2026-11-24, the taproom since 2026-11-27. See [Deployment](#deployment-on-onyx). |
| Live pages | [Registry dashboard](https://onyx.testnets.gno.land/r/g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx/lamplighter/registry/v2) · [Taproom](https://onyx.testnets.gno.land/r/g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx/lamplighter/taproom/v3) · [Last Orders](https://onyx.testnets.gno.land/r/g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx/lamplighter/lastorders/v1) |
| Demo video | [youtu.be/wRI9C4O5Lt8](https://youtu.be/wRI9C4O5Lt8) (3:26, English captions) |

## Contents

- [The problem](#the-problem)
- [How it works](#how-it-works)
- [Post a job from your realm](#post-a-job-from-your-realm)
- [Trust model](#trust-model)
- [Run jobs and get paid](#run-jobs-and-get-paid)
- [Where the storage deposit goes](#where-the-storage-deposit-goes)
- [The two example realms](#the-two-example-realms)
- [Try it](#try-it)
- [Tests](#tests)
- [Limits](#limits)
- [Prior art](#prior-art)
- [Deployment on Onyx](#deployment-on-onyx)
- [Layout](#layout)
- [License](#license)

## The problem

An escrow that should release at a deadline, a savings circle that should rotate every week, an auction that should close at nine: none of them can make it happen. Each needs somebody to send a transaction at the right moment, and each realm ends up inventing its own "anyone may poke me" function and hoping somebody does.

The idea of the chain triggering contracts itself was raised in [gnolang/gno#455](https://github.com/gnolang/gno/issues/455) in 2023, with the note that it is "probably something that won't happen". moul's [`cron` package](https://github.com/moul/gno-contracts/tree/main/p/moul/x/daily/cron) parses cron expressions and says plainly why it stops there: "A chain has no timer, so a schedule here is never a thing that runs."

Lamplighter is the other half: one shared place to post the work, a reward that makes running it worth somebody's while, and a bot that does it.

## How it works

```
your realm --Register(callback, schedule, reward)--> registry        post a job
anyone     --Fund(id) with ugnot------------------> registry        add oil
anyone     --Exec(id)-----------------------------> registry --callback(id)--> your realm
                                                    registry --reward-------> the caller
```

Oil is a job's balance in ugnot. Each run pays one reward out of it, and a job holding less than one reward is not offered to anyone.

The callback is a function value of type `func(realm, uint64) error`. The GnoVM runs a function declared in a realm under that realm's authority, wherever the value is stored and whoever calls it ([interrealm spec, borrow rule 1](https://docs.gno.land/resources/gno-interrealm-v2)). So the registry can hold callbacks from any realm and gains no power over their state. GovDAO runs proposal executors the same way.

### API

| Function | Who may call | What it does |
|---|---|---|
| `Register(name, firstDue, interval, reward, callback) uint64` | a deployed realm | Posts a job. `interval` is in seconds; `0` means the job runs once. |
| `Fund(id)` | a user, with ugnot attached | Adds oil to a job. |
| `Credit(id, amount)` | anyone; meant for a realm that has just sent coins to the registry | Adds `amount` of the registry's unaccounted coins to a job. |
| `Exec(id)` | anyone | Runs a due, funded job and pays the caller its reward. |
| `Cancel(id, refundTo)` | the realm that posted the job | Removes the job and refunds its remaining oil. |
| `DueJobs(afterDue, afterID, limit) string` | query | Jobs that can run now, longest overdue first, as `id:reward:due`. Pass the last entry to read on. |
| `IsDue`, `Exists`, `Done`, `NextDue`, `Balance`, `Reward`, `Owner`, `Unaccounted`, `Path` | query | Read a job's state. |

Rules worth knowing:

- **The schedule is a fixed grid.** Slots are `firstDue`, `firstDue + interval`, and so on. A late run does not shift the grid, and slots nobody ran are skipped, not replayed.
- **A failing callback aborts the whole transaction.** No reward is paid and nothing changes.
- **Bounds.** Interval 10 seconds to 10 years; first run at most 10 years ahead; reward at least 1,000 ugnot; name up to 80 bytes of printable text. The bounds also rule out integer overflow in the schedule.
- **A job that runs once holds at most one reward**, and stays registered, marked done, until its owner cancels it.

## Post a job from your realm

```go
import "gno.land/r/g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx/lamplighter/registry/v2"

var job uint64

func init(cur realm) {
	// first run in an hour, then every hour, paying 0.05 GNOT a run
	job = registry.Register(cross(cur), "Hourly settlement",
		time.Now().Unix()+3600, 3600, 50000, settle)
}

func settle(cur realm, id uint64) error {
	if cur.Previous().PkgPath() != registry.Path() || id != job {
		return errors.New("not my job")
	}
	// ... the work ...
	return nil
}
```

Then fund it: a user calls `Fund` with coins attached, or your realm sends coins to the registry's address and calls `Credit` for the same amount in the same transaction ([`lastorders.gno`](gno.land/r/g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx/lamplighter/lastorders/v1/lastorders.gno) does this).

## Trust model

**What the registry can do to your realm: nothing but call the callback you gave it.** It cannot write your state.

**What your callback must check: the caller and the job id.** `Exec` is open to everyone and so is `Register`, so the registry will run any reachable `func(realm, uint64) error` for anyone, including another realm's exported one. "The caller is the registry" says nothing about which job is running or who scheduled it. The job id does. Keep the callback unexported, and never grant the registry's address a privilege in your realm. [`TestAnotherRealmCannotScheduleTheTap`](gno.land/r/g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx/lamplighter/taproom/v3/taproom_test.gno) is the attack, refused.

**What running a job exposes you to: somebody else's code.** A callback is written by the job's owner. Whoever sends the transaction pays the storage deposit for every byte of state that code adds, in any realm, and a callback can behave differently in a real block than it did in simulation. Send `Exec` with a small `-max-deposit`. The bot does more; see below.

**Whose the oil is: the job's.** Anyone may fund a job, but only the realm that posted it can cancel it, and that realm decides where the remainder goes. Fund jobs of realms whose code you have read.

**What is not promised: that a job runs.** There is no staking and no penalty. A job runs when its reward is worth more than the cost of running it, or when somebody who cares runs it anyway. `Exec` being open to everyone is also the fallback: the owner, or anyone the job matters to, can always run it themselves.

## Run jobs and get paid

By hand, from the job's page on the dashboard, or:

```sh
gnokey maketx call -pkgpath gno.land/r/g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx/lamplighter/registry/v2 \
  -func Exec -args <job id> -max-deposit 10000ugnot \
  -gas-fee 20000ugnot -gas-wanted 20000000 \
  -broadcast -chainid onyx-1 -remote https://rpc.onyx.testnets.gno.land:443 <your key>
```

Or let the bot do it:

```sh
make tools bot
LAMPLIGHTER_PASSWORD=... .tools/bin/lamplighter-bot \
  -registry gno.land/r/g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx/lamplighter/registry/v2 \
  -home ~/.config/gno -key <your key>
```

Each pass (every 15 seconds by default) the bot:

1. pages through `DueJobs`, passing over jobs it is resting and jobs whose reward could not reach `-min-profit`;
2. signs and **simulates** `Exec` for each, under the widest limits you allow;
3. reads the simulation's events for what the chain would actually transfer to it, lock as deposit, and refund. It does not trust the advertised reward;
4. signs the real transaction with gas for this run plus 25%, and a **deposit limit of what the simulation locked plus a small slack** (`-deposit-slack`, 2,000 ugnot). It decides on the worst case: the whole limit spent, and no refund;
5. compares the real result with the estimate.

A job that passes simulation and then grows its state in the real block therefore fails, at the cost of the fee, instead of taking up to your whole `-max-deposit`.

What it remembers between passes:

| The job | The bot |
|---|---|
| would fail, or does not pay | rests it 1 minute, doubling to 1 hour. Costs nothing. |
| failed in its real block, or paid less than simulated | books the loss, rests it 10 minutes, doubling; on the third loss leaves it for good |
| losses reach `-max-loss` (100,000 ugnot) | the bot stops broadcasting |

`-dry-run` reports without sending. `-once` makes one pass, for cron. The password comes from `-password-file` or `LAMPLIGHTER_PASSWORD`, never from a flag.

Lines from the bot's log during one `make smoke` run on a local chain (several passes; the registry path is shortened here):

```
lamplighter-bot (live): g1sky8ml7xyg47qeskdtcagfah525yf6sv7q9f6q on dev, running jobs of gno.land/r/.../lamplighter/registry/v2
job 1: ran at height 88: received 50000, sent 0, storage deposit 0, refund 0, fee 12112, net 37888 ugnot
job 2: would fail, skipped for 1m0s: storage deposit processing encountered one or more errors: not enough deposit to cover the storage usage [...]
job 3: failed at height 161 and cost the fee of 11345: job failed: not in the mood
job 3: leaving it alone for 10m0s
```

**Known limit: racing.** Two bots that see the same due job both simulate success and both send; the second pays the fee for nothing. The bot does not yet try to avoid this. With a 50,000 ugnot reward and a fee of about 12,000, the expected gain reaches zero at about four bots competing for the same job.

## Where the storage deposit goes

gno.land locks 100 ugnot per byte of state from whoever sends the transaction that adds it, and releases it to whoever sends the transaction that frees it. For a registry that strangers transact with, that rule decides who really pays whom, and the first two versions of this registry got it wrong. Both mistakes were measured on a local chain:

- **A leaderboard row per executor.** Every new executor paid the deposit for their own row: a first run lost 0.17 GNOT against a 0.05 GNOT reward.
- **Deleting a one-shot job when it ran.** The executor's transaction freed the job's state, so the chain paid the executor the deposit the *registrant* had locked: 0.72 GNOT on top of a 0.05 GNOT fee.

The rules the current version follows:

- **Registering** a job is the one step that locks a real deposit: a few kilobytes, from the registrant.
- **Funding and running** leave the size of the state almost unchanged. Every job stays in the queue index for its whole life; funding it, or spending its last reward, moves it between a due key and a parked key of the same length.
- **Only `Cancel` frees a job**, and only the owner can call it. The deposit goes where the owner arranges, not to whoever happens to run the job. A run pays its reward and nothing else.
- The dashboard remembers the last eight runs in a ring allocated at deploy, not a row per runner.

Measured on Onyx on 2026-11-27, in the rehearsal described under [Tests](#tests) (every reward is 50,000 ugnot):

| | |
|---|---|
| The keeper opening the taproom, which registers its job | +4,074 bytes of state: a 0.41 GNOT deposit from the keeper |
| Adding oil to a job that had none | +12 bytes of state |
| A person running the taproom job by hand | +11 bytes; fee and deposit 21,100, net **+28,900 ugnot** |
| The bot running the taproom job | fee 10,909, deposit 100, net **+38,991 ugnot** |
| The bot settling an auction round | fee 13,032, deposit 100, net **+36,868 ugnot** |
| Opening an auction round | +8,625 to +11,593 bytes of state, deposit paid by the seller |
| The seller reclaiming after the round | −5,311 to −8,234 bytes of state, deposit back to the seller |

Over those two rounds the registry's share, 10,112 bytes, was freed in full. Last Orders keeps 6,673 bytes for the two entries on its wall.

Hostile jobs exist only on the local test chain. There, a job that failed in its real block cost the bot one fee, 11,345 ugnot, after which the bot rested the job.

## The two example realms

**[Taproom](gno.land/r/g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx/lamplighter/taproom/v3/taproom.gno)**: the smallest possible consumer. The brew on tap changes every five minutes; one repeating job, posted when its keeper opens the taproom. The keeper can close it, which cancels the job and returns the unspent oil.

**[Last Orders](gno.land/r/g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx/lamplighter/lastorders/v1/lastorders.gno)**: an auction that has to end on time. The highest bidder buys the last round of the night and gets a toast on the wall. Each round posts its own one-shot job and funds it, in the same call, from the fee the seller attached. Running the job at closing time moves the winning bid to the seller. Afterwards the seller cancels the finished job to get the storage deposit back.

What the pages look like on Onyx, as rendered on 2026-11-27 after the rehearsal described under [Tests](#tests):

<details>
<summary>The registry dashboard</summary>

**Lamplighter**

gno.land has no scheduler: a realm only runs when a transaction calls it. This is where realms post work that has to happen on a schedule. Anyone who runs a job once it is due is paid its reward in the same transaction.

**1 job** · **7 runs** · **0.35 GNOT** paid out

**Jobs**

| # | Job | Posted by | Every | Next run | Reward | Oil | Runs |
|---|---|---|---|---|---|---|---|
| 7 | Taproom: change the brew on tap | /r/g1zjum…dppx/lamplighter/taproom/v3 | 5m | **due now** | 0.05 GNOT | 0.4 GNOT | 2 |

**Recent runs**

| When | Job | Run by | Paid |
|---|---|---|---|
| 2026-11-27 02:37:46 UTC | 7 | g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx | 0.05 GNOT |
| 2026-11-27 02:33:05 UTC | 7 | g1h5mz9twxznnpwqlm0ugj4tacg69pnpptwjnucg | 0.05 GNOT |
| 2026-11-27 02:26:58 UTC | 5 | g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx | 0.05 GNOT |
| 2026-11-27 02:26:48 UTC | 6 | g1h5mz9twxznnpwqlm0ugj4tacg69pnpptwjnucg | 0.05 GNOT |
| 2026-11-24 23:21:18 UTC | 2 | g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx | 0.05 GNOT |
| 2026-11-24 23:21:08 UTC | 3 | g1h5mz9twxznnpwqlm0ugj4tacg69pnpptwjnucg | 0.05 GNOT |
| 2026-11-24 22:54:06 UTC | 1 | g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx | 0.05 GNOT |

(`g1zjum…dppx` runs the bot; `g1h5mz…nucg` is a person running jobs by hand. Jobs 1, 2, 3, 5 and 6 were auction rounds, settled and then reclaimed by their sellers. Job 4 belonged to the superseded taproom/v2 and was cancelled unrun when its keeper closed it.)

</details>

<details>
<summary>Last Orders</summary>

**Last Orders**

The last round of the night goes to the highest bidder, whose toast is written on the wall. Each round ends at a set time, and a realm cannot wake itself to end it. So every round posts a Lamplighter job due at closing time: whoever runs it settles the round and is paid 0.05 GNOT for it.

Open a round (you put up 0.05 GNOT for the lamplighter)

**The wall**

| # | Round | Won by | Toast |
|---|---|---|---|
| 5 | A quiet Tuesday | nobody | |
| 4 | Friday's last round | g1ytzg…sm0c for 0.105 GNOT | To the lamplighters! |
| 3 | A quiet Tuesday | nobody | |
| 2 | Friday's last round | g1ytzg…sm0c for 0.105 GNOT | To the lamplighters! |
| 1 | Tonight's last round | g1zjum…dppx for 0.1 GNOT | To the lamplighters! |

</details>

## Try it

Needs git, make and Go 1.25.9 or newer.

```sh
make tools   # builds gno, gnokey and gnodev at v1.8.0-rc.1, the version Onyx runs, into .tools/
make test    # unit tests of the three realms and of the bot
make smoke   # the whole thing on a throwaway local chain, with real transactions (three to six minutes)
make dev     # a local chain with gnoweb at http://127.0.0.1:8899
```

`make dev` uses the throwaway keys that `make smoke` creates, so run that once first. Then open `http://127.0.0.1:8899/r/g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx/lamplighter/lastorders/v1` and open a round to post a job. The local taproom stays unopened, because its keeper is the published key.

The tools are pinned to the chain's version on purpose: a `gnokey` from before v1.7.1 produces transactions that Onyx refuses.

## Tests

- **Realms** (`make test`): 30 tests. The eleven registry tests about funding, running, cancelling and the due index each end by checking two invariants: the coins the realm holds cover what its jobs are owed, and every job is filed in the queue index under the one key that matches its state.
- **Bot** (`make test`): 16 tests of the decision, the fee and gas arithmetic, the parsing of what the chain sends back, and the loss ledger.
- **End to end** (`make smoke`): 62 checks with real transactions on a local chain:
  - the registry and the taproom: funding, running, and everything that must be refused, including a `maketx run` script trying to register;
  - the bot as a first-time executor: dry run, skipping what does not pay, a real run, and its estimate against its real balance;
  - the auction: open, bid, refund of the outbid bidder, the bot settling the round, the seller paid, the seller reclaiming the deposit;
  - the bot against [hostile jobs](scripts/testdata/hostile/hostile.gno): one that grows its state at the runner's expense, and one that passes simulation and fails in its block.

- **On Onyx**: rehearsed on 2026-11-27 with four separate accounts (a seller, two patrons, and the bot's key, which also keeps the taproom): 56 checks passed, none failed.
  - Last Orders: two rounds at once; the outbid patron refunded in the same transaction; one round settled by the bot, the other by a patron by hand; the seller receiving the winning bid and reclaiming both deposits.
  - The taproom: its keeper opening it; a patron adding oil; the job run by hand and then by the bot at the next slot, five minutes later; the keeper's `Close` (simulated, since closing is for good).
  - 21 refusals checked by simulation, which costs nothing. Among them: running a job early, twice or after it was cancelled, a stranger opening or closing the taproom, a person cancelling a realm's job or reclaiming the seller's deposit, a bid below the 5% raise or after closing, and wrong fees or round settings.
  - Onyx refuses `maketx run` from this account, so that refusal is covered only by `make smoke`.

Not covered end to end: a job that pays less than simulated without failing, the `-max-loss` budget, and the ban after three losses. Unit tests cover the ledger behind them (loss total, backoff, ban), not the bot's stop at `-max-loss`.

Three properties of the Gno test runtime shape how the unit tests are written: an aborted call does not roll state back (on chain the transaction reverts), bank balances are reset between test functions while realm state is kept, and `testing.SetRealm` inside a helper does not outlive the helper.

## Limits

- **No liveness guarantee.** See the trust model.
- **The reward is fixed per run.** It does not follow the gas price. A job whose reward falls below the cost of running it waits until somebody runs it at a loss or its owner cancels it and posts it again.
- **Racing executors lose their fee.**
- **A callback that always fails blocks its own job**, which keeps its oil until the owner cancels it.
- **Funders cannot withdraw.** Oil is the job's, and only the owner cancels.
- **A job is a few kilobytes of state**, most of it two AVL tree nodes. Posting one locks that deposit until it is cancelled.
- **Interval schedules only.** Cron expressions would need moul's parser, which is not deployed on Onyx.
- **Not audited.** Four review passes found and fixed real problems (listed in [`docs/design.md`](docs/design.md)); that is not an audit.

## Prior art

- [gnolang/gno#455](https://github.com/gnolang/gno/issues/455), "RFC -> external triggers": triggers built into the chain. Lamplighter needs nothing from the chain.
- [`p/moul/x/daily/cron`](https://github.com/moul/gno-contracts/tree/main/p/moul/x/daily/cron): when a schedule next fires. Lamplighter is who fires it. The two would combine well.
- Chainlink Automation, Gelato and CronCat do this job on other chains. What is particular here is how little it takes on gno.land: a stored function value already carries exactly its declaring realm's authority, so there is no forwarder, no proxy, and no calldata.

## Deployment on Onyx

Published from `g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx`. Onyx stores new code inert until its package approver enables it ([package status page](https://gpas.onyx.testnets.gno.land)).

| Package | Submitted in block | Live |
|---|---|---|
| [`.../lamplighter/registry/v2`](https://onyx.testnets.gno.land/r/g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx/lamplighter/registry/v2) | 1366163 | 2026-11-24 |
| [`.../lamplighter/lastorders/v1`](https://onyx.testnets.gno.land/r/g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx/lamplighter/lastorders/v1) | 1366166 | 2026-11-24 |
| [`.../lamplighter/taproom/v3`](https://onyx.testnets.gno.land/r/g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx/lamplighter/taproom/v3) | 1391865 | 2026-11-27 (block 1439465) |

[`scripts/deploy-onyx.sh`](scripts/deploy-onyx.sh) is what published them, and republishes after a testnet reset. The address is a namespace, not a registered name.

Earlier submissions under the same address are superseded and unused. A submitted package cannot be withdrawn: `.../lamplighter/registry`, `.../lamplighter/taproom`, `.../lamplighter/registry/v1`, `.../lamplighter/taproom/v1`, and `.../lamplighter/taproom/v2`, which its keeper has closed.

## Layout

```
gno.land/r/<address>/lamplighter/
  registry/v2/     the registry realm and its dashboard
  taproom/v3/      example: one repeating job
  lastorders/v1/   example: one job per auction round, funded by the realm
bot/               the executor bot (Go)
scripts/           smoke test, deploy script, toolchain bootstrap, hostile test realm
docs/design.md     design notes
```

## License

MIT. See [LICENSE](LICENSE).
