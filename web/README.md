# bellingua web UI

Next.js 16 (static export) + shadcn/ui (Radix) front end, embedded into the Go
binary. See the repository README and CLAUDE.md.

```bash
npm run dev     # :3000, proxies /api to the Go server on 127.0.0.1:7070
npm run build   # static export to out/ (scripts/build.* embed it)
```
