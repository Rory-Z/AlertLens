package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/emqx/alertlens/internal/alertmanager"
	"github.com/emqx/alertlens/internal/config"
	"github.com/emqx/alertlens/internal/health"
	"github.com/emqx/alertlens/internal/holmes"
	"github.com/emqx/alertlens/internal/service"
	"github.com/emqx/alertlens/internal/session"
	slackadapter "github.com/emqx/alertlens/internal/slack"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv); err != nil {
		slog.Error("alertlens stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	store, err := session.Open(cfg.StatePath, time.Now)
	if err != nil {
		return fmt.Errorf("open state: %w", err)
	}
	slackClient := slackadapter.New(cfg.SlackBotToken, cfg.SlackAppToken, cfg.AlertChannels)
	worker := service.New(
		store,
		alertmanager.New(cfg.AlertmanagerURL, cfg.AlertmanagerTimeout),
		holmes.New(cfg.HolmesURL, cfg.HolmesTimeout),
		slackClient,
		service.Config{
			QueueSize: cfg.EventQueueSize, Workers: cfg.HolmesMaxConcurrency,
			AlertSessionTTL: cfg.AlertSessionTTL, ResolvedSessionTTL: cfg.ResolvedSessionTTL,
			AlertPayloadMaxBytes: cfg.AlertPayloadMaxBytes,
			RunbookMaxBytes:      cfg.RunbookMaxBytes, ConversationMaxBytes: cfg.ConversationMaxBytes,
			SlackOutputMaxChars: cfg.SlackOutputMaxChars,
		},
		time.Now,
	)

	listener, err := net.Listen("tcp", cfg.MetricsAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	if ctx.Err() != nil {
		return listener.Close()
	}
	return serve(ctx, listener, store.Ready, slackClient, worker)
}

type slackRunner interface {
	Run(context.Context, func(context.Context, service.Event) bool) error
	Ready() error
}

type workerRunner interface {
	Submit(context.Context, service.Event) bool
	Run(context.Context)
}

func serve(ctx context.Context, listener net.Listener, storeReady func() error, slackClient slackRunner, worker workerRunner) error {
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	srv := &http.Server{
		Handler: health.New(func() error {
			return ready(storeReady, slackClient.Ready)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serverErr := make(chan error, 1)
	slackErr := make(chan error, 1)
	serviceDone := make(chan struct{})
	go func() { serverErr <- srv.Serve(listener) }()
	go func() { slackErr <- slackClient.Run(runCtx, worker.Submit) }()
	go func() {
		worker.Run(runCtx)
		close(serviceDone)
	}()

	var result error
	serverFinished := false
	slackFinished := false
	select {
	case err := <-serverErr:
		serverFinished = true
		if !errors.Is(err, http.ErrServerClosed) {
			result = fmt.Errorf("serve: %w", err)
		}
	case err := <-slackErr:
		slackFinished = true
		if err != nil {
			result = err
		} else {
			result = errors.New("Slack Socket Mode stopped")
		}
	case <-ctx.Done():
	}
	cancelRun()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	if err := srv.Shutdown(shutdownCtx); err != nil && result == nil {
		result = fmt.Errorf("shutdown: %w", err)
	}
	cancelShutdown()
	if !serverFinished {
		if err := <-serverErr; !errors.Is(err, http.ErrServerClosed) && result == nil {
			result = fmt.Errorf("serve: %w", err)
		}
	}
	if !slackFinished {
		if err := <-slackErr; err != nil && result == nil && ctx.Err() == nil {
			result = err
		}
	}
	<-serviceDone
	return result
}

func ready(checks ...func() error) error {
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}
