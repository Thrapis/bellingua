// This file only exists so Go tooling (go test ./..., go vet ./...) treats
// web/ as a separate module and skips node_modules. The frontend is built
// with npm; scripts/build.* copy web/out into internal/ui/dist for embedding.
module bellingua-web-ignore

go 1.25
