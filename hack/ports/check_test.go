package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/stretchr/testify/require"
)

var (
	bradbury = netip.MustParseAddr("95.168.161.76")
	sniffT   = target{name: "sniff", component: componentSniff, addr: bradbury, udp: 30069, tcp: 30069}
	probe0   = target{name: "probe-0", component: componentProbe, addr: bradbury, udp: 30070, tcp: 30070}
	probe1   = target{name: "probe-1", component: componentProbe, addr: bradbury, udp: 30071, tcp: 30071}
)

func ap(s string) netip.AddrPort { return netip.MustParseAddrPort(s) }

func TestJudgeSniffID(t *testing.T) {
	id := [20]byte{19: 7}
	want := secured(id, bradbury)
	require.NotEqual(t, id, want)

	r := judgeSniffID(sniffT, want, nil, id)
	require.Equal(t, statusOK, r.status, r.detail)
	require.Equal(t, "sniff 95.168.161.76:30069/udp", r.subject)

	r = judgeSniffID(sniffT, id, nil, id)
	require.Equal(t, statusFail, r.status)
	require.Contains(t, r.detail, "without the BEP 42 prefix")

	r = judgeSniffID(sniffT, secured(id, netip.MustParseAddr("203.0.113.9")), nil, id)
	require.Equal(t, statusFail, r.status)
	require.Contains(t, r.detail, "not this release's sniff")

	r = judgeSniffID(sniffT, [20]byte{}, errors.New("timed out"), id)
	require.Equal(t, statusFail, r.status)
	require.Contains(t, r.detail, "no answer to ping: timed out")
}

func TestJudgeContacts(t *testing.T) {
	require.Equal(t, statusSkip, judgeContacts(sniffT, nil).status)
	require.Equal(t, statusOK, judgeContacts(sniffT, []netip.AddrPort{ap("95.168.161.76:30069")}).status)
	r := judgeContacts(sniffT, []netip.AddrPort{ap("95.168.161.76:30069"), ap("95.168.161.76:42069")})
	require.Equal(t, statusFail, r.status)
	require.Contains(t, r.detail, "at 95.168.161.76:42069, not 95.168.161.76:30069")
}

func TestJudgeProbe(t *testing.T) {
	r := judgeProbe(probe0, nil, false, 2, 3)
	require.Equal(t, statusOK, r.status)
	require.Equal(t, "accepts connections, serves 2 of 3 seeds", r.detail)
	require.Equal(t, statusFail, judgeProbe(probe0, errors.New("refused"), false, 0, 3).status)
	require.Equal(t, statusFail, judgeProbe(probe0, nil, true, 0, 3).status)
	split := probe0
	split.tcp = 30099
	require.Contains(t, judgeProbe(split, nil, false, 0, 3).detail, "TCP NodePort 30099 differs from UDP 30070")
}

func TestJudgeSeeds(t *testing.T) {
	probes := []target{probe0, probe1}
	seeds := [][20]byte{hash(1), hash(2), hash(3)}
	rs := judgeSeeds(seeds, [][]netip.AddrPort{
		{ap("95.168.161.76:30071"), ap("203.0.113.9:6881")},
		{ap("95.168.161.76:41173")},
		nil,
	}, [][]target{{probe1}, nil, {probe0}}, probes)
	require.Len(t, rs, 3)
	require.Equal(t, statusOK, rs[0].status)
	require.Equal(t, "announced at 95.168.161.76:30071 (probe-1), served by probe-1", rs[0].detail)
	require.Equal(t, statusFail, rs[1].status)
	require.Contains(t, rs[1].detail, "announced at 95.168.161.76:41173, no probe's NodePort")
	require.Equal(t, statusSkip, rs[2].status)

	rs = judgeSeeds(seeds[2:], [][]netip.AddrPort{{ap("203.0.113.9:6881")}}, [][]target{nil}, probes)
	require.Len(t, rs, 2)
	require.Equal(t, statusSkip, rs[0].status, "other peers are not the probes'")
	require.Equal(t, result{statusFail, "seeds", "none of 1 announced from the probes' addresses"}, rs[1])

	rs = judgeSeeds(nil, nil, nil, probes)
	require.Equal(t, []result{{statusSkip, "seeds", "no seeded probe old enough in the journal"}}, rs)
}

func TestContacts(t *testing.T) {
	target := [20]byte{0xff}
	ni := func(id byte, addr string) krpc.NodeInfo {
		a := ap(addr)
		return krpc.NodeInfo{ID: [20]byte{id}, Addr: krpc.NodeAddr{IP: net.IP(a.Addr().AsSlice()), Port: int(a.Port())}}
	}
	graph := map[netip.AddrPort][]krpc.NodeInfo{
		ap("192.0.2.1:1"): {ni(0x80, "192.0.2.2:1"), ni(0x01, "192.0.2.9:1")},
		ap("192.0.2.2:1"): {ni(0xf0, "192.0.2.3:1")},
		ap("192.0.2.3:1"): {{ID: target, Addr: krpc.NodeAddr{IP: net.IP(bradbury.AsSlice()), Port: 42069}}},
		ap("192.0.2.9:1"): {{ID: target, Addr: krpc.NodeAddr{IP: net.IP(bradbury.AsSlice()), Port: 30069}}},
	}
	find := func(_ context.Context, addr netip.AddrPort, got [20]byte) ([]krpc.NodeInfo, error) {
		require.Equal(t, target, got)
		if nis, ok := graph[addr]; ok {
			return nis, nil
		}
		return nil, errors.New("no reply")
	}
	got := contacts(t.Context(), find, []krpc.NodeInfo{ni(0x10, "192.0.2.1:1")}, target)
	require.Equal(t, []netip.AddrPort{ap("95.168.161.76:30069"), ap("95.168.161.76:42069")}, got)
}

func TestParse(t *testing.T) {
	o, err := parse([]string{"--context", "saraksh", "-n", "x", "--release", "r", "-addr", "95.168.161.76"}, &nopWriter{})
	require.NoError(t, err)
	require.Equal(t, "saraksh", o.context)
	require.Equal(t, "x", o.namespace)
	require.Equal(t, "r", o.release)
	require.Equal(t, bradbury, o.addr)

	o, err = parse([]string{"-context", "dick", "--namespace", "y"}, &nopWriter{})
	require.NoError(t, err)
	require.Equal(t, "dick", o.context)
	require.Equal(t, "y", o.namespace)

	for _, args := range [][]string{{"-addr", "::1"}, {"-seeds", "-1"}, {"extra"}, {"--bogus"}} {
		_, err = parse(args, &nopWriter{})
		require.ErrorIs(t, err, errUsage, args)
	}
}

type nopWriter struct{}

func (nopWriter) Write(b []byte) (int, error) { return len(b), nil }
