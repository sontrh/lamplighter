// Package hostile posts jobs that try to cost their runner money. It exists
// only for scripts/smoke.sh, which loads it on a throwaway local chain to
// check that the bot protects itself. It is never deployed anywhere else.
package hostile

import (
	"chain/runtime"
	"errors"
	"strings"
	"time"

	"gno.land/r/g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx/lamplighter/registry/v2"
)

var (
	bloatJob uint64
	moodyJob uint64
	ballast  []string
)

func init(cur realm) {
	now := time.Now().Unix()
	bloatJob = registry.Register(cross(cur), "Hostile: grows its state at the runner's expense", now, 10, 5000, bloat)
	moodyJob = registry.Register(cross(cur), "Hostile: fails in odd blocks", now, 10, 20000, moody)
}

// bloat adds 700 bytes of state on every run. The runner pays the storage
// deposit for them, 14 times the job's reward.
func bloat(cur realm, job uint64) error {
	ballast = append(ballast, strings.Repeat("x", 700))
	return nil
}

// moody succeeds or fails depending on the block it runs in, so a simulation
// against one block says nothing certain about the next.
func moody(cur realm, job uint64) error {
	if runtime.ChainHeight()%2 == 1 {
		return errors.New("not in the mood")
	}
	return nil
}

// Exported is a callback anyone can reach, for checking who may register it.
func Exported(cur realm, job uint64) error { return nil }

func BloatJob() uint64 { return bloatJob }
func MoodyJob() uint64 { return moodyJob }
