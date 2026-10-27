package projectctl

// skipPythonDir：Linux 上 PATH 里的目录都可以探测。
func skipPythonDir(dir string) bool { return false }
