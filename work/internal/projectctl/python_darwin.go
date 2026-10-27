package projectctl

// skipPythonDir：macOS 的 /usr/bin/python3 是 Xcode 命令行工具的壳。没装命令行工具时，执行它会弹出安装窗口；
// 装了的话它自带的是 3.9（低于 projectctl 要求的 3.11）。所以探测时不去碰 /usr/bin。
// 确实要用它的，用 DECK_PROJECTCTL_PYTHON 显式指定。
func skipPythonDir(dir string) bool { return dir == "/usr/bin" }
