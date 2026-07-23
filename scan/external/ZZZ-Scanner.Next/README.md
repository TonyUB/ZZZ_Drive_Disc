# ZZZ Scanner Next external component

ZZZ Drive Optimizer v1.15 expects the unmodified ZZZ Scanner Next 1.0.45
self-contained Windows x64 package in this directory. The executable path is:

`scan/external/ZZZ-Scanner.Next/ZZZ-Scanner.Next.exe`

Upstream release:
https://github.com/ZztIsolation/ZZZ-Scanner.Next/releases/tag/scanner-1.0.45

The optimizer starts the scanner with `--scan-once`, waits for a complete
`export.json`, converts the result, and asks the user before changing inventory.
Scanner binaries and models are release artifacts and are intentionally ignored
by Git; this README and the upstream MIT license remain tracked.
