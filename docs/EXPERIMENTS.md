# Historical transport experiments

The finite HTTP profiles, browser worker, local WSS bridge, generic hybrid and
pressure-hint experiments are retired. Their source, tests and measured outcomes
remain in Git history. They are not supported configuration choices.

Current production behavior is defined in [PROTOCOL.md](PROTOCOL.md); current
site layout and server configuration are in the [deployment instructions](../README.md#site-directory-and-caddyfile). The native
client's CAPTURE.md preserves the measured original labels and explains the
promotion of the productive shaped-WebSocket implementation to no-connect.
Classic remains the default. New measurements must identify current source,
runtime and application inputs rather than silently relabel historical data.
