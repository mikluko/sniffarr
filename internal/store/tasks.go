package store

import "time"

// Kinds name what a task asks for.
const (
	// TaskFetch asks for a sighted hash's metadata.
	TaskFetch = "fetch"
	// TaskPeers asks for a stored release's peers on the DHT.
	TaskPeers = "peers"
	// TaskProbe asks for the tracks of a stored release's main video file.
	TaskProbe = "probe"
)

// Discoveries name how a hash was sighted, in the order its fetch is served.
const (
	Arr      = "arr"
	Announce = "announce"
	Sample   = "sample"
)

// Priorities order the tasks of each kind, P0 first. A fetch takes its
// discovery's; a peer lookup or a probe asked for in the web UI takes
// PriorityRequest, a probe of a release a search returned PriorityArr, and
// any other probe PrioritySample.
const (
	PriorityRequest  = 0
	PriorityArr      = 1
	PriorityAnnounce = 2
	PrioritySample   = 3
)

// Sighting is one report of a hash seen on the DHT.
type Sighting struct {
	Hash      Hash
	Discovery string
}

// Dead-set durations.
const (
	DefaultDeadFor    = 7 * 24 * time.Hour
	DefaultNoPeersFor = 24 * time.Hour
)

// PriorityOf returns the priority of a fetch of a hash sighted by the
// discovery.
func PriorityOf(discovery string) int {
	switch discovery {
	case Arr:
		return PriorityArr
	case Announce:
		return PriorityAnnounce
	}
	return PrioritySample
}

// DiscoveryOf returns the discovery a fetch at the priority serves.
func DiscoveryOf(priority int) string {
	switch {
	case priority <= PriorityArr:
		return Arr
	case priority == PriorityAnnounce:
		return Announce
	}
	return Sample
}
