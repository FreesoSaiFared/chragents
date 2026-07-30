//go:build windows

package main

import "syscall"

type sysProcAttr = syscall.SysProcAttr

func detachedSysProcAttr() *sysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x00000008 | 0x00000200}
}
