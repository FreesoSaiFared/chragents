//go:build !windows

package main

import "syscall"

type sysProcAttr = syscall.SysProcAttr

func detachedSysProcAttr() *sysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
