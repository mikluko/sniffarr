package main

import (
	"encoding/json"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

const servicesJSON = `{"items":[
 {"metadata":{"name":"sniffarr-api","labels":{"app.kubernetes.io/component":"api"}},
  "spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/component":"api"},"ports":[{"protocol":"TCP"}]}},
 {"metadata":{"name":"sniffarr-probe","labels":{"app.kubernetes.io/component":"probe"}},
  "spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/component":"probe"}}},
 {"metadata":{"name":"sniffarr-probe-1","labels":{"app.kubernetes.io/component":"probe"}},
  "spec":{"type":"NodePort","selector":{"app.kubernetes.io/component":"probe","statefulset.kubernetes.io/pod-name":"sniffarr-probe-1"},
   "ports":[{"protocol":"TCP","nodePort":30071},{"protocol":"UDP","nodePort":30071}]}},
 {"metadata":{"name":"sniffarr-sniff","labels":{"app.kubernetes.io/component":"sniff"}},
  "spec":{"type":"NodePort","selector":{"app.kubernetes.io/component":"sniff"},
   "ports":[{"protocol":"TCP","nodePort":30069},{"protocol":"UDP","nodePort":30069}]}}
]}`

const podsJSON = `{"items":[
 {"metadata":{"name":"sniffarr-sniff-x","labels":{"app.kubernetes.io/component":"sniff"}},"spec":{"nodeName":"bradbury"}},
 {"metadata":{"name":"sniffarr-probe-0","labels":{"app.kubernetes.io/component":"probe","statefulset.kubernetes.io/pod-name":"sniffarr-probe-0"}},"spec":{"nodeName":"bradbury"}},
 {"metadata":{"name":"sniffarr-probe-1","labels":{"app.kubernetes.io/component":"probe","statefulset.kubernetes.io/pod-name":"sniffarr-probe-1"}},"spec":{"nodeName":"hetzner"}}
]}`

const nodesJSON = `{"items":[
 {"metadata":{"name":"bradbury"},"status":{"addresses":[{"type":"InternalIP","address":"95.168.161.76"},{"type":"Hostname","address":"bradbury"}]}},
 {"metadata":{"name":"hetzner"},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.5"},{"type":"ExternalIP","address":"203.0.113.9"}]}}
]}`

func decode[T any](t *testing.T, s string) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal([]byte(s), &v))
	return v
}

func TestTargets(t *testing.T) {
	svcs, pods, nodes := decode[serviceList](t, servicesJSON), decode[podList](t, podsJSON), decode[nodeList](t, nodesJSON)
	ts, err := targets("sniffarr", svcs, pods, nodes, netip.Addr{})
	require.NoError(t, err)
	require.Equal(t, []target{
		{name: "probe-1", component: componentProbe, addr: netip.MustParseAddr("203.0.113.9"), udp: 30071, tcp: 30071},
		{name: "sniff", component: componentSniff, addr: netip.MustParseAddr("95.168.161.76"), udp: 30069, tcp: 30069},
	}, ts)

	ts, err = targets("sniffarr", svcs, pods, nodeList{}, netip.MustParseAddr("198.51.100.1"))
	require.NoError(t, err)
	for _, tg := range ts {
		require.Equal(t, netip.MustParseAddr("198.51.100.1"), tg.addr, tg.name)
	}
}

// hostPortPodsJSON has the sniff on a hostPort with no Service, a probe pod
// on a hostPort that its NodePort Service selects as well, and a fetch pod
// on a hostPort, which is not checked.
const hostPortPodsJSON = `{"items":[
 {"metadata":{"name":"sniffarr-sniff-x","labels":{"app.kubernetes.io/component":"sniff"}},
  "spec":{"nodeName":"bradbury","containers":[{"ports":[
   {"protocol":"UDP","containerPort":42069,"hostPort":42069},{"protocol":"TCP","containerPort":42069,"hostPort":42069},
   {"protocol":"TCP","containerPort":9464}]}]}},
 {"metadata":{"name":"sniffarr-probe-1","labels":{"app.kubernetes.io/component":"probe","statefulset.kubernetes.io/pod-name":"sniffarr-probe-1"}},
  "spec":{"nodeName":"hetzner","containers":[{"ports":[{"protocol":"UDP","hostPort":42070}]}]}},
 {"metadata":{"name":"sniffarr-fetch-x","labels":{"app.kubernetes.io/component":"fetch"}},
  "spec":{"nodeName":"bradbury","containers":[{"ports":[{"protocol":"UDP","hostPort":42071}]}]}}
]}`

func TestTargetsHostPort(t *testing.T) {
	svcs := decode[serviceList](t, `{"items":[
	 {"metadata":{"name":"sniffarr-probe-1","labels":{"app.kubernetes.io/component":"probe"}},
	  "spec":{"type":"NodePort","selector":{"app.kubernetes.io/component":"probe","statefulset.kubernetes.io/pod-name":"sniffarr-probe-1"},
	   "ports":[{"protocol":"TCP","nodePort":30071},{"protocol":"UDP","nodePort":30071}]}}]}`)
	pods, nodes := decode[podList](t, hostPortPodsJSON), decode[nodeList](t, nodesJSON)
	ts, err := targets("sniffarr", svcs, pods, nodes, netip.Addr{})
	require.NoError(t, err)
	require.Equal(t, []target{
		{name: "probe-1", component: componentProbe, addr: netip.MustParseAddr("203.0.113.9"), udp: 30071, tcp: 30071},
		{name: "sniff", component: componentSniff, addr: netip.MustParseAddr("95.168.161.76"), udp: 42069, tcp: 42069},
	}, ts)

	ts, err = targets("sniffarr", serviceList{}, pods, nodeList{}, netip.MustParseAddr("198.51.100.1"))
	require.NoError(t, err)
	require.Len(t, ts, 2)
	for _, tg := range ts {
		require.Equal(t, netip.MustParseAddr("198.51.100.1"), tg.addr, tg.name)
	}
}

func TestTargetsErrors(t *testing.T) {
	svcs, pods, nodes := decode[serviceList](t, servicesJSON), decode[podList](t, podsJSON), decode[nodeList](t, nodesJSON)

	_, err := targets("sniffarr", serviceList{}, pods, nodes, netip.Addr{})
	require.ErrorContains(t, err, "no NodePort Service or hostPort")

	rolling := decode[podList](t, `{"items":[
	 {"metadata":{"name":"a","labels":{"app.kubernetes.io/component":"sniff"}},"spec":{"nodeName":"bradbury","containers":[{"ports":[{"protocol":"UDP","hostPort":42069}]}]}},
	 {"metadata":{"name":"b","labels":{"app.kubernetes.io/component":"sniff"}},"spec":{"nodeName":"bradbury","containers":[{"ports":[{"protocol":"UDP","hostPort":42069}]}]}}]}`)
	_, err = targets("sniffarr", serviceList{}, rolling, nodes, netip.Addr{})
	require.ErrorContains(t, err, "sniff: more than one pod with a hostPort")

	private := decode[nodeList](t, `{"items":[{"metadata":{"name":"bradbury"},"status":{"addresses":[{"type":"InternalIP","address":"10.1.2.3"}]}},
	 {"metadata":{"name":"hetzner"},"status":{"addresses":[{"type":"ExternalIP","address":"203.0.113.9"}]}}]}`)
	_, err = targets("sniffarr", svcs, pods, private, netip.Addr{})
	require.ErrorContains(t, err, "service sniffarr-sniff: node bradbury has no public IPv4 address: give -addr")

	two := decode[podList](t, `{"items":[
	 {"metadata":{"name":"a","labels":{"app.kubernetes.io/component":"sniff"}},"spec":{"nodeName":"bradbury"}},
	 {"metadata":{"name":"b","labels":{"app.kubernetes.io/component":"sniff"}},"spec":{"nodeName":"bradbury"}},
	 {"metadata":{"name":"sniffarr-probe-1","labels":{"app.kubernetes.io/component":"probe","statefulset.kubernetes.io/pod-name":"sniffarr-probe-1"}},"spec":{"nodeName":"hetzner"}}]}`)
	_, err = targets("sniffarr", svcs, two, nodes, netip.Addr{})
	require.ErrorContains(t, err, "service sniffarr-sniff: 2 pods")
}

func TestPublic(t *testing.T) {
	for s, want := range map[string]bool{
		"95.168.161.76": true,
		"10.0.0.1":      false,
		"192.168.1.1":   false,
		"100.64.0.1":    false,
		"127.0.0.1":     false,
		"2001:db8::1":   false,
	} {
		require.Equal(t, want, public(netip.MustParseAddr(s)), s)
	}
}
