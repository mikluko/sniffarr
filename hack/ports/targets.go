package main

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"sarak.sh/mikluko/sniffarr/hack/internal/cluster"
)

// Components reachable from outside, as the chart labels them.
const (
	componentSniff = "sniff"
	componentProbe = "probe"
)

// target is one pod of the release reachable from outside, through a NodePort
// Service or a hostPort: the node address and the ports the internet reaches
// it at.
type target struct {
	// name is the Service's name without the release prefix, such as
	// "sniff" or "probe-1", or for a hostPort the component's name.
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

type pod struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		NodeName   string `json:"nodeName"`
		Containers []struct {
			Ports []struct {
				Protocol string `json:"protocol"`
				HostPort uint16 `json:"hostPort"`
			} `json:"ports"`
		} `json:"containers"`
	} `json:"spec"`
}

type podList struct {
	Items []pod `json:"items"`
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

// discover reads the release's Services, its pods and their nodes through
// kubectl and returns the targets.
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

// targets pairs every NodePort Service of the sniff or a probe, and every
// pod of theirs with a hostPort that no such Service selects, with the
// address of the node the pod runs on, or with addr when it is valid.
func targets(release string, svcs serviceList, pods podList, nodes nodeList, addr netip.Addr) ([]target, error) {
	out, err := serviceTargets(release, svcs, pods, nodes, addr)
	if err != nil {
		return nil, err
	}
	hosted, err := hostPortTargets(svcs, pods, nodes, addr)
	if err != nil {
		return nil, err
	}
	out = append(out, hosted...)
	if len(out) == 0 {
		return nil, fmt.Errorf("release %s has no NodePort Service or hostPort of the sniff or a probe", release)
	}
	slices.SortFunc(out, func(a, b target) int { return strings.Compare(a.name, b.name) })
	return out, nil
}

// reachable reports whether c is the sniff or the probe.
func reachable(c string) bool { return c == componentSniff || c == componentProbe }

// serviceTargets returns a target per NodePort Service of the sniff or a
// probe.
func serviceTargets(release string, svcs serviceList, pods podList, nodes nodeList, addr netip.Addr) ([]target, error) {
	var out []target
	for _, s := range svcs.Items {
		c := s.Metadata.Labels["app.kubernetes.io/component"]
		if s.Spec.Type != "NodePort" || !reachable(c) {
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
	return out, nil
}

// hostPortTargets returns a target per pod of the sniff or a probe that
// publishes a hostPort and that no NodePort Service selects, named by its
// component; two such pods of one component are an error.
func hostPortTargets(svcs serviceList, pods podList, nodes nodeList, addr netip.Addr) ([]target, error) {
	var out []target
	seen := map[string]bool{}
	for _, p := range pods.Items {
		c := p.Metadata.Labels["app.kubernetes.io/component"]
		if !reachable(c) || nodePorted(p, svcs) {
			continue
		}
		t := target{name: c, component: c, addr: addr}
		t.udp, t.tcp = hostPorts(p)
		if t.udp == 0 && t.tcp == 0 {
			continue
		}
		if seen[c] {
			return nil, fmt.Errorf("%s: more than one pod with a hostPort", c)
		}
		seen[c] = true
		if !t.addr.IsValid() {
			a, err := nodeAddr(p.Spec.NodeName, nodes)
			if err != nil {
				return nil, fmt.Errorf("pod %s: %w", p.Metadata.Name, err)
			}
			t.addr = a
		}
		out = append(out, t)
	}
	return out, nil
}

// hostPorts returns the UDP and TCP hostPorts of p's containers, 0 for none.
func hostPorts(p pod) (udp, tcp uint16) {
	for _, ct := range p.Spec.Containers {
		for _, cp := range ct.Ports {
			switch {
			case cp.HostPort == 0:
			case cp.Protocol == "UDP":
				udp = cp.HostPort
			case cp.Protocol == "TCP", cp.Protocol == "":
				tcp = cp.HostPort
			}
		}
	}
	return udp, tcp
}

// nodePorted reports whether a NodePort Service selects p.
func nodePorted(p pod, svcs serviceList) bool {
	for _, s := range svcs.Items {
		if s.Spec.Type == "NodePort" && selects(s.Spec.Selector, p.Metadata.Labels) {
			return true
		}
	}
	return false
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
	}
	return nodeAddr(node, nodes)
}

// nodeAddr is the public address of the named node.
func nodeAddr(node string, nodes nodeList) (netip.Addr, error) {
	if node == "" {
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
