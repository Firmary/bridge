package service

import (
	"context"
	"errors"
)

var errLinuxOnly = errors.New("only on Linux")

// The background service is for Linux servers without systemd; Windows has its logon task.

func installBackground(Config) (Result, error) { return Result{}, errLinuxOnly }
func uninstallBackground(Config) error         { return nil }
func backgroundInstalled(Config) bool          { return false }
func queryBackground(Config) Status            { return Status{} }
func startBackground(Config) error             { return errLinuxOnly }
func stopBackground(Config) error              { return nil }
func Supervise(context.Context, string) error  { return errLinuxOnly }
