# REST API

`aveloxis api` serves the collected data over HTTP and JSON: repository
statistics and series, contributors, dependency licenses, SBOMs,
vulnerability findings, the comparison analytics, and the account, group
and administration operations behind the web front end. Every path is
under `/api/v1`.

This section has three parts:

- **[Conventions](conventions.md)**: the naming and design rules every
  operation follows, and the shared contract (access, errors, rate
  limits, caching, times). Read it once; the reference does not repeat it.
- **[Reference](reference.rst)**: every operation, with its parameters,
  request body, responses and access. It is rendered from one OpenAPI 3.1
  document, {download}`openapi.yaml <openapi.yaml>`, which a test keeps
  equal to the routes the server registers. Load that file into any
  OpenAPI tool to generate a client.
- **[The REST API guide](../guide/api.md)**: what the data means and why
  each endpoint behaves as it does (how contributions are counted, the
  vulnerability classes, the comparison metrics), how to get a token, and
  how the API is deployed.

## A first call

The API listens on `api.addr` (`127.0.0.1:8383` by default). A liveness
check needs no token:

```bash
curl -s http://127.0.0.1:8383/api/v1/health
```

Data operations take a Bearer token: a signed-in session from the web
front end's `/auth/token`, or an API token an administrator granted
(`avx_…`). [Getting a token](../guide/api.md#authentication-getting-an-api-token)
explains both.

```bash
API=http://127.0.0.1:8383
read -rs TOKEN   # paste the token; it is not echoed
curl -s -H "Authorization: Bearer $TOKEN" "$API/api/v1/repos/42/stats"
```

```{toctree}
:maxdepth: 2

conventions
reference
```
