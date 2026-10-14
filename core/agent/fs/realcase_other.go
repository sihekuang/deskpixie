//go:build !darwin

package agentfs

func onDiskCase(p string) string { return p }
