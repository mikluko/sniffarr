// Package broker runs the NATS server with JetStream that the API process
// embeds, with the bus topology on it, the API's in-process connection, and
// a listener for the worker user when one is configured.
package broker

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
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
	// Addr is the listen address for workers, port 0 picking a free one;
	// empty listens on none.
	Addr string
	// Password is the password of bus.WorkerUser; required with Addr.
	Password string
}

// Broker is the embedded server and the API's connection to it; Conn and
// JetStream are set from start to stop.
type Broker struct {
	Conn      *nats.Conn
	JetStream jetstream.JetStream
	srv       *server.Server
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

// defaultReady bounds the wait for the server when the start has no deadline.
const defaultReady = time.Minute

// start runs the server, waits for it to take connections, connects to it
// in process and ensures the topology; on error the server is shut down.
func (b *Broker) start(ctx context.Context, cfg Config, log *slog.Logger) (err error) {
	log = log.With("component", "nats")
	password := rand.Text()
	opts, err := options(cfg, password)
	if err != nil {
		return fmt.Errorf("nats: %w", err)
	}
	srv, err := server.NewServer(opts)
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
	b.srv, b.closed = srv, make(chan struct{})
	closed := b.closed
	b.Conn, err = nats.Connect("", nats.InProcessServer(srv), nats.UserInfo(apiUser, password), nats.Name("sniffarr-api"),
		nats.ClosedHandler(func(*nats.Conn) { close(closed) }))
	if err != nil {
		return fmt.Errorf("nats: connect: %w", err)
	}
	if b.JetStream, err = jetstream.New(b.Conn); err != nil {
		return fmt.Errorf("nats: %w", err)
	}
	if err := bus.Ensure(ctx, b.JetStream); err != nil {
		return fmt.Errorf("nats: %w", err)
	}
	attrs := []any{"dir", cfg.Dir}
	if a := srv.Addr(); a != nil {
		attrs = append(attrs, "addr", a.String())
	}
	log.Info("nats", attrs...)
	return nil
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

// ready waits until srv takes connections and runs JetStream, within ctx's
// deadline or defaultReady, failing at once on a fatal error srv logs.
func ready(ctx context.Context, srv *server.Server, fatal <-chan string) error {
	wait := defaultReady
	if d, ok := ctx.Deadline(); ok {
		wait = time.Until(d)
	}
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

// options returns the server's options for cfg: JetStream in cfg.Dir, no
// signal handling, no monitoring port, the API's user in process only, and,
// with cfg.Addr, a listener for bus.WorkerUser limited to the bus's subjects.
func options(cfg Config, apiPassword string) (*server.Options, error) {
	opts := &server.Options{
		ServerName: "sniffarr-api",
		JetStream:  true,
		StoreDir:   cfg.Dir,
		NoSigs:     true,
		DontListen: cfg.Addr == "",
		Users: []*server.User{{
			Username:               apiUser,
			Password:               apiPassword,
			AllowedConnectionTypes: map[string]struct{}{jwt.ConnectionTypeInProcess: {}},
		}},
	}
	if cfg.Addr == "" {
		return opts, nil
	}
	if cfg.Password == "" {
		return nil, errors.New("a listen address without a worker password")
	}
	host, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return nil, err
	}
	if opts.Port, err = strconv.Atoi(port); err != nil {
		return nil, fmt.Errorf("port %q", port)
	}
	if opts.Port == 0 {
		opts.Port = server.RANDOM_PORT
	}
	opts.Host = host
	opts.Users = append(opts.Users, &server.User{
		Username: bus.WorkerUser,
		Password: cfg.Password,
		Permissions: &server.Permissions{
			Publish:   &server.SubjectPermission{Allow: bus.WorkerPublish()},
			Subscribe: &server.SubjectPermission{Allow: bus.WorkerSubscribe()},
		},
		AllowedConnectionTypes: map[string]struct{}{jwt.ConnectionTypeStandard: {}},
	})
	return opts, nil
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
