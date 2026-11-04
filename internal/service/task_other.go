//go:build !windows

package service

import "errors"

var errWindowsOnly = errors.New("only on Windows")

func installTask(Config) (Result, error) { return Result{}, errWindowsOnly }
func uninstallTask(Config) error         { return errWindowsOnly }
func queryTask(Config) Status            { return Status{} }
func stopTask() error                    { return errWindowsOnly }
func startTask() error                   { return errWindowsOnly }
func setRunKey(string, bool) error       { return errWindowsOnly }
func runKey() (string, bool)             { return "", false }
