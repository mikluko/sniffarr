package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
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

// kube names the kubectl one context and namespace.
type kube struct {
	context   string
	namespace string
}

// args prefixes args with the context and namespace flags.
func (k kube) args(args ...string) []string {
	var out []string
	if k.context != "" {
		out = append(out, "--context", k.context)
	}
	return append(append(out, "--namespace", k.namespace), args...)
}

// forward is a running kubectl port-forward listening at url.
type forward struct {
	url    string
	cmd    *exec.Cmd
	exited chan error
}

// portForward starts kubectl port-forward to service on a free local port
// and returns once it listens; the caller must close it.
func portForward(ctx context.Context, k kube, service string) (*forward, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd := exec.Command("kubectl", k.args("port-forward", "svc/"+service, ":"+apiPort)...)
	cmd.Stdout = pw
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, fmt.Errorf("kubectl port-forward: %w", err)
	}
	pw.Close()
	addr := make(chan string, 1)
	go scanForward(pr, addr)
	f := &forward{cmd: cmd, exited: make(chan error, 1)}
	go func() { f.exited <- cmd.Wait() }()
	timer := time.NewTimer(forwardTimeout)
	defer timer.Stop()
	select {
	case a := <-addr:
		f.url = "http://" + a
		return f, nil
	case err := <-f.exited:
		f.exited <- err
		return nil, fmt.Errorf("kubectl port-forward svc/%s: %v: %s", service, err, bytes.TrimSpace(stderr.Bytes()))
	case <-timer.C:
		f.close()
		return nil, fmt.Errorf("kubectl port-forward svc/%s: not listening after %s", service, forwardTimeout)
	case <-ctx.Done():
		f.close()
		return nil, ctx.Err()
	}
}

// close stops kubectl and waits for it to exit.
func (f *forward) close() {
	_ = f.cmd.Process.Kill()
	<-f.exited
}

// scanForward sends the first IPv4 address r reports listening on, then
// drains r so that kubectl never blocks writing to it.
func scanForward(r io.ReadCloser, addr chan<- string) {
	defer r.Close()
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
