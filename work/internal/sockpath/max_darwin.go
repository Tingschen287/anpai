package sockpath

// Max 是 socket 路径允许的最大字节数。macOS 的 sun_path 是 104 字节，留一个给结尾的 NUL。
const Max = 103
