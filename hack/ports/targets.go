package main

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"sarak.sh/mikluko/sniffarr/hack/internal/cluster"
)

// Components with node ports, as the chart labels them.
const (
	componentSniff = "sniff"
	componentProbe = "probe"
)

// target is one NodePort Service of the release: the node address and the
// ports the internet reaches its pod at.
type target struct {
	// name is the Service's name without the release prefix, such as
	// "sniff" or "probe-1".
	name      string
	component string
	addr      netip.Addr
	udp, tcp  uint16
}

func (t target) udpAddr() netip.AddrPort { return netip.AddrPortFrom(t.addr, t.udp) }
func (t target) tcpAddr() netip.AddrPort { return netip.AddrPortFrom(t.addr, t.tcp) }

type meta struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

type serviceList struct {
	Items []struct {
		Metadata meta `json:"metadata"`
		Spec     struct {
			Type     string            `json:"type"`
			Selector map[string]string `json:"selector"`
			Ports    []struct {
				Protocol string `json:"protocol"`
				NodePort uint16 `json:"nodePort"`
			} `json:"ports"`
		} `json:"spec"`
	} `json:"items"`
}

type podList struct {
	Items []struct {
		Metadata meta `json:"metadata"`
		Spec     struct {
			NodeName string `json:"nodeName"`
		} `json:"spec"`
	} `json:"items"`
}

type nodeAddress struct {
	Type    string `json:"type"`
	Address string `json:"address"`
}

type nodeList struct {
	Items []struct {
		Metadata meta `json:"metadata"`
		Status   struct {
			Addresses []nodeAddress `json:"addresses"`
		} `json:"status"`
	} `json:"items"`
}

// discover reads the release's NodePort Services, their pods and the pods'
// nodes through kubectl and returns the targets.
func discover(ctx context.Context, k cluster.Kube, release string, addr netip.Addr) ([]target, error) {
	sel := "app.kubernetes.io/instance=" + release
	var svcs serviceList
	if err := k.Get(ctx, &svcs, "services", "-l", sel); err != nil {
		return nil, err
	}
	var pods podList
	if err := k.Get(ctx, &pods, "pods", "-l", sel); err != nil {
		return nil, err
	}
	var nodes nodeList
	if !addr.IsValid() {
		if err := k.Get(ctx, &nodes, "nodes"); err != nil {
			return nil, err
		}
	}
	return targets(release, svcs, pods, nodes, addr)
}

// targets pairs every NodePort Service of the sniff or a probe with the
// address of the node its pod runs on, or with addr when it is valid.
func targets(release string, svcs serviceList, pods podList, nodes nodeList, addr netip.Addr) ([]target, error) {
	var out []target
	for _, s := range svcs.Items {
		c := s.Metadata.Labels["app.kubernetes.io/component"]
		if s.Spec.Type != "NodePort" || (c != componentSniff && c != componentProbe) {
			continue
		}
		t := target{name: strings.TrimPrefix(s.Metadata.Name, release+"-"), component: c, addr: addr}
		for _, p := range s.Spec.Ports {
			switch p.Protocol {
			case "UDP":
				t.udp = p.NodePort
			case "TCP", "":
				t.tcp = p.NodePort
			}
		}
		if !t.addr.IsValid() {
			a, err := podAddr(s.Spec.Selector, pods, nodes)
			if err != nil {
				return nil, fmt.Errorf("service %s: %w", s.Metadata.Name, err)
			}
			t.addr = a
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("release %s has no NodePort service of the sniff or a probe", release)
	}
	slices.SortFunc(out, func(a, b target) int { return strings.Compare(a.name, b.name) })
	return out, nil
}

// podAddr is the public address of the node of the one pod selector
// selects.
func podAddr(selector map[string]string, pods podList, nodes nodeList) (netip.Addr, error) {
	var node string
	n := 0
	for _, p := range pods.Items {
		if selects(selector, p.Metadata.Labels) {
			node = p.Spec.NodeName
			n++
		}
	}
	switch {
	case n == 0:
		return netip.Addr{}, fmt.Errorf("no pod")
	case n > 1:
		return netip.Addr{}, fmt.Errorf("%d pods: give -addr", n)
	case node == "":
		return netip.Addr{}, fmt.Errorf("pod not scheduled")
	}
	for _, nd := range nodes.Items {
		if nd.Metadata.Name == node {
			return publicAddr(node, nd.Status.Addresses)
		}
	}
	return netip.Addr{}, fmt.Errorf("node %s not found", node)
}

// publicAddr is the node's ExternalIP, or else its InternalIP, the first
// public IPv4 one of either.
func publicAddr(node string, addrs []nodeAddress) (netip.Addr, error) {
	for _, typ := range []string{"ExternalIP", "InternalIP"} {
		for _, a := range addrs {
			ip, err := netip.ParseAddr(a.Address)
			if a.Type == typ && err == nil && public(ip) {
				return ip, nil
			}
		}
	}
	return netip.Addr{}, fmt.Errorf("node %s has no public IPv4 address: give -addr", node)
}

// public reports whether ip is an IPv4 address the internet routes to.
func public(ip netip.Addr) bool {
	return ip.Is4() && ip.IsGlobalUnicast() && !ip.IsPrivate() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
}

// selects reports whether labels hold every pair of selector.
func selects(selector, labels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}
