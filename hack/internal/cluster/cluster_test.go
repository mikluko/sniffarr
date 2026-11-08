package cluster

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseForward(t *testing.T) {
	for line, want := range map[string]string{
		"Forwarding from 127.0.0.1:54321 -> 8080":   "127.0.0.1:54321",
		"Forwarding from 127.0.0.1:54321 -> 8080\r": "127.0.0.1:54321",
		"Forwarding from [::1]:54321 -> 8080":       "",
		"Handling connection for 54321":             "",
		"Forwarding from 127.0.0.1:0 -> 8080":       "",
		"Forwarding from 127.0.0.1:x -> 8080":       "",
		"Forwarding from 127.0.0.1:54321":           "",
	} {
		got, ok := parseForward(line)
		require.Equal(t, want, got, line)
		require.Equal(t, want != "", ok, line)
	}
}

func TestScanForward(t *testing.T) {
	r := strings.NewReader("Forwarding from [::1]:4000 -> 8080\nForwarding from 127.0.0.1:4000 -> 8080\nHandling connection for 4000\n")
	addr := make(chan string, 1)
	scanForward(nopCloser{r}, addr)
	require.Equal(t, "127.0.0.1:4000", <-addr)
}

func TestKubeArgs(t *testing.T) {
	require.Equal(t, []string{"--namespace", "sniffarr", "get", "svc"}, Kube{Namespace: "sniffarr"}.args("get", "svc"))
	require.Equal(t, []string{"--context", "saraksh", "--namespace", "x", "get"}, Kube{Context: "saraksh", Namespace: "x"}.args("get"))
}

type nopCloser struct{ *strings.Reader }

func (nopCloser) Close() error { return nil }
