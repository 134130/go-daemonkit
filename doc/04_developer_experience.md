# 04. Developer Experience

## Typed RPC contracts

- Shared contract packages declare public DTOs and `NewMethod[Request, Response]("stable.name")`.
- Wire names are explicit and independent of Go packages, type names, and implementation method names.
- `App.Register(service)` manages optional Startup and Shutdown hooks only. Other methods are unrestricted and never exposed automatically.
- `App.Handle(method, handler)` explicitly exposes a function with signature `func(context.Context, Request) (Response, error)`. Go 1.27 generic methods check the handler against the contract.
- Handlers do not require a lifecycle participant. Register their owner separately when hooks are needed.
- `Client.Invoke(ctx, method, request)` infers the request and response types. Errors return the response's zero value.
- Request and response DTOs are named struct values. Both may be reused across operations.
- Handle and Invoke validate contracts. Empty or whitespace-padded names, control methods, and the `$/` and `rpc.` prefixes are rejected.
- Handle rejects nil handlers and duplicate wire names. Register and Handle are permanently closed when Run begins.
- The raw `Client.Call(ctx, name, request, responsePointer)` remains available.
- Applications own JSON field compatibility and business validation. The framework does not derive routing from Go types or negotiate DTO schemas.
- Contract declarations require no generator.
