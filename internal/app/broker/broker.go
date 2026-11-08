// Package broker runs the NATS server with JetStream that the API process
// embeds, with the bus topology on it and the API's in-process connection.
// The server listens on no port; it may connect to an upstream server as a
// leaf node.
package broker

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.uber.org/fx"

	"sarak.sh/mikluko/sniffarr/internal/bus"
)

// Config is what Module needs.
type Config struct {
	// Dir is the JetStream store directory, created when missing.
	Dir string
	// Upstream, unless nil, is the leafnode URL of the server the embedded
	// one connects to as a leaf node, with the user name it connects as, if
	// any.
	Upstream *url.URL
	// Password is the password of Upstream's user.
	Password string
	// Creds is the .creds file the leaf node authenticates with, in place
	// of a user and password.
	Creds string
}

// Domain is the JetStream domain of the embedded server, which keeps its
// streams apart from those of an upstream server.
const Domain = "sniffarr"

// Broker is the embedded server and the API's connection to it; Conn and
// JetStream are set from start to stop.
type Broker struct {
	Conn      *nats.Conn
	JetStream jetstream.JetStream
	srv       *server.Server
	password  string
	closed    chan struct{}
}

// Module provides *Broker, whose server starts, with the bus topology
// ensured, at start, and drains and shuts down at stop.
var Module = fx.Module("broker",
	fx.Provide(newBroker),
	fx.Invoke(func(*Broker) {}),
)

func newBroker(lc fx.Lifecycle, cfg Config, log *slog.Logger) *Broker {
	b := &Broker{}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error { return b.start(ctx, cfg, log) },
		OnStop:  b.stop,
	})
	return b
}

// apiUser is the user the API connects as, in process only, with a password
// made at each start.
const apiUser = "api"

// RecoveryBudget bounds the wait at start for the server to recover its
// store and take connections; the start's deadline must leave it that long.
const RecoveryBudget = 5 * time.Minute

// start runs the server, waits for it to take connections, connects to it
// in process and ensures the topology; on error the server is shut down.
func (b *Broker) start(ctx context.Context, cfg Config, log *slog.Logger) (err error) {
	t0 := time.Now()
	log = log.With("component", "nats")
	password := rand.Text()
	srv, err := server.NewServer(options(cfg, password))
	if err != nil {
		return fmt.Errorf("nats: %w", err)
	}
	fatal := make(chan string, 1)
	srv.SetLoggerV2(&logger{log: log, fatal: fatal}, log.Enabled(ctx, slog.LevelDebug), false, false)
	defer func() {
		if err != nil {
			if b.Conn != nil {
				b.Conn.Close()
			}
			srv.Shutdown()
			srv.WaitForShutdown()
		}
	}()
	go srv.Start()
	if err := ready(ctx, srv, fatal); err != nil {
		return fmt.Errorf("nats: %w", err)
	}
	b.srv, b.password, b.closed = srv, password, make(chan struct{})
	closed := b.closed
	b.Conn, err = b.Connect(nats.Name("sniffarr-api"), nats.ClosedHandler(func(*nats.Conn) { close(closed) }))
	if err != nil {
		return fmt.Errorf("nats: connect: %w", err)
	}
	if b.JetStream, err = jetstream.New(b.Conn); err != nil {
		return fmt.Errorf("nats: %w", err)
	}
	if err := bus.Ensure(ctx, b.JetStream); err != nil {
		return fmt.Errorf("nats: %w", err)
	}
	attrs := []any{"dir", cfg.Dir, "start", time.Since(t0).Round(time.Millisecond)}
	if cfg.Upstream != nil {
		attrs = append(attrs, "upstream", cfg.Upstream.Redacted())
	}
	log.Info("nats", attrs...)
	return nil
}

// Connect returns another in-process connection to the server, from start to
// stop, as the API's user; the caller closes it.
func (b *Broker) Connect(opts ...nats.Option) (*nats.Conn, error) {
	return nats.Connect("", append([]nats.Option{nats.InProcessServer(b.srv), nats.UserInfo(apiUser, b.password)}, opts...)...)
}

// stop drains the API's connection, until ctx ends, then shuts the server
// down and waits for it.
func (b *Broker) stop(ctx context.Context) error {
	var err error
	if err = b.Conn.Drain(); err == nil {
		select {
		case <-b.closed:
		case <-ctx.Done():
			err = fmt.Errorf("nats: drain: %w", ctx.Err())
		}
	}
	b.Conn.Close()
	b.srv.Shutdown()
	b.srv.WaitForShutdown()
	return err
}

// ready waits until srv takes connections and runs JetStream, within
// readyWait, failing at once on a fatal error srv logs.
func ready(ctx context.Context, srv *server.Server, fatal <-chan string) error {
	wait := readyWait(ctx)
	done := make(chan bool, 1)
	go func() { done <- srv.ReadyForConnections(wait) }()
	select {
	case msg := <-fatal:
		return errors.New(msg)
	case ok := <-done:
		if !ok {
			return fmt.Errorf("not ready for connections within %s", wait)
		}
	}
	if !srv.JetStreamEnabled() {
		return errors.New("JetStream is not enabled")
	}
	return nil
}

// readyWait is RecoveryBudget, cut short by ctx's deadline.
func readyWait(ctx context.Context) time.Duration {
	if d, ok := ctx.Deadline(); ok {
		return min(RecoveryBudget, time.Until(d))
	}
	return RecoveryBudget
}

// options returns the server's options for cfg: JetStream in cfg.Dir in
// Domain, no listener, no signal handling, the API's user in process only,
// and, with cfg.Upstream, one leaf node remote.
func options(cfg Config, apiPassword string) *server.Options {
	opts := &server.Options{
		ServerName:      "sniffarr-api",
		JetStream:       true,
		JetStreamDomain: Domain,
		StoreDir:        cfg.Dir,
		NoSigs:          true,
		DontListen:      true,
		Users: []*server.User{{
			Username:               apiUser,
			Password:               apiPassword,
			AllowedConnectionTypes: map[string]struct{}{jwt.ConnectionTypeInProcess: {}},
		}},
	}
	if cfg.Upstream != nil {
		u := *cfg.Upstream
		if cfg.Password != "" {
			u.User = url.UserPassword(u.User.Username(), cfg.Password)
		}
		opts.LeafNode.Remotes = []*server.RemoteLeafOpts{{URLs: []*url.URL{&u}, Credentials: cfg.Creds, TLS: u.Scheme == "tls"}}
	}
	return opts
}

// logger routes the server's log to a slog.Logger: notices at Info, debug
// and trace at Debug, and fatal errors at Error, the first of them also sent
// to fatal.
type logger struct {
	log   *slog.Logger
	fatal chan<- string
}

func (l *logger) Noticef(format string, v ...any) { l.log.Info(fmt.Sprintf(format, v...)) }
func (l *logger) Warnf(format string, v ...any)   { l.log.Warn(fmt.Sprintf(format, v...)) }
func (l *logger) Errorf(format string, v ...any)  { l.log.Error(fmt.Sprintf(format, v...)) }
func (l *logger) Debugf(format string, v ...any)  { l.log.Debug(fmt.Sprintf(format, v...)) }
func (l *logger) Tracef(format string, v ...any)  { l.log.Debug(fmt.Sprintf(format, v...)) }

func (l *logger) Fatalf(format string, v ...any) {
	msg := fmt.Sprintf(format, v...)
	l.log.Error(msg)
	select {
	case l.fatal <- msg:
	default:
	}
}
