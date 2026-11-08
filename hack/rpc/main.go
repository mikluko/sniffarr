// Command rpc sends one unary request to the sniffarr API over ConnectRPC,
// at --url or through kubectl port-forward to the API in a Kubernetes
// cluster, and prints the response as protojson.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	_ "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
)

// pkg is the protobuf package of the API, prefixed to short method names.
const pkg = "sniffarr.v1"

// errUsage is returned once the flag package has reported a usage error.
var errUsage = errors.New("usage")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
	case errors.Is(err, errUsage):
		os.Exit(2)
	default:
		fmt.Fprintln(os.Stderr, "rpc:", err)
		os.Exit(1)
	}
}

// options are the command-line flags.
type options struct {
	url       string
	context   string
	namespace string
	release   string
	timeout   time.Duration
	verbose   bool
	list      bool
}

func parse(args []string, stderr io.Writer) (options, []string, error) {
	var o options
	fs := flag.NewFlagSet("rpc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: rpc [flags] <Service>/<Method> [json|-]\n       rpc -l")
		fs.PrintDefaults()
	}
	fs.StringVar(&o.url, "url", "", "base URL of the API, such as http://127.0.0.1:8080; empty port-forwards to the API in Kubernetes")
	fs.StringVar(&o.context, "context", "", "kube context; empty is the current one")
	fs.StringVar(&o.namespace, "namespace", "sniffarr", "namespace of the release")
	fs.StringVar(&o.namespace, "n", "sniffarr", "shorthand for --namespace")
	fs.StringVar(&o.release, "release", "sniffarr", "Helm release name; the API is the Service <release>-api")
	fs.DurationVar(&o.timeout, "timeout", 30*time.Second, "how long to wait for the response")
	fs.BoolVar(&o.verbose, "v", false, "print the method, target URL and timing to stderr")
	fs.BoolVar(&o.list, "l", false, "list the methods")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, nil, err
		}
		return o, nil, errUsage
	}
	return o, fs.Args(), nil
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	o, args, err := parse(args, stderr)
	if err != nil {
		return err
	}
	if o.list || (len(args) == 1 && args[0] == "list") {
		_, err := io.WriteString(stdout, strings.Join(methods(), "\n")+"\n")
		return err
	}
	if len(args) < 1 || len(args) > 2 {
		return errors.New("want <Service>/<Method> and at most one request body; rpc -h for usage")
	}
	md, err := resolve(args[0])
	if err != nil {
		return err
	}
	body, err := requestBody(args[1:], stdin)
	if err != nil {
		return err
	}
	req, err := newRequest(md, body)
	if err != nil {
		return err
	}
	base := o.url
	if base == "" {
		f, err := portForward(ctx, kube{context: o.context, namespace: o.namespace}, o.release+"-api")
		if err != nil {
			return err
		}
		defer f.close()
		base = f.url
	}
	u, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("--url: %w", err)
	}
	target := strings.TrimSuffix(base, "/") + "/" + procedure(md)
	if o.verbose {
		fmt.Fprintf(stderr, "method %s\nurl %s\n", procedure(md), target)
	}
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	start := time.Now()
	resp, err := call(ctx, httpClient(u), target, md, req)
	if o.verbose {
		fmt.Fprintf(stderr, "time %s\n", time.Since(start).Round(time.Millisecond))
	}
	if err != nil {
		return err
	}
	return printResponse(stdout, resp)
}

// procedure is md's Connect procedure path, without the leading slash.
func procedure(md protoreflect.MethodDescriptor) string {
	return string(md.Parent().FullName()) + "/" + string(md.Name())
}

// methods lists the procedures of pkg, sorted, marking the streaming ones.
func methods() []string {
	var out []string
	protoregistry.GlobalFiles.RangeFilesByPackage(pkg, func(fd protoreflect.FileDescriptor) bool {
		for i := range fd.Services().Len() {
			ms := fd.Services().Get(i).Methods()
			for j := range ms.Len() {
				md := ms.Get(j)
				line := procedure(md)
				if s := streaming(md); s != "" {
					line += " (" + s + ")"
				}
				out = append(out, line)
			}
		}
		return true
	})
	slices.Sort(out)
	return out
}

// streaming names md's kind of streaming, or "" for a unary method.
func streaming(md protoreflect.MethodDescriptor) string {
	switch {
	case md.IsStreamingClient() && md.IsStreamingServer():
		return "bidi streaming"
	case md.IsStreamingClient():
		return "client streaming"
	case md.IsStreamingServer():
		return "server streaming"
	}
	return ""
}

// resolve finds the unary method name names, as Service/Method in pkg or as
// package.Service/Method.
func resolve(name string) (protoreflect.MethodDescriptor, error) {
	svc, method, ok := strings.Cut(strings.TrimPrefix(name, "/"), "/")
	if !ok || svc == "" || method == "" {
		return nil, fmt.Errorf("%q: want <Service>/<Method>", name)
	}
	if !strings.Contains(svc, ".") {
		svc = pkg + "." + svc
	}
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(svc))
	if err != nil {
		return nil, fmt.Errorf("unknown service %s; rpc -l lists the methods", svc)
	}
	sd, ok := d.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, fmt.Errorf("%s is not a service", svc)
	}
	md := sd.Methods().ByName(protoreflect.Name(method))
	if md == nil {
		return nil, fmt.Errorf("unknown method %s/%s; rpc -l lists the methods", svc, method)
	}
	if s := streaming(md); s != "" {
		return nil, fmt.Errorf("%s is %s: rpc calls unary methods only", procedure(md), s)
	}
	return md, nil
}

// requestBody is the argument, or stdin when it is "-" or absent; an absent
// argument with stdin on a terminal is the empty body.
func requestBody(args []string, stdin io.Reader) ([]byte, error) {
	if len(args) == 1 && args[0] != "-" {
		return []byte(args[0]), nil
	}
	if len(args) == 0 {
		if f, ok := stdin.(*os.File); ok && terminal(f) {
			return nil, nil
		}
	}
	b, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("stdin: %w", err)
	}
	return b, nil
}

// terminal reports whether f is a character device.
func terminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// newRequest decodes body as protojson into md's input message; a blank body
// is the zero message.
func newRequest(md protoreflect.MethodDescriptor, body []byte) (*dynamicpb.Message, error) {
	m := dynamicpb.NewMessage(md.Input())
	if strings.TrimSpace(string(body)) == "" {
		return m, nil
	}
	if err := protojson.Unmarshal(body, m); err != nil {
		return nil, fmt.Errorf("request %s: %w", md.Input().FullName(), err)
	}
	return m, nil
}

// httpClient speaks HTTP/2 without TLS to an http:// URL, as the API serves.
func httpClient(u *url.URL) *http.Client {
	if u.Scheme != "http" {
		return &http.Client{}
	}
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: &http.Transport{Protocols: &p}}
}

// call sends req to the unary method md at target, the full procedure URL.
func call(ctx context.Context, hc *http.Client, target string, md protoreflect.MethodDescriptor, req *dynamicpb.Message) (*dynamicpb.Message, error) {
	c := connect.NewClient[dynamicpb.Message, dynamicpb.Message](hc, target,
		connect.WithSchema(md),
		connect.WithResponseInitializer(func(_ connect.Spec, msg any) error {
			m, ok := msg.(*dynamicpb.Message)
			if !ok {
				return fmt.Errorf("response is %T, want *dynamicpb.Message", msg)
			}
			*m = *dynamicpb.NewMessage(md.Output())
			return nil
		}),
	)
	resp, err := c.CallUnary(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func printResponse(w io.Writer, m *dynamicpb.Message) error {
	b, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(m)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}
