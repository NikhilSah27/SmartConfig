//go:build linux && !ppc64 && !ppc64le

package fsutil

// fsIocGetflags is FS_IOC_GETFLAGS, _IOR('f', 1, long), as amd64, arm64,
// riscv64, loong64 and s390x encode it.
const fsIocGetflags = 0x80086601
