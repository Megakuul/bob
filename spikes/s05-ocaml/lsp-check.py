# Minimal LSP session against .bob/bin/ocamllsp: diagnostics + hover for one file.
import json, subprocess, sys, os, time
root = os.path.abspath(sys.argv[1]); f = os.path.join(root, sys.argv[2]); line, col = int(sys.argv[3]), int(sys.argv[4])
p = subprocess.Popen([os.path.join(root, ".bob/bin/ocamllsp")], cwd=root, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
def send(m):
    b = json.dumps(m).encode(); p.stdin.write(b"Content-Length: %d\r\n\r\n" % len(b) + b); p.stdin.flush()
def recv():
    n = 0
    while True:
        l = p.stdout.readline().strip()
        if not l: break
        if l.lower().startswith(b"content-length"): n = int(l.split(b":")[1])
    return json.loads(p.stdout.read(n))
uri = "file://" + f
t0 = time.time()
send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"processId": None, "rootUri": "file://" + root, "capabilities": {}}})
while recv().get("id") != 1: pass
send({"jsonrpc": "2.0", "method": "initialized", "params": {}})
send({"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": {"textDocument": {"uri": uri, "languageId": "ocaml", "version": 1, "text": open(f).read()}}})
send({"jsonrpc": "2.0", "id": 2, "method": "textDocument/hover", "params": {"textDocument": {"uri": uri}, "position": {"line": line, "character": col}}})
diag = hover = None
while diag is None or hover is None:
    m = recv()
    if m.get("method") == "textDocument/publishDiagnostics": diag = [d["message"][:80] for d in m["params"]["diagnostics"]]
    if m.get("id") == 2: hover = (m.get("result") or {}).get("contents", {})
print("diagnostics:", diag); print("hover:", json.dumps(hover)[:120]); print("time %.2fs" % (time.time() - t0))
p.kill()
