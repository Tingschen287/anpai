package agentdeck

// Linux 的本平台进程表就是 /proc。
const nativeProcTableName = "/proc"

func nativePIDs() ([]int, error) { return procPIDs("/proc") }

func nativeChildren() map[int][]int { return procChildren("/proc") }

func nativeCmdline(pid int) ([]byte, error) { return procCmdlineFile("/proc", pid) }
