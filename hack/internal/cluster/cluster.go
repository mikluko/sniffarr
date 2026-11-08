// Package cluster reaches a sniffarr release in Kubernetes from a
// workstation, through kubectl, for the commands under hack.
package cluster

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// apiPort is the port of the API's Service in the chart.
const apiPort = "8080"

// forwardTimeout bounds how long kubectl port-forward takes to listen.
const forwardTimeout = 30 * time.Second

// Kube names the kube context, the current one when empty, and the
// namespace kubectl runs in.
type Kube struct {
	Context   string
	Namespace string
}

// args prefixes args with the context and namespace flags.
func (k Kube) args(args ...string) []string {
	var out []string
	if k.Context != "" {
		out = append(out, "--context", k.Context)
	}
	return append(append(out, "--namespace", k.Namespace), args...)
}

// Get runs kubectl get with args and decodes its JSON output into v.
func (k Kube) Get(ctx context.Context, v any, args ...string) error {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "kubectl", k.args(append(append([]string{"get"}, args...), "-o", "json")...)...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl get %s: %v: %s", strings.Join(args, " "), err, bytes.TrimSpace(stderr.Bytes()))
	}
	if err := json.Unmarshal(stdout.Bytes(), v); err != nil {
		return fmt.Errorf("kubectl get %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// Forward is a running kubectl port-forward listening at URL.
type Forward struct {
	URL    string
	cmd    *exec.Cmd
	exited chan error
}

// PortForward starts kubectl port-forward to the API's service on a free
// local port and returns once it listens; the caller must close it.
func PortForward(ctx context.Context, k Kube, service string) (*Forward, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd := exec.Command("kubectl", k.args("port-forward", "svc/"+service, ":"+apiPort)...)
	cmd.Stdout = pw
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return nil, fmt.Errorf("kubectl port-forward: %w", err)
	}
	_ = pw.Close()
	addr := make(chan string, 1)
	go scanForward(pr, addr)
	f := &Forward{cmd: cmd, exited: make(chan error, 1)}
	go func() { f.exited <- cmd.Wait() }()
	timer := time.NewTimer(forwardTimeout)
	defer timer.Stop()
	select {
	case a := <-addr:
		f.URL = "http://" + a
		return f, nil
	case err := <-f.exited:
		f.exited <- err
		return nil, fmt.Errorf("kubectl port-forward svc/%s: %v: %s", service, err, bytes.TrimSpace(stderr.Bytes()))
	case <-timer.C:
		f.Close()
		return nil, fmt.Errorf("kubectl port-forward svc/%s: not listening after %s", service, forwardTimeout)
	case <-ctx.Done():
		f.Close()
		return nil, ctx.Err()
	}
}

// Close stops kubectl and waits for it to exit.
func (f *Forward) Close() {
	_ = f.cmd.Process.Kill()
	<-f.exited
}

// scanForward sends the first IPv4 address r reports listening on, then
// drains r so that kubectl never blocks writing to it.
func scanForward(r io.ReadCloser, addr chan<- string) {
	defer func() { _ = r.Close() }()
	s := bufio.NewScanner(r)
	for s.Scan() {
		if a, ok := parseForward(s.Text()); ok {
			addr <- a
			break
		}
	}
	_, _ = io.Copy(io.Discard, r)
}

// parseForward returns the local address of a kubectl port-forward line
// such as "Forwarding from 127.0.0.1:54321 -> 8080".
func parseForward(line string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Forwarding from ")
	if !ok {
		return "", false
	}
	a, _, ok := strings.Cut(rest, " -> ")
	if !ok {
		return "", false
	}
	host, port, err := net.SplitHostPort(a)
	if err != nil || host != "127.0.0.1" {
		return "", false
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", false
	}
	return a, true
}

// HTTPClient speaks HTTP/2 without TLS to an http:// URL, as the API serves.
func HTTPClient(u *url.URL) *http.Client {
	if u.Scheme != "http" {
		return &http.Client{}
	}
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: &http.Transport{Protocols: &p}}
}
