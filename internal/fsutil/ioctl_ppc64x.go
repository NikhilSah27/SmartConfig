//go:build linux && (ppc64 || ppc64le)

package fsutil

// fsIocGetflags is FS_IOC_GETFLAGS, _IOR('f', 1, long), as powerpc encodes
// it (its read direction bit is another).
const fsIocGetflags = 0x40086601
