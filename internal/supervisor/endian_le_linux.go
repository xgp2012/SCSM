//go:build linux && (amd64 || arm64 || riscv64 || ppc64le || loong64 || mips64le || s390x)

package supervisor

// isBigEndian is false for every little-endian target this panel supports.
const isBigEndian = false
