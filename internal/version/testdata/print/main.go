// Command print writes what version.Get reports, for the linker flags test.
package main

import (
	"fmt"
	"time"

	"sarak.sh/mikluko/sniffarr/internal/version"
)

func main() {
	i := version.Get()
	fmt.Println(i.Version, i.Commit, i.Built.Format(time.RFC3339), i.Server())
}
