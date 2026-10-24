package main

import "runtime"

func runtimeLock() { runtime.LockOSThread() }
