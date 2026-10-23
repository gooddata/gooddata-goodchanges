module goodchanges

go 1.27.3

replace goodchanges/tsgo-vendor => ./_vendor/typescript-go

require (
	github.com/bmatcuk/doublestar/v4 v4.10.3
	goodchanges/tsgo-vendor v0.0.0-00010101000000-000000000000
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/klauspost/compress v1.20.0 // indirect
	github.com/klauspost/cpuid/v2 v2.4.1 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)
