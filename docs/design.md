# Design notes

What the [README](../README.md) does not already cover: the decisions inside the code, what the reviews found, and what is still open.

## Out of scope, on purpose

A staked keeper network with penalties, a token of its own, and cron expressions.

## Registry

- **Effects before the callback.** `Exec` charges and reschedules the job before it calls the callback, so nothing the callback does can run the job twice.
- **Reentrancy.** While a callback runs, `Exec` and `Credit` are refused. `Cancel` is allowed only for the running job itself: a job may cancel itself from its callback, and that run is still paid.
- **Due index.** `DueJobs` reads a separate index ordered by due time, so a query costs in proportion to the jobs it returns, not to the number of jobs. A job stays in the index for its whole life under one of two keys of the same length, due or parked. That is also why funding and running a job barely change the size of the state.
- **Oil.** Only ugnot is accepted; any other coin would be stranded. `Credit` takes an explicit amount, so coins somebody else left at the registry's address cannot change what a call credits.
- **Names.** Up to 80 bytes, no control characters, and not only invisible ones: bidi and zero-width characters are stripped before that check.
- **Dashboard.** Jobs are listed 20 to a page.

## Bot

- Each pass reads the gas price and the account once, then pages through `DueJobs` and picks at most `-max-jobs` (default 20).
- At startup it checks that the node serves the `-chain-id` it signs for. Simulation skips the signature check, so a wrong chain id would otherwise show only as every broadcast being refused.
- Text returned by the chain is cleaned and shortened before it is logged.
- Decrypting the key takes about 0.34 s per signature on the test machine, and each job is signed twice: once to simulate, once to send.

## What the reviews found

Four review passes: the first registry, the bot and scripts, the whole entry against the hackathon criteria, and the current registry before it was published. Fixed as a result:

- impersonation through an exported callback;
- integer overflow in the schedule;
- a `maketx run` script able to register a job;
- unbounded iteration over the jobs;
- oil that could be stuck for good;
- the bot sending its whole deposit limit with the real transaction;
- the bot forgetting jobs that had cost it money;
- `Credit` blocked by a single stray ugnot;
- storage deposits flowing to the wrong party (see the README);
- the deploy script able to pay for a package twice.

## Open

1. Cron schedules. Moul's `p/moul/x/daily/cron` is not deployed on Onyx, so it cannot be imported yet.
2. Racing between bots. The slower of two bots running the same job pays a fee for nothing.
3. Size. A job is a few kilobytes of state, mostly two AVL tree nodes.
