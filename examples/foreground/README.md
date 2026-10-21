# RPC example

Both commands call the same explicitly registered `clock.now` handler.

From the repository root, run the service in one terminal:

```sh
go run ./examples/foreground
```

In another terminal, compare the two client APIs:

```sh
go run ./examples/foreground now
go run ./examples/foreground now-raw
```

- `now` uses the shared typed contract and returns the response directly:

  ```go
  response, err := client.Invoke(ctx, clockapi.Now, clockapi.NowRequest{})
  ```

- `now-raw` supplies the wire name and response destination explicitly:

  ```go
  var response clockapi.NowResponse
  err := client.Call(ctx, "clock.now", clockapi.NowRequest{}, &response)
  ```

Both require a running service. Press Ctrl+C in the service terminal to shut it down.
