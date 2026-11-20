//go:build !unix

package main

import (
	"errors"
	"time"
)

var errNoJobs = errors.New("jobs run on macOS and Linux only")

func startJob([]string) error             { return errNoJobs }
func keepJob([]string) error              { return errNoJobs }
func showJobs([]string) error             { return errNoJobs }
func stopJob(string, time.Duration) error { return errNoJobs }
func jobLogPath(string) (string, error)   { return "", errNoJobs }
func isJob(name string) bool              { return false }
