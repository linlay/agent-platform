#!/usr/bin/env python3
"""Derive the Compose transport from the authoritative Desktop SDK contract."""
import argparse,hashlib,pathlib
p=argparse.ArgumentParser();p.add_argument('--desktop-root',required=True);p.add_argument('--check',action='store_true');a=p.parse_args()
source=(pathlib.Path(a.desktop_root)/'contracts/webapp/bridge.mjs').read_text()
value=source
# Page requests use host-only sessions and the authenticated bootstrap CSRF.
helper='''async function cloudPageHeaders() {
  const response = await fetch("/__desktop/bootstrap.json", { credentials: "same-origin", cache: "no-store", redirect: "error" });
  const value = await response.json();
  if (!response.ok || typeof value.csrfToken !== "string" || !value.csrfToken) throw new DesktopBridgeError("page.session", "app_grant_required", "Application session is unavailable.");
  return { "X-WebApp-CSRF": value.csrfToken };
}

'''
value=helper+value
old='...(transport ? { Authorization: "Bearer " + transport.token } : {})'
assert value.count(old)==1
value=value.replace(old,'...(transport ? { Authorization: "Bearer " + transport.token } : await cloudPageHeaders())')
value=value.replace('credentials: "omit", redirect: "error", signal,','credentials: transport ? "omit" : "same-origin", redirect: "error", signal,',1)
old='await fetch(ACTION_PATH, { method: "POST", headers: { "Content-Type": "application/json" },'
assert value.count(old)==1
value=value.replace(old,'await fetch(ACTION_PATH, { method: "POST", headers: { "Content-Type": "application/json", ...await cloudPageHeaders() },')
value=value.replace('body, credentials: "omit", redirect: "error", signal: combined','body, credentials: "same-origin", redirect: "error", signal: combined')
# Cloud has no Desktop visitor preload. Do not use it even when embedded.
start=value.index('export const auth = Object.freeze(');end=value.index('\nexport const desktop =',start)
value=value[:start]+'''export const auth = Object.freeze({ isAvailable: () => false, createSession: reserved("auth.createSession") });
'''+value[end:]
# Native image inputs cannot use the hosted bridge. Report the unsupported
# capability before attempting any upload or local Desktop action.
value=value.replace('Object.freeze(image), executeAssistant','Object.freeze(Object.assign(reserved("assistant.image"), { cancel: reserved("assistant.image.cancel") })), executeAssistant')
value=value.replace('"not_implemented",\n      "This capability is reserved but not implemented in the current Desktop version."', '"not_supported",\n      "This capability is unavailable in Compose."')
value='// Generated from Desktop SDK SHA256: '+hashlib.sha256(source.encode()).hexdigest()+'\n'+value
output=pathlib.Path(__file__).resolve().parents[1]/'internal/webappbridge/sdk/bridge.mjs'
if a.check:
 if not output.exists() or output.read_text()!=value:raise SystemExit('Compose WebApp SDK is stale')
else:output.write_text(value)
print('Compose WebApp SDK is synchronized')
