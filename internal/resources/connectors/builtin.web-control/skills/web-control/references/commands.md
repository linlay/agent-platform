# Standard CDP gateway compatibility

This reference is only for explicitly debugging the loopback gateway. Ordinary page control uses the surface tools and a page surfaceId.

The gateway exposes GET /json/version and /json/list. /json/list returns standard descriptors with an id and webSocketDebuggerUrl; that id is the same page Surface identity. Open the returned WebSocket to send Chromium commands through the bundled cdp-send.mjs helper. The raw CDP Target.closeTarget method retains its standard params.targetId spelling as a protocol adapter only; it does not define a second Desktop identity.

Only the foreground application is exposed on this unauthenticated loopback compatibility path. It cannot borrow Chat/Run grants. /json/new is disabled. Never switch to this transport to bypass a tool authorization failure.
