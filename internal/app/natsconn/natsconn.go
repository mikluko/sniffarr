// Package natsconn provides the NATS connection a worker publishes on: the
// API's own when the API runs in this process, else one to the API's server
// at a URL, or none.
package natsconn

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"
	"go.uber.org/fx"

	"sarak.sh/mikluko/sniffarr/internal/app/broker"
	"sarak.sh/mikluko/sniffarr/internal/bus"
)

// Config is what Module needs.
type Config struct {
	// URL is the API's NATS server, reached as bus.WorkerUser; empty reaches
	// none unless the API runs in this process.
	URL string
	// Password is bus.WorkerUser's at URL.
	Password string
}

// Conn is a worker's NATS connection.
type Conn struct {
	broker *broker.Broker
	nc     *nats.Conn
}

// NATS returns the connection, set from start to stop; nil when there is
// none.
func (c *Conn) NATS() *nats.Conn {
	if c.broker != nil {
		return c.broker.Conn
	}
	return c.nc
}

// Module provides *Conn. A connection to Config.URL is made at start,
// retried in the background while the server is unreachable, and drained at
// stop.
var Module = fx.Module("natsconn", fx.Provide(newConn))

type params struct {
	fx.In
	Lifecycle fx.Lifecycle
	Config    Config
	Log       *slog.Logger
	// Broker is the API's embedded server when the API runs in this
	// process.
	Broker *broker.Broker `optional:"true"`
}

func newConn(p params) *Conn {
	c := &Conn{broker: p.Broker}
	if p.Broker != nil || p.Config.URL == "" {
		return c
	}
	log := p.Log.With("component", "nats")
	closed := make(chan struct{})
	p.Lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			nc, err := nats.Connect(p.Config.URL,
				nats.UserInfo(bus.WorkerUser, p.Config.Password),
				nats.CustomInboxPrefix(bus.WorkerInbox),
				nats.Name("sniffarr-worker"),
				nats.RetryOnFailedConnect(true),
				nats.MaxReconnects(-1),
				nats.ConnectHandler(func(nc *nats.Conn) { log.Info("nats connected", "url", nc.ConnectedUrlRedacted()) }),
				nats.ReconnectHandler(func(nc *nats.Conn) { log.Info("nats reconnected", "url", nc.ConnectedUrlRedacted()) }),
				nats.DisconnectErrHandler(func(_ *nats.Conn, err error) { log.Warn("nats disconnected", "err", err) }),
				nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { log.Warn("nats", "err", err) }),
				nats.ClosedHandler(func(*nats.Conn) { close(closed) }),
			)
			if err != nil {
				return fmt.Errorf("nats: %w", err)
			}
			c.nc = nc
			return nil
		},
		OnStop: func(ctx context.Context) error {
			if err := c.nc.Drain(); err != nil {
				c.nc.Close()
				return nil
			}
			select {
			case <-closed:
				return nil
			case <-ctx.Done():
				c.nc.Close()
				return fmt.Errorf("nats: drain: %w", ctx.Err())
			}
		},
	})
	return c
}
