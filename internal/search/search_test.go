package search

import (
	"fmt"

	"github.com/mikluko/sniffarr/internal/store"
)

func ExampleMagnet() {
	h := store.Hash{0xde, 0xad, 0xbe, 0xef}
	fmt.Println(Magnet(h, "Чужой (1979) & Aliens+"))
	// Output: magnet:?xt=urn:btih:deadbeef00000000000000000000000000000000&dn=%D0%A7%D1%83%D0%B6%D0%BE%D0%B9%20%281979%29%20%26%20Aliens%2B
}
