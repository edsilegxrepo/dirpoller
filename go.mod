module criticalsys.net/dirpoller

go 1.26.0

require (
	github.com/fsnotify/fsnotify v1.10.1
	github.com/google/uuid v1.6.0
	github.com/klauspost/compress v1.19.1
	github.com/pkg/sftp v1.13.11
	github.com/zeebo/xxh3 v1.1.0
	golang.org/x/crypto v0.54.0
	golang.org/x/sys v0.47.0
)

require github.com/klauspost/cpuid/v2 v2.4.0 // indirect

require (
	criticalsys.net/secretprotector v0.0.0
	github.com/kr/fs v0.1.0 // indirect
)

replace criticalsys.net/secretprotector => ../secretprotector
