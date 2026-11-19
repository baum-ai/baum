This pull request changes an interface other code calls. Check, and raise a risk anchored on the registration or type for each that holds:

- A removed or renamed route, tool, command, flag or export breaks every caller that still uses it, including clients released earlier that cannot update with the server.
- A request field that becomes required, a response field removed or retyped, or a changed default breaks callers that send or read the old shape.
- A changed status, error or exit code breaks callers and scripts that branch on it.
- A new route or tool must check authentication and authorisation as its neighbours do, validate its input and bound what it returns.
- Callers in this repository (API client modules, the app, other services, tests) must change with the interface; one that still uses the old shape will fail at run time.
- Published documents (OpenAPI, JSON Schema, protocol docs, READMEs) must describe the new interface.
- A deprecation should keep the old behaviour working and say what replaces it.
