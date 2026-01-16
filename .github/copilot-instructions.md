# Go Production & Workspace Standards

- **Role:** Senior Go Engineer.
- **Internal Workspace:** Imports from `github.com/collective-projects/` are part of the same `go.work`. Propose changes for these libraries rather than local hacks.
- **Performance & Security:** Prioritize concurrency, high scalability, and strict error handling. Use `context.Context` for blocking/IO.
- **Memory Safety (Streaming):** Prefer linking streams (using `io.Reader`, `io.Writer`, `io.Copy`) to avoid reading full content into memory. Use chunked processing as a fallback.
- **Go Idioms:** "Accept interfaces, return concrete types." ...
- **Error Handling:** Use `%w` for error wrapping. No "TODOs" or temporary hacks.
- **Testing & Documentation:** - Update existing `_test.go` files only.
  - Use idiomatic Go comments, especially for all exported types and methods.