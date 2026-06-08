package app

import (
	"testing"
	"time"
)

func TestFormatInterval(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},                             // zero stays "0s" (only surviving 0)
		{-5 * time.Second, "0s"},              // negative clamps to "0s"
		{30 * time.Second, "30s"},             // seconds only
		{time.Minute, "1m"},                   // 1m0s -> 1m
		{90 * time.Second, "1m30s"},           // mixed minutes+seconds
		{time.Hour, "1h"},                     // 1h0m0s -> 1h
		{time.Hour + 30*time.Minute, "1h30m"}, // 1h30m0s -> 1h30m
		{time.Hour + time.Second, "1h1s"},     // 1h0m1s -> 1h1s (skip the zero minute)
		{2*time.Hour + 15*time.Minute + 30*time.Second, "2h15m30s"},
		{24 * time.Hour, "1d"},                   // day unit
		{25 * time.Hour, "1d1h"},                 // day + hour
		{7 * 24 * time.Hour, "1w"},               // week unit
		{9 * 24 * time.Hour, "1w2d"},             // week + days
		{30 * 24 * time.Hour, "1mo"},             // month (30d approximation)
		{31 * 24 * time.Hour, "1mo1d"},           // month + day
		{37 * 24 * time.Hour, "1mo1w"},           // month + week
		{90 * 24 * time.Hour, "3mo"},             // multiple months
		{8*24*time.Hour + 3*time.Hour, "1w1d3h"}, // week + day + hour chain
		{1500 * time.Millisecond, "1.5s"},        // sub-second falls back to stdlib
	}
	for _, c := range cases {
		if got := formatInterval(c.in); got != c.want {
			t.Errorf("formatInterval(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
