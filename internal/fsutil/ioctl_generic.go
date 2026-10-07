//go:build linux && (amd64 || arm64 || riscv64 || loong64 || s390x)

package fsutil

// fsIocGetflags is FS_IOC_GETFLAGS, _IOR('f', 1, long), as amd64, arm64,
// riscv64, loong64 and s390x encode it. Only for them (and powerpc, its own
// file), the ports sc builds for (fstatat): another, mips or a 32-bit one,
// encodes it otherwise, and must get its own (M4 follow-up 7).
const fsIocGetflags = 0x80086601
