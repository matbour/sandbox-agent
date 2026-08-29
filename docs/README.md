# Documentation

Read the relevant file before working on a topic. Add to it when you learn something that cost time
to work out, and mark anything not verified as "to verify".

| File | Read it when |
| --- | --- |
| [host.md](host.md) | Anything that runs on the board: kernel features, users, AppArmor, other services, ingress |
| [architecture.md](architecture.md) | Components, flow from issue to PR, proxy, patch validation, credentials, limits |
| [threat-model.md](threat-model.md) | Before adding or relaxing any isolation or network rule |
| [sandbox.md](sandbox.md) | Implementing layer 1: namespaces, mounts, cgroups, capabilities, Landlock, seccomp |
| [docker-sandboxes.md](docker-sandboxes.md) | Installing, scripting or testing Docker Sandboxes (`sbx`), the first runtime |
| [microvm.md](microvm.md) | Firecracker spike, gVisor fallback, layer comparison |
| [agents.md](agents.md) | Agent backends, the input/output contract, the prompt |
| [escape-tests.md](escape-tests.md) | The list of escape tests and their expected results |
| [roadmap.md](roadmap.md) | What to do next |
| [decisions.md](decisions.md) | Why things are the way they are |
| [open-questions.md](open-questions.md) | What is still undecided or unverified |
