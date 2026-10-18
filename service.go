package daemon

import (
	"context"
	"errors"
	"fmt"
	"reflect"
)

type starter interface {
	Startup(context.Context) error
}

type stopper interface {
	Shutdown(context.Context) error
}

type registeredService struct {
	name  string
	value any
}

// Register accepts a non-nil pointer to a named struct. Its exported methods,
// except Startup and Shutdown, must have the form
// Method(context.Context, Request) (Response, error), with named struct values.
// Registration validates only; hooks run during Run. Names must be unique.
// Optional Startup(context.Context) error and Shutdown(context.Context) error
// hooks are reserved and are never RPC methods. Startup owns partial cleanup
// on failure. Shutdown hooks share a five-second deadline and must honor it.
// Registration is permanently closed once Run begins.
func (a *App) Register(service any) error {
	a.serviceMu.Lock()
	defer a.serviceMu.Unlock()
	if a.sealed {
		return fmt.Errorf("service registration is closed")
	}
	typ := reflect.TypeOf(service)
	if typ == nil || typ.Kind() != reflect.Pointer || reflect.ValueOf(service).IsNil() || typ.Elem().Kind() != reflect.Struct || typ.Elem().Name() == "" {
		return fmt.Errorf("service must be a non-nil pointer to a named struct")
	}
	name := typ.Elem().Name()
	for _, existing := range a.services {
		if existing.name == name {
			return fmt.Errorf("service %s is already registered", name)
		}
	}
	ctxType, errType := reflect.TypeFor[context.Context](), reflect.TypeFor[error]()
	for i := range typ.NumMethod() {
		method := typ.Method(i)
		signature := method.Type
		if method.Name == "Startup" || method.Name == "Shutdown" {
			if signature.NumIn() != 2 || signature.In(1) != ctxType || signature.NumOut() != 1 || signature.Out(0) != errType || signature.IsVariadic() {
				return fmt.Errorf("%s.%s must have signature (context.Context) error", name, method.Name)
			}
			continue
		}
		if signature.IsVariadic() || signature.NumIn() != 3 || signature.In(1) != ctxType || signature.NumOut() != 2 || signature.Out(1) != errType || !namedStruct(signature.In(2)) || !namedStruct(signature.Out(0)) {
			return fmt.Errorf("%s.%s must have signature (context.Context, Request) (Response, error) with named struct values", name, method.Name)
		}
	}
	a.services = append(a.services, registeredService{name, service})
	return nil
}

func namedStruct(typ reflect.Type) bool {
	return typ.Kind() == reflect.Struct && typ.Name() != ""
}

// Hooks must honor their contexts; arbitrary Go code cannot be forcibly stopped.
// A failed Startup owns its partial cleanup and is not followed by Shutdown.
func (a *App) startServices(ctx context.Context) (started []registeredService, err error) {
	for _, service := range a.services {
		if err := ctx.Err(); err != nil {
			return started, err
		}
		if hook, ok := service.value.(starter); ok {
			startupCtx, cancel := context.WithCancel(ctx)
			err := hook.Startup(startupCtx)
			cancel()
			if err != nil {
				return started, fmt.Errorf("%s.Startup: %w", service.name, err)
			}
		}
		started = append(started, service)
	}
	return started, ctx.Err()
}

func shutdownServices(ctx context.Context, services []registeredService) error {
	var result error
	for i := len(services) - 1; i >= 0; i-- {
		service := services[i]
		if hook, ok := service.value.(stopper); ok {
			if err := hook.Shutdown(ctx); err != nil {
				result = errors.Join(result, fmt.Errorf("%s.Shutdown: %w", service.name, err))
			}
		}
	}
	return errors.Join(result, ctx.Err())
}
