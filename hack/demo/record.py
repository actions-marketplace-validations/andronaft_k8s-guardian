#!/usr/bin/env python3
"""Record the README demo: runs real k8s-guardian commands in a pseudo-terminal
and writes an asciicast (docs/demo.cast). The cluster answers come from the
scripted fake kubectl used by the tests (internal/cli/testdata/fakebin), so
the recording is reproducible without a cluster.

    make build && python3 hack/demo/record.py
    agg --font-family "DejaVu Sans Mono,Noto Color Emoji" --font-size 14 \\
        --theme monokai --idle-time-limit 3 --last-frame-duration 4 docs/demo.cast docs/demo.gif
"""
import fcntl, json, os, pty, select, shutil, struct, tempfile, termios, time

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
W, H = 124, 30
work = tempfile.mkdtemp()
shutil.copy(f"{ROOT}/examples/insecure-deployment.yaml", f"{work}/deploy.yaml")
shutil.copy(f"{ROOT}/examples/costly-app.yaml", f"{work}/app.yaml")
env = dict(os.environ, TERM="xterm-256color",
           PATH=f"{ROOT}/bin:{ROOT}/internal/cli/testdata/fakebin:/usr/bin:/bin")
env.pop("NO_COLOR", None)

events, t = [], 0.0


def emit(s, dt=0.0):
    global t
    t += dt
    events.append([round(t, 3), "o", s])


def run(cmd, pause):
    emit("\x1b[1;32m$\x1b[0m ", 0.6)
    for ch in cmd:
        emit(ch, 0.035)
    emit("\r\n", 0.35)
    pid, fd = pty.fork()
    if pid == 0:
        os.chdir(work)
        os.execvpe("bash", ["bash", "-c", cmd], env)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", H, W, 0, 0))
    last = time.time()
    while True:
        r, _, _ = select.select([fd], [], [], 0.05)
        if r:
            try:
                data = os.read(fd, 4096)
            except OSError:
                break
            if not data:
                break
            now = time.time()
            emit(data.decode(errors="replace"), min(now - last, 0.08))
            last = now
    os.waitpid(pid, 0)
    emit("", pause)


def clear():
    emit("\x1b[2J\x1b[H", 0.3)


emit("\x1b[1;36m# 🛡️  k8s-guardian: Kubernetes guardrails that know your cluster\x1b[0m\r\n", 0.2)
run("k8s-guardian check -f deploy.yaml", 3.0)
run("k8s-guardian check -f deploy.yaml --fix 2>&1 | tail -5", 3.0)
clear()
run("k8s-guardian check -f app.yaml --live --skip KG003,KG005,KG006,KG007,KG008,KG009,KG013,KG014,KG016", 3.2)
clear()
run("k8s-guardian diff -f app.yaml", 3.5)
clear()
run("k8s-guardian cost -f app.yaml --usage 2>/dev/null | head -9", 3.2)
run("k8s-guardian export vap 2>&1 >guardrails.yaml | tail -1", 3.0)

with open(f"{ROOT}/docs/demo.cast", "w") as f:
    f.write(json.dumps({"version": 2, "width": W, "height": H, "title": "k8s-guardian"}) + "\n")
    for e in events:
        f.write(json.dumps(e) + "\n")
shutil.rmtree(work)
print(f"docs/demo.cast: {t:.1f}s")
