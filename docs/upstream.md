# Taking the session guard upstream

The session guard works today as a loadable plugin. This page describes how it would land in Bifrost itself, and shows that the code is already in a shape to do so.

## Why upstream matters

A custom Go plugin needs a dynamically linked gateway, the same Go version, and the same version of every shared package. Bifrost's published binary is statically linked, so it cannot load one. As a built-in, the session guard would be available on the standard binary with one line of configuration:

```json
{ "plugins": [ { "name": "session-guard", "enabled": true, "config": {} } ] }
```

## It already works as a built-in

`upstream/bifrost-v2.5.0-builtin.patch` registers the guard in the gateway. It adds 20 lines to two files and changes nothing else:

| File | Change |
| --- | --- |
| `transports/bifrost-http/lib/config.go` | Adds `session-guard` to the list of built-in plugin names. |
| `transports/bifrost-http/server/plugins.go` | Creates the plugin in `loadBuiltinPlugin`, and registers it in `loadBuiltinPlugins` when its config entry is present and enabled. |

To build and test a gateway with the patch applied:

```bash
make gateway-builtin          # .work/bin/bifrost-http-builtin
STATIC=1 make e2e-builtin     # statically linked, like Bifrost's releases; runs the 16 end-to-end tests
```

The build leaves the Bifrost source tree untouched. It applies the patch to copies and passes them to the compiler with Go's `-overlay` and `-modfile` flags.

All 16 end-to-end tests pass against that statically linked gateway, with no shared object involved.

## What a pull request would contain

1. **The plugin as a module in Bifrost's repository**, at `plugins/sessionguard`, next to `governance` and `semanticcache`. The `guard` package has no Bifrost imports and the adapter imports only `core/schemas`, so the code moves as it is.
2. **The registration** from the patch above.
3. **Constructor in house style.** Bifrost's built-ins expose `Init(ctx, config, logger)`. The guard's `New(config)` would gain the context and logger and use Bifrost's logger for start-up messages.
4. **Schema and documentation.** An entry for the plugin's config in Bifrost's config schema, and a page under `docs/features/plugins/`. The [configuration reference](configuration.md) is written to be moved there.
5. **Tests.** The Go unit tests move with the code. The end-to-end scenarios would be ported to Bifrost's own test harness. The [study with real models](../study/README.md) would go with the pull request as evidence for the default thresholds.

## Open questions for the maintainers

These are choices the maintainers are better placed to make.

- **Plugin order.** The patch places the guard after governance, so the virtual key is known, and after the semantic cache, so cache hits skip it. Should cached responses count toward a session's limits?
- **Shared state.** Counters are in memory per process. Bifrost has a KV store for session stickiness. Should session counters use it, so limits hold across replicas?
- **Status surface.** The guard serves its own status page so that it works on any build. Upstream, the session list would fit better in Bifrost's dashboard and under its `/api` routes and auth.
- **A failure flag for tool results.** The guard has to read the text of a tool result to tell that a call failed, because Bifrost's normalised request has no such field, although Anthropic's format carries `is_error`. If Bifrost kept that flag, the guard could act on failed calls with confidence instead of only reporting them.
- **Relation to Enterprise.** Bifrost Enterprise has a circuit breaker that fails over when a provider degrades. The session guard acts on a different thing, the caller's session, and uses no Enterprise code. The name was chosen to keep the two apart.
