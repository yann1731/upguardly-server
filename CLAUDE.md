# Upguardly Backend

Go backend server for Upguardly.

## Build & Run

```bash
go build ./...          # Build all packages
go run .                # Run the server
go test ./...           # Run all tests
go test -v ./...        # Run tests with verbose output
go test -race ./...     # Run tests with race detector
```

## Code Style

- Follow standard Go conventions and idioms
- Use `gofmt` for formatting
- Use `golint` and `go vet` for linting
- Error handling: return errors, don't panic
- Prefer explicit over implicit
- This is the entrypoint to the backend for upguardly
- Api should use the go gin framework
- Api is going to be accessible at api.upguardly.com
- Database used will be postgresql. Default port 5432 will be used
- Convention should be api.upguardly.com/v1/{route}
- Prisma ORM is the method of choice to interact with the database
- The database used is postgresql
- Upguardly's goal is service monitoring and alerting
- Possible monitoring options are http, port or ping
- It should also give insight into service health in the form of latency when monitoring
- Alerting options are email, sms, discord and slack
- Authentication and Authorization framework are to be determined

## Project Structure

```
/cmd        # Application entrypoints
/internal   # Private application code
/pkg        # Public library code (if needed)
/api        # API definitions (OpenAPI, protobuf, etc.)
```
