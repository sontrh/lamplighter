package main

import (
	"gno.land/r/g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx/lamplighter/registry/v2"
	"gno.land/r/test/hostile"
)

// A script run with `gnokey maketx run` is not a deployed realm: the registry
// must refuse to take a job from it.
func main(cur realm) {
	registry.Register(cross(cur), "from a script", 0, 60, registry.MinReward, hostile.Exported)
}
