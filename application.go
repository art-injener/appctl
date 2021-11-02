package appctl

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

type (
	Application struct {
		MainFunc              func(ctx context.Context, holdOn <-chan struct{}) error
		Services              *ServiceKeeper
		TerminationTimeout    time.Duration
		InitializationTimeout time.Duration

		appState int32
		err      error
		holdOn   chan struct{}
		done     chan struct{}
	}
	AppContext struct{}
)

const (
	appStateInit int32 = iota
	appStateReady
	appStateRunning
	appStateHoldOn
	appStateShutdown
	appStateOff
)

const (
	defaultTerminationTimeout    = time.Second
	defaultInitializationTimeout = time.Second * 15
)

func (a *Application) init() error {
	if a.TerminationTimeout == 0 {
		a.TerminationTimeout = defaultTerminationTimeout
	}
	if a.InitializationTimeout == 0 {
		a.InitializationTimeout = defaultInitializationTimeout
	}
	a.holdOn = make(chan struct{})
	a.done = make(chan struct{})
	if a.Services != nil {
		ctx, cancel := context.WithTimeout(a, a.InitializationTimeout)
		defer cancel()
		return a.Services.Init(ctx)
	}
	return nil
}

func (a *Application) run(sig <-chan os.Signal) error {
	var errCh = make(chan error, 3)
	go func() {
		defer func() {
			r := recover()
			if r != nil {
				errCh <- fmt.Errorf("unhandled panic: %v", r)
			}
			close(errCh)
		}()
		if err := a.MainFunc(a, a.holdOn); err != nil {
			errCh <- err
		}
		a.Shutdown()
	}()
	go func() {
		<-sig // wait for os signal
		a.HoldOn()
		// In this mode, the main thread should stop accepting new requests, terminate all current requests, and exit.
		// Exiting the procedure of the main thread will lead to an implicit call Shutdown(),
		// if this does not happen, we will make an explicit call through the shutdown timeout
		<-time.After(a.TerminationTimeout)
		a.Shutdown()
	}()
	select {
	case err, ok := <-errCh:
		if ok && err != nil {
			a.Shutdown()
			return err
		}
	case <-a.done:
		// normal exit
	}
	return nil
}

func (a *Application) checkState(old, new int32) bool {
	return atomic.CompareAndSwapInt32(&a.appState, old, new)
}

func (a *Application) setError(err error) {
	if err == nil {
		return
	}
	if a.checkState(appStateRunning, appStateHoldOn) {
		a.err = err
	}
	a.Shutdown()
}

// Run starts the execution of the main application thread with the MainFunc function.
// Returns an error if the execution of the application ended abnormally, otherwise it will return a nil.
func (a *Application) Run() error {
	if a.MainFunc == nil {
		return ErrMainOmitted
	}
	if a.checkState(appStateInit, appStateRunning) {
		if err := a.init(); err != nil {
			return err
		}
		if a.Services != nil {
			go func() {
				defer a.Shutdown()
				a.setError(a.Services.Watch(a))
			}()
		}
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
		a.setError(a.run(sig))
		return a.err
	}
	return ErrWrongState
}

// HoldOn signals the application to terminate the current computational processes and prepare to stop the application.
func (a *Application) HoldOn() {
	if a.checkState(appStateRunning, appStateHoldOn) {
		close(a.holdOn)
	}
}

// Shutdown stops the application immediately. At this point, all calculations should be completed.
func (a *Application) Shutdown() {
	a.HoldOn()
	if a.checkState(appStateHoldOn, appStateShutdown) {
		close(a.done)
	}
}

// Deadline returns the time when work done on behalf of this context should be canceled.
func (a *Application) Deadline() (deadline time.Time, ok bool) {
	return time.Time{}, false
}

// Done returns a channel that's closed when work done on behalf of this context should be canceled.
func (a *Application) Done() <-chan struct{} {
	return a.done
}

// Err returns error when application is closed.
// If Done is not yet closed, Err returns nil. If Done is closed, Err returns ErrShutdown.
func (a *Application) Err() error {
	if atomic.LoadInt32(&a.appState) == appStateShutdown {
		return ErrShutdown
	}
	return nil
}

// Value returns the Application object.
func (a *Application) Value(key interface{}) interface{} {
	var appContext = AppContext{}
	if key == appContext {
		return a
	}
	return nil
}
